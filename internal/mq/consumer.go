package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/mq"

	"github.com/apache/rocketmq-client-go/v2"
	rmqconsumer "github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"go.uber.org/zap"
)

// ==================== RocketMQ PushConsumer 双链路消费者 ====================
//
// 链路 1 — seckill_tx_topic（主流程落库）
//
//	触发：用户秒杀成功后，Producer 异步投递
//	延迟：通常 < 100ms（MQ 正常时）
//	职责：
//	  1. 解析消息 → 获取 Product/Activity 信息
//	  2. 开启 DB 事务：CreateSeckillOrderTx（建单 + 扣 DB 库存 + 写流水）
//	  3. Duplicate Entry → 已处理，ACK
//	  4. 事务成功 → HDEL seckill:pending:{activityID} {orderNo}（抹除悬空）
//	  5. 发送 30 分钟延迟消息到 seckill_order_timeout_topic（支付超时检查）
//
// 链路 2 — seckill_order_timeout_topic（30 分钟支付超时检查）
//
//	触发：链路 1 建单成功后发送 30 分钟延迟消息
//	职责：
//	  1. 查 DB 订单状态
//	     → 已支付/已取消 → ACK
//	  2. 仍为 pending（未支付）→ 取消订单
//	     - DB 事务：更新订单状态 + 回滚 DB 库存 + 记录流水
//	     - Redis：INCR 库存 + SREM 用户购买标记
//
// 悬空预扣兜底由 Cron 定时扫描承担（internal/cron/pending_checker.go）。

// SeckillConsumer 秒杀消费者
type SeckillConsumer struct {
	orderRepo       *repository.OrderRepo    // DB 操作
	timeoutTaskRepo *repository.OrderTimeoutTaskRepo
	seckillCache    *cache.SeckillCache      // Redis 操作
	txConsumer      rocketmq.PushConsumer    // 主流程消费者
	timeoutConsumer rocketmq.PushConsumer    // 订单超时消费者
	nameServer      string                   // NameServer 地址
	txGoroutines    int
	snapshotCache   sync.Map
}

// SeckillOrderMessage MQ 消息体（与 service 层定义一致）
type SeckillOrderMessage struct {
	OrderNo    string `json:"order_no"`
	UserID     int64  `json:"user_id"`
	ActivityID int64  `json:"activity_id"`
	ProductID  int64  `json:"product_id"`
	Title      string `json:"title"`
	Price      int64  `json:"price"`
}

// Config 消费者配置
type Config struct {
	NameServer string // NameServer 地址
}

// NewSeckillConsumer 创建秒杀消费者实例
func NewSeckillConsumer(
	orderRepo *repository.OrderRepo,
	timeoutTaskRepo *repository.OrderTimeoutTaskRepo,
	seckillCache *cache.SeckillCache,
	nameServer string,
	txGoroutines int,
) *SeckillConsumer {
	if txGoroutines <= 0 {
		txGoroutines = 24
	}
	return &SeckillConsumer{
		orderRepo:       orderRepo,
		timeoutTaskRepo: timeoutTaskRepo,
		seckillCache:    seckillCache,
		nameServer:      nameServer,
		txGoroutines:    txGoroutines,
	}
}

