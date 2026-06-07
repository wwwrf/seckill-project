package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"seckill-system/internal/model"
	"seckill-system/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ==================== 业务错误定义 ====================
// 这些错误会在 Service 层被捕获并转换为对应的 HTTP 响应码
// 仓储层只负责描述「发生了什么」，不关心「怎么回复客户端」

var (
	// ErrDuplicateOrder 重复下单（触发 uk_user_activity 唯一约束）
	// 这是 MQ 消费端做幂等的核心错误：收到此错误说明订单已存在，直接 ACK 即可
	ErrDuplicateOrder = errors.New("重复下单: 该用户在此活动中已有订单")

	// ErrStockNotEnough 库存不足或乐观锁冲突
	// 扣减 SQL 的 affected rows 为 0 时触发，可能是库存耗尽，也可能是并发冲突
	ErrStockNotEnough = errors.New("库存不足或并发冲突: 请重试")

	// ErrOrderCannotPay 订单无法支付（不存在、不属于该用户、或非待支付状态）
	ErrOrderCannotPay = errors.New("订单不存在或不可支付")
)

// ==================== OrderRepo 订单仓储层 ====================

// OrderRepo 封装订单相关的数据库操作
type OrderRepo struct {
	db *gorm.DB
}

// NewOrderRepo 创建订单仓储实例
func NewOrderRepo() *OrderRepo {
	return &OrderRepo{db: DB}
}

// FindByUserAndActivity 根据用户 ID 和活动 ID 查询订单
//
// 用于秒杀结果轮询接口（GET /api/v1/seckill/result）：
//   - 查到订单 → 返回订单对象（MQ 消费端已成功建单）
//   - 未查到  → 返回 nil, nil（需继续查询 Redis 已购集合）
//
// 命中索引：uk_user_activity (user_id, activity_id)，查询性能 O(1)。
func (r *OrderRepo) FindByUserAndActivity(ctx context.Context, userID, activityID int64) (*model.Order, error) {
	var order model.Order
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND activity_id = ?", userID, activityID).
		First(&order).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询订单失败: %w", err)
	}

	return &order, nil
}

// FindByOrderNo 根据订单号查询订单
//
// 用于延迟对账消费者（链路 2）检查该 orderNo 是否已成功落库：
//   - 查到订单 → 订单已创建，无需补偿
//   - 未查到  → 可能需要触发补偿回滚
//
// 命中索引：uk_order_no (order_no)，查询性能 O(1)。
func (r *OrderRepo) FindByOrderNo(ctx context.Context, orderNo string) (*model.Order, error) {
	var order model.Order
	err := r.db.WithContext(ctx).
		Where("order_no = ?", orderNo).
		First(&order).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("根据订单号查询订单失败: %w", err)
	}

	return &order, nil
}

