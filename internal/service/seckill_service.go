package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/utils"

	"go.uber.org/zap"
)

// ==================== 秒杀核心 Service（Phase 4+5 完整版） ====================
//
// 完整链路：
//
//	用户点击 → HTTP → JWT → 限流 → DoSeckill
//	  → 进程级连击拦截
//	  → TTL 售罄检查
//	  → BloomFilter 防穿透
//	  → 生成 OrderNo
//	  → Lua 原子预扣减（含 Pending Hash 写入）
//	  → 写入用户连击冷却标记
//	  → 写入主消息 outbox（seckill_tx_tasks）
//	  → 返回 {code: 0, order_no: "xxx", msg: "排队中"}
//
// 消费端（Phase 5）：
//
//	链路 1: seckill_tx_topic → DB 事务建单 → HDEL pending
//
// 悬空预扣兜底（Cron 定时扫描，详见 internal/cron/pending_checker.go）：
//
//	每分钟 SCAN seckill:pending:* → 超过 5 分钟且 DB 无订单 → 补偿回滚

// SeckillOrderMessage 秒杀订单 MQ 消息体
//
// 用于 seckill_tx_topic。
// 消费端据此创建 DB 订单。
type SeckillOrderMessage struct {
	OrderNo    string `json:"order_no"`    // 全局唯一订单号（雪花算法生成）
	UserID     int64  `json:"user_id"`     // 用户 ID（来自 JWT Token）
	ActivityID int64  `json:"activity_id"` // 秒杀活动 ID
	ProductID  int64  `json:"product_id"`  // 商品 ID
	Title      string `json:"title"`       // 商品标题快照
	Price      int64  `json:"price"`       // 秒杀价格快照
}

// SeckillResult 秒杀执行结果
//
//	Code=0     + OrderNo → 新下单成功，进入排队
//	Code=20002 + Msg    → 已在排队中
type SeckillResult struct {
	Code    int    // 业务码：0=排队成功, 20002=已在排队
	OrderNo string // 订单号（仅 Code=0 时有值）
	Msg     string // 提示消息
}

// SeckillQueryResult 秒杀结果查询响应（轮询接口）
type SeckillQueryResult struct {
	Status  string `json:"status"`             // "SUCCESS" | "PROCESSING" | "FAILED"
	OrderNo string `json:"order_no,omitempty"` // 订单号（仅 SUCCESS 时有值）
	Msg     string `json:"msg,omitempty"`      // 提示消息
}

// SeckillService 秒杀业务 Service
type SeckillService struct {
	cache      *cache.SeckillCache         // 多级缓存防线
	orderRepo  *repository.OrderRepo       // 订单仓储（轮询查 DB 用）
	txTaskRepo *repository.SeckillTxTaskRepo
}

// NewSeckillService 创建秒杀 Service 实例
func NewSeckillService(
	seckillCache *cache.SeckillCache,
	orderRepo *repository.OrderRepo,
	txTaskRepo *repository.SeckillTxTaskRepo,
) *SeckillService {
	return &SeckillService{
		cache:      seckillCache,
		orderRepo:  orderRepo,
		txTaskRepo: txTaskRepo,
	}
}

