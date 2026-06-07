package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"strconv"
	"sync/atomic"
	"time"

	"seckill-system/internal/model/e"
	"seckill-system/pkg/logger"

	gocache "github.com/patrickmn/go-cache"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ==================== SeckillCache 多级缓存防线（Pending 版） ====================
//
// Phase 3+5 完整版：在 TTL 自愈缓存的基础上，
// 为 Lua 预扣脚本新增 Pending Hash 写入，
// 并提供补偿回滚 Lua 脚本供延迟检查消费者调用。
//
// Pending Hash 设计原理：
//
//	Redis 预扣成功（库存 -1, SADD 用户）后，将 orderNo 写入
//	seckill:pending:{activityID} Hash（field=orderNo, value=timestamp）。
//	MQ 消费端建单成功后 HDEL 该条目。
//	如果 5 分钟后延迟检查发现该条目仍然存在，说明：
//	  - MQ 消息丢失
//	  - 消费者崩溃未 ACK
//	  - 任何导致 DB 订单未建立的异常
//	此时触发补偿回滚：INCR 库存 + SREM 用户 + HDEL pending。

// ==================== TTL 常量配置 ====================

const (
	// soldOutTTL 售罄标记 TTL
	//
	// 5 秒过期设计的权衡：
	//   太短（<2s）：售罄后大量请求穿透到 Redis，失去本地缓存意义
	//   太长（>30s）：补货后用户长时间无法参与，体验差
	//   5s 是较好的折中：售罄后 5s 内完全挡住，5s 后放行少量试探请求
	soldOutTTL = 5 * time.Second

	// soldOutCleanup 过期条目扫描清理间隔
	soldOutCleanup = 10 * time.Second

	// userLimitTTL 用户连击冷却 TTL
	//
	// 3 秒冷却设计原理：
	//   正常用户从点击到看到结果需要 1~2 秒，3 秒内重复点击属于手抖或脚本
	//   对正常用户无感知（因为已经拿到结果了），对恶意脚本有显著抑制
	userLimitTTL = 3 * time.Second

	// userLimitCleanup 过期条目扫描清理间隔
	userLimitCleanup = 6 * time.Second

	// canceledMarkerTTL 作废标记默认 TTL
	// 需覆盖 MQ 重试窗口，避免迟到消息绕过拦截导致脏建单
	canceledMarkerTTL = 24 * time.Hour
)

const (
	// CancelMarkCreated Cron 成功创建作废标记
	CancelMarkCreated = 1
	// CancelMarkAlreadyExists 作废标记已存在（已处理过）
	CancelMarkAlreadyExists = 0
	// CancelMarkNoPending pending 条目不存在（可能已被其他链路清理）
	CancelMarkNoPending = -1
	// CancelMarkProcessing 订单正在被 MQ 消费者处理
	CancelMarkProcessing = -2
)

// SeckillCache 秒杀缓存服务（Pending 版）
//
// 核心数据结构：
//   - rdb:            Redis 客户端（BloomFilter + Lua 脚本 + SISMEMBER + Pending Hash）
//   - soldOutCache:   售罄标记本地缓存（5s TTL，过期自愈）
//   - userLimitCache: 用户连击限制缓存（3s TTL，冷却后放行）
type SeckillCache struct {
	rdb            *redis.Client
	soldOutCache   *gocache.Cache // key: "{activityID}_{productID}", value: true
	userLimitCache *gocache.Cache // key: "limit:{userID}:{activityID}", value: true
	bloomDisabled  atomic.Bool
}

// NewSeckillCache 创建秒杀缓存实例并启动 Pub/Sub 监听
func NewSeckillCache(rdb *redis.Client) *SeckillCache {
	sc := &SeckillCache{
		rdb:            rdb,
		soldOutCache:   gocache.New(soldOutTTL, soldOutCleanup),
		userLimitCache: gocache.New(userLimitTTL, userLimitCleanup),
	}

	// 启动 Pub/Sub 补货监听
	go sc.subscribeRestock()

	return sc
}

// ==================== 进程级用户连击防护 ====================