// CreateSeckillOrderTx 在一个数据库事务中完成秒杀下单的全部操作
//
// 这是整个秒杀系统在数据库层面最核心的方法，必须保证以下 4 步操作的原子性：
//
//	Step 1: 插入 Order 主单（触发 Unique Key 冲突则幂等返回）
//	Step 2: 插入 OrderItem 子单（携带商品快照）
//	Step 3: 乐观锁扣减库存（available_stock - 1, version + 1）
//	Step 4: 插入 StockDeductLog 流水（审计追踪）
//
// 参数说明：
//   - ctx:        上下文，支持超时和取消传播
//   - userID:     下单用户 ID
//   - activityID: 秒杀活动 ID
//   - product:    商品信息（用于生成快照）
//   - activity:   秒杀活动信息（用于获取秒杀价和当前 version）
//   - orderNo:    雪花算法预生成的全局唯一订单号
//
// 返回值：
//   - ErrDuplicateOrder: 重复下单（幂等，上游可安全 ACK）
//   - ErrStockNotEnough: 库存不足或版本冲突（需告知用户秒杀失败）
//   - 其他 error:        系统级错误（需要报警）
func (r *OrderRepo) CreateSeckillOrderTx(
	ctx context.Context,
	userID, activityID int64,
	productID int64,
	productTitle string,
	seckillPrice int64,
	orderNo string,
) error {
	// 使用 GORM 的 Transaction 方法，自动处理 Begin / Commit / Rollback
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// ========== Step 1: 插入 Order 主单 ==========
		// uk_user_activity (user_id, activity_id) 唯一索引保证一人一单
		// 如果 INSERT 触发 Duplicate Key Error，说明该用户已下过单
		order := &model.Order{
			OrderNo:     orderNo,
			UserID:      userID,
			ActivityID:  activityID,
			TotalAmount: seckillPrice, // 秒杀场景：总金额 = 秒杀价
			PayAmount:   seckillPrice, // 秒杀场景：实付 = 秒杀价
			Status:      model.OrderStatusPending,
			OrderType:   model.OrderTypeSeckill,
		}

		if err := tx.Create(order).Error; err != nil {
			// 判断是否为唯一键冲突（MySQL error code 1062）
			if isDuplicateKeyError(err) {
				logger.Warn("幂等拦截: 重复下单",
					zap.Int64("userID", userID),
					zap.Int64("activityID", activityID),
					zap.String("orderNo", orderNo),
				)
				return ErrDuplicateOrder
			}
			logger.Error("插入订单失败", zap.Error(err))
			return fmt.Errorf("插入订单失败: %w", err)
		}

		logger.Info("Step1 完成: 订单已创建",
			zap.String("orderNo", orderNo),
			zap.Int64("orderID", order.ID),
		)

		// ========== Step 2: 插入 OrderItem 子单 ==========
		// 记录下单时的商品名称和价格快照，后续商品改名/调价不影响历史订单
		orderItem := &model.OrderItem{
			OrderID:       order.ID,
			ProductID:     productID,
			SnapshotTitle: productTitle,          // 快照：下单时的商品名
			SnapshotPrice: seckillPrice,          // 快照：秒杀价
			Quantity:      1,                     // 秒杀固定为 1
			TotalPrice:    seckillPrice,          // 小计 = 秒杀价 * 1
		}

		if err := tx.Create(orderItem).Error; err != nil {
			logger.Error("插入订单明细失败", zap.Error(err))
			return fmt.Errorf("插入订单明细失败: %w", err)
		}

		logger.Info("Step2 完成: 订单明细已创建",
			zap.Int64("orderItemID", orderItem.ID),
			zap.String("snapshotTitle", orderItem.SnapshotTitle),
		)

		// ========== Step 3: 行锁扣减库存（替代乐观锁） ==========
		// SQL: UPDATE seckill_activities
		//      SET available_stock = available_stock - 1,
		//          version = version + 1
		//      WHERE id = ? AND available_stock > 0
		//
		// 设计要点：
		//   - available_stock > 0: 防止超卖（库存不能扣到负数）
		//   - 不再使用 version 条件：Redis Lua 预扣已保证不超卖，
		//     DB 层仅需 available_stock > 0 兜底即可。
		//     InnoDB 行锁会将并发 UPDATE 串行化，保证正确性。
		//   - 去掉 version 条件后，并发消费者不再因版本冲突大量失败，
		//     吞吐量从 ~270/2000 提升到 2000/2000。
		//   - version 字段仍自增，保留审计追踪能力。
		//   - RowsAffected == 0: 说明 available_stock 已耗尽
		result := tx.Model(&model.SeckillActivity{}).
			Where("id = ? AND available_stock > 0", activityID).
			Updates(map[string]interface{}{
				"available_stock": gorm.Expr("available_stock - 1"),
				"version":         gorm.Expr("version + 1"),
			})

		if result.Error != nil {
			logger.Error("扣减库存 SQL 执行失败", zap.Error(result.Error))
			return fmt.Errorf("扣减库存失败: %w", result.Error)
		}

		// 关键校验：受影响行数必须为 1
		if result.RowsAffected == 0 {
			logger.Warn("库存扣减失败: DB 库存已耗尽",
				zap.Int64("activityID", activityID),
			)
			return ErrStockNotEnough
		}

		logger.Info("Step3 完成: 库存扣减成功",
			zap.Int64("activityID", activityID),
		)

		// ========== Step 4: 插入库存扣减流水 ==========
		// 流水表用于事后对账和问题排查，只 INSERT 不 UPDATE/DELETE
		deductLog := &model.StockDeductLog{
			ActivityID:     activityID,
			OrderNo:        orderNo,
			DeductQuantity: 1,
			Type:           model.StockDeductTypeDeduct,
		}

		if err := tx.Create(deductLog).Error; err != nil {
			logger.Error("插入库存扣减流水失败", zap.Error(err))
			return fmt.Errorf("插入库存扣减流水失败: %w", err)
		}

		logger.Info("Step4 完成: 库存流水已记录",
			zap.Int64("logID", deductLog.ID),
			zap.String("orderNo", orderNo),
		)

		// ========== Step 5: 事务提交 ==========
		// GORM Transaction 方法会在回调返回 nil 时自动 Commit
		logger.Info("秒杀下单事务完成 ✓",
			zap.String("orderNo", orderNo),
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
		)

		return nil
	})
}