// Start 启动双链路消费者
//
// 启动顺序：先启动主流程消费者（链路 1），再启动订单超时消费者（链路 2）。
// 两个消费者使用不同的 ConsumerGroup，各自独立消费、独立重试。
func (sc *SeckillConsumer) Start() error {

	// ========== 链路 1：主流程落库消费者 ==========
	txConsumer, err := rocketmq.NewPushConsumer(
		rmqconsumer.WithNameServer([]string{sc.nameServer}),
		rmqconsumer.WithGroupName(e.GroupSeckillTxConsumer),
		rmqconsumer.WithConsumeFromWhere(rmqconsumer.ConsumeFromLastOffset),
		// 并发消费：多个 Goroutine 并行处理不同消息
		rmqconsumer.WithConsumerModel(rmqconsumer.Clustering),
		// 提升消费并发度：默认 20 → 64，匹配 MySQL maxOpenConns 吞吐
		rmqconsumer.WithConsumeGoroutineNums(sc.txGoroutines),
	)
	if err != nil {
		return fmt.Errorf("创建主流程消费者失败: %w", err)
	}

	// 订阅 seckill_tx_topic
	err = txConsumer.Subscribe(e.TopicSeckillTx, rmqconsumer.MessageSelector{},
		sc.handleTxMessage)
	if err != nil {
		return fmt.Errorf("订阅 %s 失败: %w", e.TopicSeckillTx, err)
	}

	if err = txConsumer.Start(); err != nil {
		return fmt.Errorf("启动主流程消费者失败: %w", err)
	}
	sc.txConsumer = txConsumer

	logger.Info("链路 1 消费者启动成功",
		zap.String("topic", e.TopicSeckillTx),
		zap.String("group", e.GroupSeckillTxConsumer),
	)

	// ========== 链路 2：订单支付超时消费者 ==========
	timeoutConsumer, err := rocketmq.NewPushConsumer(
		rmqconsumer.WithNameServer([]string{sc.nameServer}),
		rmqconsumer.WithGroupName(e.GroupSeckillOrderTimeoutConsumer),
		rmqconsumer.WithConsumeFromWhere(rmqconsumer.ConsumeFromLastOffset),
		rmqconsumer.WithConsumerModel(rmqconsumer.Clustering),
	)
	if err != nil {
		return fmt.Errorf("创建订单超时消费者失败: %w", err)
	}

	err = timeoutConsumer.Subscribe(e.TopicSeckillOrderTimeout, rmqconsumer.MessageSelector{},
		sc.handleTimeoutMessage)
	if err != nil {
		return fmt.Errorf("订阅 %s 失败: %w", e.TopicSeckillOrderTimeout, err)
	}

	if err = timeoutConsumer.Start(); err != nil {
		return fmt.Errorf("启动订单超时消费者失败: %w", err)
	}
	sc.timeoutConsumer = timeoutConsumer

	logger.Info("链路 2 消费者启动成功",
		zap.String("topic", e.TopicSeckillOrderTimeout),
		zap.String("group", e.GroupSeckillOrderTimeoutConsumer),
	)

	return nil
}

// Close 关闭双链路消费者
func (sc *SeckillConsumer) Close() {
	if sc.txConsumer != nil {
		if err := sc.txConsumer.Shutdown(); err != nil {
			logger.Error("关闭主流程消费者失败", zap.Error(err))
		} else {
			logger.Info("主流程消费者已关闭")
		}
	}
	if sc.timeoutConsumer != nil {
		if err := sc.timeoutConsumer.Shutdown(); err != nil {
			logger.Error("关闭订单超时消费者失败", zap.Error(err))
		} else {
			logger.Info("订单超时消费者已关闭")
		}
	}
}

// ==================== 链路 1：主流程落库 ====================

// handleTxMessage 处理 seckill_tx_topic 消息
//
// 消费流程：
//
//	 1. 解析 JSON 消息体 → SeckillOrderMessage
//	 2. 查 DB 获取 Product 和 SeckillActivity 信息
//	 3. 调用 CreateSeckillOrderTx 开启 DB 事务：
//	    - 插入 Order（uk_user_activity 防重）
//	    - 插入 OrderItem（商品快照）
//	    - 乐观锁扣减 Activity 库存
//	    - 插入 StockDeductLog 流水
//	 4. 处理结果：
//	    - Duplicate Entry → 已处理，直接 ACK
//	    - 库存不足 → ACK（Redis 已预扣但 DB 库存不够，记 WARN 日志）
//	    - 事务成功 → HDEL pending → ACK
//	    - 其他错误 → RECONSUME_LATER（RocketMQ 自动重试）
//
// 幂等保证：
//
//	uk_user_activity 唯一索引是幂等的核心。
//	即使同一条消息被重复消费（MQ 重试、网络重传），
//	INSERT 触发 Duplicate Key Error 后直接 ACK，不会产生重复订单。
func (sc *SeckillConsumer) handleTxMessage(ctx context.Context, msgs ...*primitive.MessageExt) (rmqconsumer.ConsumeResult, error) {
	for _, msg := range msgs {
		if err := sc.processTxMessage(ctx, msg); err != nil {
			logger.Error("主流程消息处理失败，将重试",
				zap.String("msgID", msg.MsgId),
				zap.Int("reconsumeTimes", int(msg.ReconsumeTimes)),
				zap.Error(err),
			)
			return rmqconsumer.ConsumeRetryLater, nil
		}
	}
	return rmqconsumer.ConsumeSuccess, nil
}