// DoSeckill 执行秒杀下单（核心方法 — Phase 4+5 完整版）
//
// 执行流程（8 步，严格顺序）：
//
//	Step 0: 进程级连击拦截（go-cache 3s TTL，零 IO）
//	Step 1: TTL 售罄检查（go-cache 5s TTL，零 IO）
//	Step 2: BloomFilter 防穿透（Redis BF.EXISTS，~0.5ms）
//	Step 3: 雪花算法生成订单号（纯内存，~0.01ms）
//	Step 4: Lua 原子预扣减 + Pending Hash 写入（Redis EVAL，~1ms）
//	Step 5: 写入用户连击冷却标记（go-cache，~0.001ms）
//	Step 6: 持久化主消息 outbox（DB insert）
//	Step 7: 返回 SeckillResult
//
// 返回值：
//   - *SeckillResult: 非 nil 时表示业务层面的正常结果
//   - error: 仅系统级错误或可预期的拦截错误
func (s *SeckillService) DoSeckill(ctx context.Context, userID, activityID, productID int64) (*SeckillResult, error) {

	// ==================== Step 0: 进程级连击拦截 ====================
	if s.cache.CheckUserLocalLimit(userID, activityID) {
		return &SeckillResult{
			Code: e.CodeSeckillQueuing,
			Msg:  "您已经在排队队列中，请勿重复点击",
		}, nil
	}

	// ==================== Step 1: TTL 售罄检查（5s 自愈） ====================
	if s.cache.CheckLocalSoldOut(activityID, productID) {
		return nil, e.ErrSeckillSoldOut
	}

	// ==================== Step 2: BloomFilter 防穿透 ====================
	exists, _ := s.cache.IsActivityItemExist(ctx, activityID, productID)
	if !exists {
		return nil, e.ErrSeckillActivityNotStart
	}

	// ==================== Step 2.5: 前置重复购买检测 ====================
	//
	// 在生成 OrderNo 和执行 Lua 脚本之前，先通过 SISMEMBER 快速检查
	// 用户是否已在 purchased 集合中。如果已购买，直接返回"已在排队中"，
	// 避免进入 Lua 脚本的开销。Lua 脚本中仍保留 SISMEMBER 检查作为
	// 原子性保证的最终防线（防止并发窗口）。
	alreadyPurchased, _ := s.cache.IsUserInPurchasedSet(ctx, activityID, productID, userID)
	if alreadyPurchased {
		s.cache.MarkUserLocalLimit(userID, activityID)
		return &SeckillResult{
			Code: e.CodeSeckillQueuing,
			Msg:  "您已参与过该活动",
		}, nil
	}

	// ==================== Step 3: 雪花算法生成订单号 ====================
	//
	// OrderNo 必须在 Lua 脚本之前生成：
	//   Lua 脚本需要将 orderNo 写入 Pending Hash（HSET field）
	//   这样延迟检查消费者才能通过 orderNo 关联到具体的预扣记录
	orderNo := utils.GenOrderNo()
	now := time.Now().Unix()

	// ==================== Step 4: Lua 原子预扣减 + Pending Hash ====================
	//
	// Lua 脚本在 Redis 中原子执行 5 个操作：
	//   EXISTS → SISMEMBER → GET → DECR + SADD + HSET
	//
	// HSET seckill:pending:{activityID} {orderNo} {timestamp}
	// 该 Pending 记录是延迟对账兜底机制的核心数据：
	//   正常路径：MQ 消费建单后 HDEL
	//   异常路径：5 分钟延迟检查发现仍存在 → 补偿回滚
	if err := s.cache.PreDeductStock(ctx, activityID, productID, userID, orderNo, now); err != nil {
		// 重复购买不再作为错误抛出，而是友好返回"已在排队中"
		if errors.Is(err, e.ErrSeckillRepeatBuy) {
			s.cache.MarkUserLocalLimit(userID, activityID)
			return &SeckillResult{
				Code: e.CodeSeckillQueuing,
				Msg:  "您已经在排队队列中，请勿重复点击",
			}, nil
		}
		return nil, err
	}

	// ==================== Step 5: 写入用户连击冷却标记 ====================
	s.cache.MarkUserLocalLimit(userID, activityID)

	// ==================== Step 6: 写入主消息 Outbox ====================
	msg := SeckillOrderMessage{
		OrderNo:    orderNo,
		UserID:     userID,
		ActivityID: activityID,
		ProductID:  productID,
		Title:      "",
		Price:      0,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		logger.Error("秒杀消息序列化失败（编程错误）",
			zap.String("orderNo", orderNo),
			zap.Int64("userID", userID),
			zap.Error(err),
		)
		return &SeckillResult{
			Code:    0,
			OrderNo: orderNo,
			Msg:     "抢购请求已受理，请查看订单列表",
		}, nil
	}

	if s.txTaskRepo == nil {
		_ = s.cache.RollbackPreDeduct(ctx, activityID, productID, userID, orderNo)
		return nil, errors.New("tx outbox repo is nil")
	}

	task := &model.SeckillTxTask{
		OrderNo:     orderNo,
		UserID:      userID,
		ActivityID:  activityID,
		ProductID:   productID,
		Payload:     msgBytes,
		Status:      model.SeckillTxTaskPending,
		RetryCount:  0,
		NextRetryAt: time.Now(),
	}
	if err := s.txTaskRepo.CreateIfNotExists(ctx, task); err != nil {
		if rbErr := s.cache.RollbackPreDeduct(ctx, activityID, productID, userID, orderNo); rbErr != nil {
			logger.Error("主消息 Outbox 写入失败且 Redis 回滚失败",
				zap.String("orderNo", orderNo),
				zap.Error(rbErr),
			)
		}
		return nil, err
	}
	s.cache.RecordAsyncOrderAccepted(ctx, activityID, productID)

	logger.Info("秒杀下单成功，主消息已写入 Outbox",
		zap.String("orderNo", orderNo),
		zap.Int64("userID", userID),
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
	)

	return &SeckillResult{
		Code:    0,
		OrderNo: orderNo,
		Msg:     "抢购成功，排队处理中",
	}, nil
}