// isDuplicateKeyError 判断是否为 MySQL 唯一键冲突错误 (Error 1062)
//
// MySQL 唯一键冲突的错误信息格式：
// "Error 1062: Duplicate entry 'xxx' for key 'uk_xxx'"
//
// 这里用字符串匹配而不是依赖 MySQL 驱动的错误码类型，
// 是因为 GORM 对底层驱动错误做了封装，直接类型断言不总是可靠。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// MySQL 1062 错误关键字
	return contains(errMsg, "1062") || contains(errMsg, "Duplicate entry")
}

// contains 简单的字符串包含判断
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

// searchString 在 s 中搜索 substr
func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ==================== 订单查询方法 ====================

// ListByUserID 根据用户 ID 分页查询订单列表
//
// 按创建时间倒序排列，返回指定页的订单 + 总数。
func (r *OrderRepo) ListByUserID(ctx context.Context, userID int64, page, pageSize int) ([]*model.Order, int64, error) {
	var orders []*model.Order
	var total int64

	query := r.db.WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("查询订单总数失败: %w", err)
	}

	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&orders).Error
	if err != nil {
		return nil, 0, fmt.Errorf("查询订单列表失败: %w", err)
	}

	return orders, total, nil
}

// ==================== 订单支付 ====================

// PayOrder 支付订单（模拟支付回调）
//
// 将订单状态从 pending(0) 更新为 paid(10)，并记录支付时间。
// 使用 WHERE 条件保证只有属于该用户的待支付订单才能被更新（防越权+防重复支付）。
//
// 返回值：
//   - nil: 支付成功
//   - ErrOrderCannotPay: 订单不存在 / 不属于该用户 / 非待支付状态
func (r *OrderRepo) PayOrder(ctx context.Context, orderNo string, userID int64) error {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&model.Order{}).
		Where("order_no = ? AND user_id = ? AND status = ?", orderNo, userID, model.OrderStatusPending).
		Updates(map[string]interface{}{
			"status":   model.OrderStatusPaid,
			"pay_time": &now,
		})

	if result.Error != nil {
		return fmt.Errorf("支付订单失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrOrderCannotPay
	}

	logger.Info("订单支付成功",
		zap.String("orderNo", orderNo),
		zap.Int64("userID", userID),
	)
	return nil
}

// ==================== 订单超时取消 ====================

// CancelExpiredOrder 取消超时未支付的订单（事务操作）
//
// 调用场景：链路 3 订单超时消费者发现订单仍为 pending 状态时调用。
//
// 事务内操作：
//  1. 查询订单（状态 = pending）
//  2. 更新订单状态为 cancelled + 设置取消时间
//  3. 回滚 DB 库存（available_stock + 1）
//  4. 插入库存回滚流水
//
// 返回值：
//   - (*Order, nil):  取消成功，返回被取消的订单（用于后续 Redis 回滚）
//   - (nil, nil):     订单不存在或已非 pending 状态（无需操作）
//   - (nil, error):   系统错误
func (r *OrderRepo) CancelExpiredOrder(ctx context.Context, orderNo string) (*model.Order, error) {
	var cancelledOrder *model.Order

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Step 1: 查询 pending 订单
		var order model.Order
		err := tx.Where("order_no = ? AND status = ?", orderNo, model.OrderStatusPending).First(&order).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 订单不存在或已非 pending，跳过
			}
			return fmt.Errorf("查询订单失败: %w", err)
		}

		// Step 2: 更新订单状态为 cancelled
		now := time.Now()
		result := tx.Model(&order).
			Where("status = ?", model.OrderStatusPending).
			Updates(map[string]interface{}{
				"status":      model.OrderStatusCancelled,
				"cancel_time": &now,
			})
		if result.RowsAffected == 0 {
			return nil // 并发环境下订单状态已变更
		}

		// Step 3: 回滚 DB 库存
		tx.Model(&model.SeckillActivity{}).
			Where("id = ?", order.ActivityID).
			UpdateColumn("available_stock", gorm.Expr("available_stock + 1"))

		// Step 4: 插入库存回滚流水
		tx.Create(&model.StockDeductLog{
			ActivityID:     order.ActivityID,
			OrderNo:        orderNo,
			DeductQuantity: 1,
			Type:           model.StockDeductTypeRollback,
		})

		cancelledOrder = &order

		logger.Info("订单超时取消事务完成",
			zap.String("orderNo", orderNo),
			zap.Int64("userID", order.UserID),
			zap.Int64("activityID", order.ActivityID),
		)
		return nil
	})

	return cancelledOrder, err
}