// processTxMessage 处理单条主流程消息
func (sc *SeckillConsumer) processTxMessage(ctx context.Context, msg *primitive.MessageExt) error {
	// 1. 解析消息体
	var orderMsg SeckillOrderMessage
	if err := json.Unmarshal(msg.Body, &orderMsg); err != nil {
		logger.Error("主流程消息解析失败（丢弃）",
			zap.String("msgID", msg.MsgId),
			zap.String("body", string(msg.Body)),
			zap.Error(err),
		)
		// 消息格式错误无法重试，直接 ACK 丢弃
		return nil
	}

	logger.Info("链路 1: 开始处理秒杀建单消息",
		zap.String("orderNo", orderMsg.OrderNo),
		zap.Int64("userID", orderMsg.UserID),
		zap.Int64("activityID", orderMsg.ActivityID),
		zap.Int64("productID", orderMsg.ProductID),
		zap.String("msgID", msg.MsgId),
	)

	// 1.5 如果订单已被 Cron 判定作废，直接丢弃迟到消息
	canceled, err := sc.seckillCache.IsOrderCanceled(ctx, orderMsg.OrderNo)
	if err != nil {
		return fmt.Errorf("查询作废标记失败: %w", err)
	}
	if canceled {
		logger.Warn("链路 1: 迟到消息命中作废标记，跳过建单",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.String("msgID", msg.MsgId),
		)
		_ = sc.seckillCache.DeletePending(ctx, orderMsg.ActivityID, orderMsg.OrderNo)
		return nil
	}

	// 1.6 抢占订单处理标记，防止与 Cron 回滚并发赛跑
	processingMarked, err := sc.seckillCache.TryMarkOrderProcessing(ctx, orderMsg.OrderNo, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("写入处理中标记失败: %w", err)
	}
	if !processingMarked {
		logger.Info("链路 1: 订单已作废或已被其他消费者处理，跳过",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.String("msgID", msg.MsgId),
		)
		return nil
	}
	defer func() {
		if clearErr := sc.seckillCache.ClearOrderProcessing(ctx, orderMsg.OrderNo); clearErr != nil {
			logger.Warn("链路 1: 清理处理中标记失败",
				zap.String("orderNo", orderMsg.OrderNo),
				zap.Error(clearErr),
			)
		}
	}()

	snapshot, err := sc.getOrderSnapshot(ctx, orderMsg.ActivityID, orderMsg.ProductID, orderMsg.Title, orderMsg.Price)
	if err != nil {
		return fmt.Errorf("获取订单快照失败: %w", err)
	}

	// 2. 开启 DB 事务建单
	txErr := sc.orderRepo.CreateSeckillOrderTx(
		ctx,
		orderMsg.UserID,
		orderMsg.ActivityID,
		orderMsg.ProductID,
		snapshot.Title,
		snapshot.Price,
		orderMsg.OrderNo,
	)

	if txErr != nil {
		// ---- 4a. Duplicate Entry（唯一键冲突）→ 幂等 ACK ----
		if txErr == repository.ErrDuplicateOrder {
			logger.Info("链路 1: 幂等拦截，订单已存在",
				zap.String("orderNo", orderMsg.OrderNo),
				zap.Int64("userID", orderMsg.UserID),
			)
			// 即使是重复消费，也确保 pending 被清理
			_ = sc.seckillCache.DeletePending(ctx, orderMsg.ActivityID, orderMsg.OrderNo)
			return nil
		}

		// ---- 4b. 库存不足 → ACK + 记录 WARN ----
		//
		// 这种情况说明 Redis 预扣成功但 DB 乐观锁扣减失败。
		// 可能的原因：
		//   - Redis 和 DB 库存不一致（Redis 库存 > DB 库存）
		//   - 极高并发下多个消息竞争同一行的乐观锁
		//
		// 处理策略：ACK 消息（避免无限重试），Pending 不清除，
		// 由延迟检查链路 2 决定是否需要回滚 Redis 预扣。
		if txErr == repository.ErrStockNotEnough {
			logger.Warn("链路 1: DB 库存扣减失败（Redis 与 DB 库存可能不一致）",
				zap.String("orderNo", orderMsg.OrderNo),
				zap.Int64("activityID", orderMsg.ActivityID),
			)
			return nil
		}

		// ---- 4c. 其他系统错误 → 返回 error 触发重试 ----
		return txErr
	}

	acceptedAtMillis := sc.extractAcceptedAtMillis(ctx, orderMsg.ActivityID, orderMsg.OrderNo)

	// 3. 事务成功 → HDEL pending（抹除悬空状态）
	if err := sc.seckillCache.DeletePending(ctx, orderMsg.ActivityID, orderMsg.OrderNo); err != nil {
		// HDEL 失败不影响建单结果，仅记录 WARN
		// 延迟检查消费者会发现 DB 已建单，直接 ACK
		logger.Warn("链路 1: HDEL pending 失败（不影响建单结果）",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.Error(err),
		)
	}
	sc.seckillCache.RecordAsyncOrderCreated(ctx, orderMsg.ActivityID, orderMsg.ProductID, acceptedAtMillis)

	// 4. 发送 30 分钟延迟消息 → 订单支付超时检查
	//
	// 如果用户 30 分钟内未支付，链路 2 将自动取消订单并回滚库存。
	// 延迟消息发送失败不影响建单结果，仅记录 WARN。
	timeoutBody, _ := json.Marshal(orderMsg)
	if err := mq.SendOrderTimeoutMessage(
		ctx,
		e.TopicSeckillOrderTimeout,
		timeoutBody,
		orderMsg.OrderNo,
		orderMsg.UserID,
		orderMsg.ActivityID,
		orderMsg.ProductID,
	); err != nil {
		if sc.timeoutTaskRepo != nil {
			task := &model.OrderTimeoutTask{
				OrderNo:     orderMsg.OrderNo,
				UserID:      orderMsg.UserID,
				ActivityID:  orderMsg.ActivityID,
				ProductID:   orderMsg.ProductID,
				Status:      model.OrderTimeoutTaskPending,
				RetryCount:  0,
				NextRetryAt: time.Now().Add(30 * time.Second),
				LastError:   err.Error(),
			}
			if createErr := sc.timeoutTaskRepo.CreateIfNotExists(ctx, task); createErr != nil {
				logger.Error("链路 1: 记录延迟消息补偿任务失败",
					zap.String("orderNo", orderMsg.OrderNo),
					zap.Error(createErr),
				)
			}
		}

		logger.Warn("链路 1: 订单超时延迟消息发送失败（需人工对账）",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.Error(err),
		)
	}

	logger.Info("链路 1: 秒杀建单完成 ✓",
		zap.String("orderNo", orderMsg.OrderNo),
		zap.Int64("userID", orderMsg.UserID),
		zap.Int64("activityID", orderMsg.ActivityID),
	)

	return nil
}