// CheckUserLocalLimit 检查用户是否在连击冷却期内
//
// 返回值：
//   - true:  3s 冷却期内，应直接拦截并友好提示"已在排队中"
//   - false: 冷却期已过或首次请求，放行进入后续流程
func (sc *SeckillCache) CheckUserLocalLimit(userID, activityID int64) bool {
	key := fmt.Sprintf("limit:%d:%d", userID, activityID)
	_, found := sc.userLimitCache.Get(key)
	return found
}

// MarkUserLocalLimit 写入用户连击冷却标记
func (sc *SeckillCache) MarkUserLocalLimit(userID, activityID int64) {
	key := fmt.Sprintf("limit:%d:%d", userID, activityID)
	sc.userLimitCache.Set(key, true, gocache.DefaultExpiration)
}

// ==================== 防线 1: RedisBloom 防穿透 ====================

// IsActivityItemExist 通过 BloomFilter 检查活动商品组合是否存在
//
// BF.EXISTS 返回 1 表示可能存在，0 表示一定不存在。
// 降级策略：RedisBloom 不可用时放行，由后续防线兜底。
func (sc *SeckillCache) IsActivityItemExist(ctx context.Context, activityID, productID int64) (bool, error) {
	if sc.bloomDisabled.Load() {
		return true, nil
	}

	member := e.BuildBloomMember(activityID, productID)

	cmd := sc.rdb.Do(ctx, "BF.EXISTS", e.KeySeckillBloomItems, member)
	if err := cmd.Err(); err != nil {
		sc.disableBloomOnUnsupported(err)
		logger.Error("BloomFilter BF.EXISTS 执行失败，降级放行",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		return true, err
	}

	result, err := cmd.Bool()
	if err != nil {
		sc.disableBloomOnUnsupported(err)
		logger.Error("BloomFilter BF.EXISTS 执行失败，降级放行",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		return true, err
	}

	exists := result
	if !exists {
		logger.Warn("BloomFilter 拦截: 活动商品不存在",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
		)
	}

	return exists, nil
}

func (sc *SeckillCache) disableBloomOnUnsupported(err error) {
	if err == nil || sc.bloomDisabled.Load() {
		return
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "ERR unknown command 'BF.EXISTS'") ||
		strings.Contains(errMsg, "ERR unknown command 'BF.ADD'") {
		sc.bloomDisabled.Store(true)
		logger.Warn("RedisBloom 模块不可用，已关闭 Bloom 检查并降级到后续防线")
	}
}

// ==================== 防线 2: 本地售罄标记（TTL 自愈版） ====================

// CheckLocalSoldOut 检查本地进程内的售罄标记
func (sc *SeckillCache) CheckLocalSoldOut(activityID, productID int64) bool {
	key := e.BuildSoldOutMapKey(activityID, productID)
	_, found := sc.soldOutCache.Get(key)
	return found
}

// markLocalSoldOut 标记本地售罄（带 TTL 自愈）
func (sc *SeckillCache) markLocalSoldOut(activityID, productID int64) {
	key := e.BuildSoldOutMapKey(activityID, productID)
	sc.soldOutCache.Set(key, true, gocache.DefaultExpiration)
	logger.Info("本地售罄标记已写入（TTL=5s）",
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
	)
}

// clearLocalSoldOut 清除本地售罄标记（补货时调用）
func (sc *SeckillCache) clearLocalSoldOut(activityID, productID int64) {
	key := e.BuildSoldOutMapKey(activityID, productID)
	sc.soldOutCache.Delete(key)
	logger.Info("本地售罄标记已清除（收到补货信号）",
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
	)
}

// restockMessage 补货消息结构
type restockMessage struct {
	ActivityID int64 `json:"activityId"`
	ProductID  int64 `json:"productId"`
}

// subscribeRestock 订阅 Redis Pub/Sub 补货频道
func (sc *SeckillCache) subscribeRestock() {
	pubsub := sc.rdb.Subscribe(context.Background(), e.TopicSeckillRestock)
	defer pubsub.Close()

	logger.Info("Pub/Sub 补货监听已启动",
		zap.String("topic", e.TopicSeckillRestock),
	)

	ch := pubsub.Channel()
	for msg := range ch {
		var restock restockMessage
		if err := json.Unmarshal([]byte(msg.Payload), &restock); err != nil {
			logger.Error("解析补货消息失败",
				zap.String("payload", msg.Payload),
				zap.Error(err),
			)
			continue
		}

		if restock.ActivityID == 0 || restock.ProductID == 0 {
			logger.Warn("补货消息字段不完整，跳过",
				zap.String("payload", msg.Payload),
			)
			continue
		}

		sc.clearLocalSoldOut(restock.ActivityID, restock.ProductID)
	}
}

// ==================== 防线 3: Lua 原子预扣减（含 Pending Hash） ====================

// seckillLuaScript 秒杀预扣减 Lua 脚本（Phase 5 增强版：含 Pending Log）
//
// 相比 Phase 3 版本新增 Pending Hash 写入，为延迟对账兜底提供数据支撑。
//
// KEYS:
//
//	KEYS[1] = seckill:stock:{activityID}:{productID}     -- 库存 Key (STRING)
//	KEYS[2] = seckill:purchased:{activityID}:{productID}  -- 已购集合 Key (SET)
//	KEYS[3] = seckill:pending:{activityID}                 -- 悬空预扣 Hash (HASH)
//
// ARGV:
//
//	ARGV[1] = userID    -- 用户 ID（字符串）
//	ARGV[2] = orderNo   -- 雪花算法订单号（字符串）
//	ARGV[3] = timestamp -- 当前时间戳（秒级，用于延迟检查判断悬挂时长）
//
// 返回码:
//
//	-2: 库存 Key 不存在（活动未预热或已结束）
//	-1: userID 已在已购集合中（重复购买）
//	 0: 库存 <= 0（已售罄）
//	 1: 扣减成功（库存 -1 + SADD 用户 + HSET pending）
//
// 原子性保证：
//
//	5 个 Redis 命令在同一个 Lua 脚本中执行，Redis 单线程保证没有并发窗口：
//	  DECR stock → SADD purchased → HSET pending
//	  如果中间出现任何异常，整个脚本回滚（Redis Lua 事务语义）。
var seckillLuaScript = redis.NewScript(`
-- 秒杀预扣减 Lua 脚本（含 Pending Hash 写入）
-- KEYS[1]: 库存 Key
-- KEYS[2]: 已购集合 Key
-- KEYS[3]: 悬空预扣 Pending Hash Key
-- ARGV[1]: userID
-- ARGV[2]: orderNo
-- ARGV[3]: pendingValue (JSON: {"ts":xxx,"uid":xxx,"pid":xxx})

-- Step 1: 检查库存 Key 是否存在（活动是否已预热）
if redis.call('EXISTS', KEYS[1]) == 0 then
    return -2
end

-- Step 2: 检查是否重复购买（一人一单）
if redis.call('SISMEMBER', KEYS[2], ARGV[1]) == 1 then
    return -1
end

-- Step 3: 检查库存是否充足
local stock = tonumber(redis.call('GET', KEYS[1]))
if stock <= 0 then
    return 0
end

-- Step 4: 原子扣减库存 + 记录已购用户 + 写入 Pending Hash
redis.call('DECR', KEYS[1])
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('HSET', KEYS[3], ARGV[2], ARGV[3])

return 1
`)

// PreDeductStock 执行 Lua 脚本进行秒杀库存预扣减（含 Pending Hash 写入）
//
// 相比 Phase 3 版本，新增 3 个参数：
//   - orderNo:   雪花算法生成的订单号（写入 Pending Hash 的 field）
//   - timestamp: 当前时间戳秒级（写入 Pending Hash 的 value，用于延迟检查）
//
// 返回值（哨兵错误）：
//   - nil:                          扣减成功 + Pending 已写入
//   - e.ErrSeckillActivityNotStart: 活动未预热
//   - e.ErrSeckillRepeatBuy:        重复购买
//   - e.ErrSeckillSoldOut:          已售罄
//   - 其他 error:                   Redis 系统级错误
func (sc *SeckillCache) PreDeductStock(
	ctx context.Context,
	activityID, productID, userID int64,
	orderNo string,
	timestamp int64,
) error {
	stockKey := e.BuildStockKey(activityID, productID)
	purchasedKey := e.BuildPurchasedKey(activityID, productID)
	pendingKey := e.BuildPendingKey(activityID)
	userIDStr := strconv.FormatInt(userID, 10)
	// Pending Hash value 改为 JSON 格式，携带 userID 和 productID，
	// 使 Cron 检查器能执行完整的补偿回滚（INCR 库存 + SREM 用户）
	pendingValue := fmt.Sprintf(`{"ts":%d,"tsm":%d,"uid":%d,"pid":%d}`, timestamp, time.Now().UnixMilli(), userID, productID)

	// 执行 Lua 脚本
	result, err := seckillLuaScript.Run(ctx, sc.rdb,
		[]string{stockKey, purchasedKey, pendingKey}, // KEYS[1..3]
		userIDStr, orderNo, pendingValue,              // ARGV[1..3]
	).Int()

	if err != nil {
		logger.Error("Lua 脚本执行失败",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Int64("userID", userID),
			zap.String("orderNo", orderNo),
			zap.Error(err),
		)
		return fmt.Errorf("redis lua script error: %w", err)
	}

	// 根据 Lua 返回码映射为哨兵错误
	switch result {
	case 1:
		// 扣减成功 + Pending 已写入
		logger.Info("Redis 预扣减成功（含 Pending 写入）",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Int64("userID", userID),
			zap.String("orderNo", orderNo),
		)
		return nil

	case 0:
		// 已售罄 —— 写入 TTL 售罄标记
		sc.markLocalSoldOut(activityID, productID)
		return e.ErrSeckillSoldOut

	case -1:
		// 重复购买
		logger.Warn("Redis 一人一单拦截",
			zap.Int64("activityID", activityID),
			zap.Int64("userID", userID),
		)
		return e.ErrSeckillRepeatBuy

	case -2:
		// 活动未预热
		logger.Warn("Redis 库存 Key 不存在，活动未预热",
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
		)
		return e.ErrSeckillActivityNotStart

	default:
		return fmt.Errorf("lua script 返回未知状态码: %d", result)
	}
}

// ==================== Pending Hash 操作方法 ====================

// DeletePending 从 Pending Hash 中删除指定 orderNo
//
// 调用时机：MQ 消费端（链路 1）建单事务成功后调用。
// 删除成功说明该预扣记录已正常落库，不再需要延迟检查兜底。
func (sc *SeckillCache) DeletePending(ctx context.Context, activityID int64, orderNo string) error {
	pendingKey := e.BuildPendingKey(activityID)
	return sc.rdb.HDel(ctx, pendingKey, orderNo).Err()
}

// GetPending 从 Pending Hash 中查询指定 orderNo 是否存在
//
// 调用时机：Cron 定时扫描时调用。
// 返回 timestamp 字符串和是否存在。
func (sc *SeckillCache) GetPending(ctx context.Context, activityID int64, orderNo string) (string, bool, error) {
	pendingKey := e.BuildPendingKey(activityID)
	val, err := sc.rdb.HGet(ctx, pendingKey, orderNo).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

// HasPending 检查指定 orderNo 是否仍处于 pending 中间态
func (sc *SeckillCache) HasPending(ctx context.Context, activityID int64, orderNo string) (bool, error) {
	pendingKey := e.BuildPendingKey(activityID)
	return sc.rdb.HExists(ctx, pendingKey, orderNo).Result()
}

// ==================== 补偿回滚 Lua 脚本 ====================

// ScanPendingKeys 扫描所有 seckill:pending:* 的 Key
//
// 调用时机：Cron 定时扫描悬空预扣时，先获取所有存在的 pending Hash Key。
// 返回 Key 列表和对应的 activityID 列表。
func (sc *SeckillCache) ScanPendingKeys(ctx context.Context) ([]string, error) {
	var keys []string
	iter := sc.rdb.Scan(ctx, 0, e.KeySeckillPendingPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("SCAN pending keys error: %w", err)
	}
	return keys, nil
}

// GetPendingAll 获取指定 pending Hash 的全部条目
//
// 返回 map[orderNo]timestamp（字符串）。
func (sc *SeckillCache) GetPendingAll(ctx context.Context, pendingKey string) (map[string]string, error) {
	result, err := sc.rdb.HGetAll(ctx, pendingKey).Result()
	if err != nil {
		return nil, fmt.Errorf("HGETALL %s error: %w", pendingKey, err)
	}
	return result, nil
}

// RecordAsyncOrderAccepted 记录异步接单指标
func (sc *SeckillCache) RecordAsyncOrderAccepted(ctx context.Context, activityID, productID int64) {
	key := e.BuildBenchmarkSummaryKey(activityID, productID)
	nowMillis := time.Now().UnixMilli()
	pipe := sc.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, "accepted_total", 1)
	pipe.HSet(ctx, key, "last_accepted_at_ms", nowMillis)
	_, _ = pipe.Exec(ctx)
}

// RecordAsyncOrderCreated 记录异步建单完成指标
func (sc *SeckillCache) RecordAsyncOrderCreated(ctx context.Context, activityID, productID int64, acceptedAtMillis int64) {
	key := e.BuildBenchmarkSummaryKey(activityID, productID)
	nowMillis := time.Now().UnixMilli()
	pipe := sc.rdb.Pipeline()
	pipe.HIncrBy(ctx, key, "created_total", 1)
	if acceptedAtMillis > 0 && nowMillis >= acceptedAtMillis {
		pipe.HIncrBy(ctx, key, "create_latency_total_ms", nowMillis-acceptedAtMillis)
	}
	pipe.HSet(ctx, key, "last_created_at_ms", nowMillis)
	_, _ = pipe.Exec(ctx)
}

// GetBenchmarkSummary 读取压测汇总指标
func (sc *SeckillCache) GetBenchmarkSummary(ctx context.Context, activityID, productID int64) (map[string]string, error) {
	return sc.rdb.HGetAll(ctx, e.BuildBenchmarkSummaryKey(activityID, productID)).Result()
}

// ==================== 订单作废与处理中标记 ====================

// markCanceledIfPendingLua 原子写入作废标记脚本
//
// KEYS:
//   KEYS[1] = pending hash key (seckill:pending:{activityID})
//   KEYS[2] = canceled key (seckill:canceled:{orderNo})
//   KEYS[3] = processing key (seckill:processing:{orderNo})
// ARGV:
//   ARGV[1] = orderNo
//   ARGV[2] = canceled ttl seconds
//
// 返回值:
//   1  = 创建作废标记成功
//   0  = 作废标记已存在
//  -1  = pending 不存在
//  -2  = 订单正在处理中
var markCanceledIfPendingLua = redis.NewScript(`
if redis.call('HEXISTS', KEYS[1], ARGV[1]) == 0 then
    return -1
end

if redis.call('EXISTS', KEYS[3]) == 1 then
    return -2
end

if redis.call('SET', KEYS[2], '1', 'NX', 'EX', ARGV[2]) then
    return 1
end

return 0
`)

// tryMarkProcessingLua 订单处理抢占脚本
//
// KEYS:
//   KEYS[1] = processing key
//   KEYS[2] = canceled key
// ARGV:
//   ARGV[1] = processing ttl seconds
//
// 返回值:
//   1  = 抢占处理成功
//   0  = 已作废
//  -1  = 已被其他消费者处理
var tryMarkProcessingLua = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
    return 0
end

if redis.call('SET', KEYS[1], '1', 'NX', 'EX', ARGV[1]) then
    return 1
end

return -1
`)

// TryMarkOrderCanceledIfPending 尝试为订单写入作废标记（原子）
func (sc *SeckillCache) TryMarkOrderCanceledIfPending(
	ctx context.Context,
	activityID int64,
	orderNo string,
	ttl time.Duration,
) (int, error) {
	if ttl <= 0 {
		ttl = canceledMarkerTTL
	}

	pendingKey := e.BuildPendingKey(activityID)
	canceledKey := e.BuildCanceledOrderKey(orderNo)
	processingKey := e.BuildProcessingOrderKey(orderNo)
	ttlSec := strconv.FormatInt(int64(ttl/time.Second), 10)

	ret, err := markCanceledIfPendingLua.Run(ctx, sc.rdb,
		[]string{pendingKey, canceledKey, processingKey},
		orderNo, ttlSec,
	).Int()
	if err != nil {
		return 0, fmt.Errorf("mark canceled lua error: %w", err)
	}

	return ret, nil
}

// IsOrderCanceled 查询订单是否已作废
func (sc *SeckillCache) IsOrderCanceled(ctx context.Context, orderNo string) (bool, error) {
	key := e.BuildCanceledOrderKey(orderNo)
	exists, err := sc.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// TryMarkOrderProcessing 尝试抢占订单处理标记
func (sc *SeckillCache) TryMarkOrderProcessing(ctx context.Context, orderNo string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	processingKey := e.BuildProcessingOrderKey(orderNo)
	canceledKey := e.BuildCanceledOrderKey(orderNo)
	ttlSec := strconv.FormatInt(int64(ttl/time.Second), 10)

	ret, err := tryMarkProcessingLua.Run(ctx, sc.rdb,
		[]string{processingKey, canceledKey},
		ttlSec,
	).Int()
	if err != nil {
		return false, fmt.Errorf("mark processing lua error: %w", err)
	}

	return ret == 1, nil
}

// ClearOrderProcessing 清除订单处理中标记
func (sc *SeckillCache) ClearOrderProcessing(ctx context.Context, orderNo string) error {
	key := e.BuildProcessingOrderKey(orderNo)
	return sc.rdb.Del(ctx, key).Err()
}

// ==================== 原补偿回滚 Lua 脚本 ====================

// rollbackLuaScript 悬空预扣补偿回滚 Lua 脚本
//
// 当延迟检查发现 Redis 预扣了但 DB 未建单时，执行补偿回滚：
//   1. INCR 库存（恢复预扣的 1 个库存）
//   2. SREM 用户购买标记（允许用户重新参与秒杀）
//   3. HDEL Pending 记录（清除悬空标记）
//
// KEYS:
//   KEYS[1] = seckill:stock:{activityID}:{productID}
//   KEYS[2] = seckill:purchased:{activityID}:{productID}
//   KEYS[3] = seckill:pending:{activityID}
//
// ARGV:
//   ARGV[1] = userID
//   ARGV[2] = orderNo
//
// 返回值: 1 (成功)
//
// 幂等性：多次执行不会产生副作用（INCR 多次会多加库存，
// 但该场景不会发生——延迟消息只投递一次，即使重复消费也先查 DB/pending）。
var rollbackLuaScript = redis.NewScript(`
-- 悬空预扣补偿回滚 Lua 脚本
-- KEYS[1]: 库存 Key
-- KEYS[2]: 已购集合 Key
-- KEYS[3]: Pending Hash Key
-- ARGV[1]: userID
-- ARGV[2]: orderNo

redis.call('INCR', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[1])
redis.call('HDEL', KEYS[3], ARGV[2])

return 1
`)

// RollbackPreDeduct 执行补偿回滚 Lua 脚本
//
// 调用场景：延迟检查消费者（链路 2）发现悬空预扣记录后调用。
//
// 原子性：3 个操作在同一个 Lua 脚本中执行，保证不会出现
// "库存回滚了但用户购买标记没清"的中间状态。
func (sc *SeckillCache) RollbackPreDeduct(
	ctx context.Context,
	activityID, productID, userID int64,
	orderNo string,
) error {
	stockKey := e.BuildStockKey(activityID, productID)
	purchasedKey := e.BuildPurchasedKey(activityID, productID)
	pendingKey := e.BuildPendingKey(activityID)
	userIDStr := strconv.FormatInt(userID, 10)

	_, err := rollbackLuaScript.Run(ctx, sc.rdb,
		[]string{stockKey, purchasedKey, pendingKey},
		userIDStr, orderNo,
	).Int()

	if err != nil {
		return fmt.Errorf("rollback lua script error: %w", err)
	}

	logger.Warn("补偿回滚 Lua 执行成功",
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
		zap.Int64("userID", userID),
		zap.String("orderNo", orderNo),
	)
	return nil
}

// ==================== 轮询辅助方法 ====================

// IsUserInPurchasedSet 查询用户是否在 Redis 已购集合中
//
// SISMEMBER 时间复杂度 O(1)，性能极好。
func (sc *SeckillCache) IsUserInPurchasedSet(ctx context.Context, activityID, productID, userID int64) (bool, error) {
	key := e.BuildPurchasedKey(activityID, productID)
	return sc.rdb.SIsMember(ctx, key, strconv.FormatInt(userID, 10)).Result()
}

// ==================== 辅助方法（运营预热） ====================

// WarmUpActivity 活动预热：将库存和 BloomFilter 数据写入 Redis
func (sc *SeckillCache) WarmUpActivity(ctx context.Context, activityID, productID, stock int64) error {
	stockKey := e.BuildStockKey(activityID, productID)
	bloomMember := e.BuildBloomMember(activityID, productID)

	// 设置库存
	if err := sc.rdb.Set(ctx, stockKey, stock, 0).Err(); err != nil {
		return fmt.Errorf("设置库存 Key 失败: %w", err)
	}

	// 写入 BloomFilter（降级处理）
	if err := sc.rdb.Do(ctx, "BF.ADD", e.KeySeckillBloomItems, bloomMember).Err(); err != nil {
		sc.disableBloomOnUnsupported(err)
		logger.Warn("BloomFilter BF.ADD 失败（RedisBloom 模块可能未加载），跳过",
			zap.Error(err),
		)
	}

	// 清除本地售罄标记
	sc.clearLocalSoldOut(activityID, productID)

	logger.Info("活动预热完成",
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
		zap.Int64("stock", stock),
	)

	return nil
}

// ==================== 订单超时取消 Redis 回滚 ====================

// cancelOrderRollbackLua 订单超时取消回滚 Lua 脚本
//
// 与 rollbackLuaScript 的区别：
//   - rollback 针对「悬空预扣」，需要 HDEL pending
//   - 此脚本针对「订单超时取消」，pending 早已被链路 1 清除，仅需回滚库存+已购
//
// KEYS:
//
//	KEYS[1] = seckill:stock:{activityID}:{productID}     -- 库存 Key
//	KEYS[2] = seckill:purchased:{activityID}:{productID}  -- 已购集合 Key
//
// ARGV:
//
//	ARGV[1] = userID
//
// 返回值: 1 (成功)
var cancelOrderRollbackLua = redis.NewScript(`
-- 订单超时取消回滚 Lua 脚本
-- KEYS[1]: 库存 Key
-- KEYS[2]: 已购集合 Key
-- ARGV[1]: userID

redis.call('INCR', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[1])

return 1
`)

// CancelOrderRollback 订单超时取消时回滚 Redis 库存和已购标记
//
// 调用场景：链路 3 订单超时消费者取消订单后调用。
// 原子性：2 个操作在同一 Lua 脚本中执行，保证不会出现
// "库存回滚了但用户购买标记没清"的中间状态。
func (sc *SeckillCache) CancelOrderRollback(
	ctx context.Context,
	activityID, productID, userID int64,
) error {
	stockKey := e.BuildStockKey(activityID, productID)
	purchasedKey := e.BuildPurchasedKey(activityID, productID)
	userIDStr := strconv.FormatInt(userID, 10)

	_, err := cancelOrderRollbackLua.Run(ctx, sc.rdb,
		[]string{stockKey, purchasedKey},
		userIDStr,
	).Int()

	if err != nil {
		return fmt.Errorf("cancel order rollback lua error: %w", err)
	}

	logger.Warn("订单超时取消 Redis 回滚完成",
		zap.Int64("activityID", activityID),
		zap.Int64("productID", productID),
		zap.Int64("userID", userID),
	)
	return nil
}