// QuerySeckillResult 查询秒杀结果（轮询接口核心方法）
//
// 查询优先级：
//
//	Level 1: MySQL Order 表查询 → SUCCESS
//	Level 2: 若传入 orderNo，检查 Redis pending → PROCESSING
//	Level 3: Redis purchased 集合查询 → PROCESSING（兼容旧客户端无 orderNo）
//	Level 4: 都不在 → FAILED
func (s *SeckillService) QuerySeckillResult(
	ctx context.Context,
	userID, activityID, productID int64,
	orderNo string,
) (*SeckillQueryResult, error) {

	// Level 1: 查 MySQL Order 表
	order, err := s.orderRepo.FindByUserAndActivity(ctx, userID, activityID)
	if err != nil {
		logger.Error("轮询查询 DB 订单失败",
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
			zap.Error(err),
		)
		return nil, err
	}

	if order != nil {
		return &SeckillQueryResult{
			Status:  "SUCCESS",
			OrderNo: order.OrderNo,
			Msg:     "恭喜！抢购成功",
		}, nil
	}

	// Level 2: 若客户端携带 orderNo，优先检查 pending 中间态
	if orderNo != "" {
		pending, pErr := s.cache.HasPending(ctx, activityID, orderNo)
		if pErr != nil {
			logger.Warn("轮询查询 pending 失败，降级继续",
				zap.String("orderNo", orderNo),
				zap.Error(pErr),
			)
		} else if pending {
			return &SeckillQueryResult{
				Status: "PROCESSING",
				Msg:    "排队处理中，请稍候",
			}, nil
		}
	}

	// Level 3: 查 Redis purchased 集合（兼容旧客户端）
	inSet, err := s.cache.IsUserInPurchasedSet(ctx, activityID, productID, userID)
	if err != nil {
		logger.Error("轮询查询 Redis 已购集合失败",
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		return &SeckillQueryResult{
			Status: "PROCESSING",
			Msg:    "排队发货中，请稍候",
		}, nil
	}

	if inSet {
		return &SeckillQueryResult{
			Status: "PROCESSING",
			Msg:    "排队发货中，请稍候",
		}, nil
	}

	// Level 4: 都不在 → 抢购失败
	return &SeckillQueryResult{
		Status: "FAILED",
		Msg:    "抢购失败或活动已售罄",
	}, nil
}