func (sc *SeckillConsumer) extractAcceptedAtMillis(ctx context.Context, activityID int64, orderNo string) int64 {
	raw, exists, err := sc.seckillCache.GetPending(ctx, activityID, orderNo)
	if err != nil || !exists {
		return 0
	}

	var payload struct {
		Tsm int64 `json:"tsm"`
		Ts  int64 `json:"ts"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		if payload.Tsm > 0 {
			return payload.Tsm
		}
		if payload.Ts > 0 {
			return payload.Ts * 1000
		}
	}

	ts, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr == nil && ts > 0 {
		return ts * 1000
	}
	return 0
}

type orderSnapshot struct {
	Title string
	Price int64
}

func (sc *SeckillConsumer) getOrderSnapshot(ctx context.Context, activityID, productID int64, title string, price int64) (*orderSnapshot, error) {
	cacheKey := fmt.Sprintf("%d:%d", activityID, productID)
	if cached, ok := sc.snapshotCache.Load(cacheKey); ok {
		snapshot := cached.(orderSnapshot)
		return &snapshot, nil
	}

	snapshot := orderSnapshot{Title: title, Price: price}
	if snapshot.Title == "" || snapshot.Price <= 0 {
		var row struct {
			Title        string
			SeckillPrice int64
		}
		err := repository.DB.WithContext(ctx).
			Table("seckill_activities AS sa").
			Select("p.title AS title, sa.seckill_price AS seckill_price").
			Joins("JOIN products AS p ON p.id = sa.product_id").
			Where("sa.id = ? AND sa.product_id = ?", activityID, productID).
			Take(&row).Error
		if err != nil {
			return nil, err
		}
		snapshot.Title = row.Title
		snapshot.Price = row.SeckillPrice
	}

	sc.snapshotCache.Store(cacheKey, snapshot)
	return &snapshot, nil
}

// ==================== 链路 2：订单支付超时取消 ====================
//
// 技术选型：RocketMQ 延迟消息（DelayLevel=16, 30 分钟）
//
// 替代方案对比：
//
//	| 方案                     | 优点              | 缺点                          |
//	|--------------------------|-------------------|-------------------------------|
//	| RocketMQ 延迟消息(选用)  | 精确单订单触发     | 依赖 MQ 可靠性                 |
//	| 定时任务扫描 DB          | 实现简单           | 扫描频率 vs 精度矛盾，DB 压力  |
//	| Redis Key 过期回调       | 精确，轻量         | 不保证可靠投递（可能丢失）      |
//	| 时间轮 (HashedWheel)    | 内存高效，精确     | 进程重启后任务丢失              |
//
// 为什么不用 Redis Keyspace Notifications（KEY_EVENT_EXPIRED）：
//
//	Redis 的 keyspace notification 机制存在以下致命缺陷，不适用于订单超时场景：
//
//	1. 不保证投递（Fire-and-Forget）：
//	   Redis Pub/Sub 是"发后即忘"语义，如果消费端在通知发出时不在线
//	   （网络断开、进程重启、GC 暂停），该通知将永久丢失，无法重试。
//	   而 RocketMQ 延迟消息具有持久化存储 + ACK 机制 + 自动重试16次。
//
//	2. 过期时间不精确：
//	   Redis 采用惰性删除 + 定期抽样删除策略，Key 过期后不一定立即触发通知。
//	   在高负载场景下，实际触发时间可能比设定 TTL 晚数秒甚至数分钟。
//	   RocketMQ 延迟消息基于 Broker 端定时调度，时间偏差控制在秒级以内。
//
//	3. 无法获取过期 Key 的 Value：
//	   keyspace notification 只告诉你"哪个 Key 过期了"，不携带 Value。
//	   而订单超时需要 orderNo/userID/activityID 等完整信息来执行取消。
//	   绕过方案是额外维护一份映射关系，增加了复杂度和数据一致性风险。
//
//	4. 集群模式下不可靠：
//	   Redis Cluster 中 keyspace notification 只在 Key 所在的 shard 本地触发，
//	   客户端必须订阅所有 shard 的通知频道，部署和运维复杂度极高。
//
//	综上，RocketMQ 延迟消息是订单超时取消的最佳方案：
//	  持久化 + ACK + 自动重试 = 高可靠
//	  消息体携带完整业务数据 = 自包含
//	  Broker 端定时调度 = 时间精确

// handleTimeoutMessage 处理 seckill_order_timeout_topic 延迟消息
func (sc *SeckillConsumer) handleTimeoutMessage(ctx context.Context, msgs ...*primitive.MessageExt) (rmqconsumer.ConsumeResult, error) {
	for _, msg := range msgs {
		if err := sc.processTimeoutMessage(ctx, msg); err != nil {
			logger.Error("订单超时消息处理失败，将重试",
				zap.String("msgID", msg.MsgId),
				zap.Int("reconsumeTimes", int(msg.ReconsumeTimes)),
				zap.Error(err),
			)
			return rmqconsumer.ConsumeRetryLater, nil
		}
	}
	return rmqconsumer.ConsumeSuccess, nil
}

// processTimeoutMessage 处理单条订单超时消息
//
// 流程：
//  1. 查 DB 订单状态
//  2. 已支付/已取消 → ACK
//  3. 仍为 pending → 取消订单（DB 事务）+ 回滚 Redis 库存和已购标记
func (sc *SeckillConsumer) processTimeoutMessage(ctx context.Context, msg *primitive.MessageExt) error {
	// 1. 解析消息体
	var orderMsg SeckillOrderMessage
	if err := json.Unmarshal(msg.Body, &orderMsg); err != nil {
		logger.Error("订单超时消息解析失败（丢弃）",
			zap.String("msgID", msg.MsgId),
			zap.String("body", string(msg.Body)),
			zap.Error(err),
		)
		return nil
	}

	logger.Info("链路 2: 开始检查订单支付超时",
		zap.String("orderNo", orderMsg.OrderNo),
		zap.Int64("userID", orderMsg.UserID),
		zap.Int64("activityID", orderMsg.ActivityID),
		zap.Duration("elapsed", 30*time.Minute),
	)

	// 2. 查 DB 订单状态
	order, err := sc.orderRepo.FindByOrderNo(ctx, orderMsg.OrderNo)
	if err != nil {
		return fmt.Errorf("查询订单失败: %w", err)
	}

	if order == nil {
		// 订单不存在 → 可能链路 1 从未成功建单（已被 Cron 补偿回滚）
		logger.Info("链路 2: 订单不存在，跳过",
			zap.String("orderNo", orderMsg.OrderNo),
		)
		return nil
	}

	// 3. 检查订单状态
	if order.Status != model.OrderStatusPending {
		// 已支付或已取消 → 无需操作
		logger.Info("链路 2: 订单已非 pending 状态，跳过",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.Int8("status", order.Status),
		)
		return nil
	}

	// 4. 订单仍为 pending → 执行超时取消
	cancelledOrder, err := sc.orderRepo.CancelExpiredOrder(ctx, orderMsg.OrderNo)
	if err != nil {
		return fmt.Errorf("取消超时订单失败: %w", err)
	}

	if cancelledOrder == nil {
		// 并发环境下订单状态已变更，跳过
		logger.Info("链路 2: 订单状态已变更，跳过取消",
			zap.String("orderNo", orderMsg.OrderNo),
		)
		return nil
	}

	// 5. 回滚 Redis 库存和已购标记
	if err := sc.seckillCache.CancelOrderRollback(
		ctx,
		orderMsg.ActivityID,
		orderMsg.ProductID,
		orderMsg.UserID,
	); err != nil {
		// Redis 回滚失败不影响 DB 取消结果，记 WARN
		logger.Warn("链路 2: Redis 库存回滚失败（DB 已取消，Redis 库存可能泄漏）",
			zap.String("orderNo", orderMsg.OrderNo),
			zap.Error(err),
		)
	}

	logger.Warn("✅ 链路 2: 订单支付超时已自动取消",
		zap.String("orderNo", orderMsg.OrderNo),
		zap.Int64("userID", orderMsg.UserID),
		zap.Int64("activityID", orderMsg.ActivityID),
		zap.String("action", "DB cancel + Redis INCR stock + SREM user"),
	)

	return nil
}
