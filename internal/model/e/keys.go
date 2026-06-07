package e

// ==================== Redis Key 常量定义 ====================
//
// 统一管理所有 Redis Key 的前缀和格式，严禁在业务代码中硬编码字符串拼接。
//
// 命名规范：
//   - 使用冒号 : 作为层级分隔符（Redis 约定俗成）
//   - 格式：业务域:功能:子维度:{动态参数}
//   - 所有 Key 都要在此文件中注册，便于全局检索和 TTL 审计

import "fmt"

// -------------------- 秒杀业务 Redis Key --------------------

const (
	// KeySeckillStockPrefix 秒杀库存 Key 前缀
	// 完整 Key: seckill:stock:{activityID}:{productID}
	// 值类型: STRING (int)
	// 用途: Lua 脚本原子扣减的核心 Key，由运营预热写入
	KeySeckillStockPrefix = "seckill:stock:"

	// KeySeckillPurchasedPrefix 秒杀已购集合 Key 前缀
	// 完整 Key: seckill:purchased:{activityID}:{productID}
	// 值类型: SET (成员为 userID 的字符串表示)
	// 用途: Lua 脚本 SISMEMBER 判重，实现 Redis 层一人一单拦截
	KeySeckillPurchasedPrefix = "seckill:purchased:"

	// KeySeckillPendingPrefix 秒杀悬空预扣 Pending Hash Key 前缀
	// 完整 Key: seckill:pending:{activityID}
	// 值类型: HASH (field=orderNo, value=timestamp)
	//
	// 生命周期：
	//   写入: Lua 预扣脚本成功时 HSET（与库存扣减、SADD 原子执行）
	//   删除: MQ 消费端建单成功后 HDEL
	//   兜底: 5 分钟延迟检查消费者发现仍存在 → 触发补偿回滚
	//
	// 该 Key 是整个「悬空预扣补偿」机制的核心数据结构：
	//   如果 orderNo 在 pending Hash 中存在，说明 Redis 预扣已完成但 DB 订单未建立。
	//   正常情况下 MQ 消费端会在数百毫秒内建单并 HDEL；
	//   异常情况下（MQ 丢消息、消费者崩溃），5 分钟延迟检查将触发回滚。
	KeySeckillPendingPrefix = "seckill:pending:"

	// KeySeckillCanceledPrefix 已作废订单标记 Key 前缀
	// 完整 Key: seckill:canceled:{orderNo}
	// 值类型: STRING("1")
	// 用途: Cron 判定悬空回滚前先打标，MQ 建单前检查命中则丢弃，避免迟到消息导致超卖
	KeySeckillCanceledPrefix = "seckill:canceled:"

	// KeySeckillProcessingPrefix 订单处理中标记 Key 前缀
	// 完整 Key: seckill:processing:{orderNo}
	// 值类型: STRING("1")
	// 用途: MQ 消费者建单前抢占处理锁，Cron 回滚前若发现处理中则跳过
	KeySeckillProcessingPrefix = "seckill:processing:"

	// KeySeckillBloomItems BloomFilter Key
	// 值类型: RedisBloom BF (需要 RedisBloom 模块)
	// 成员格式: "{activityID}_{productID}"
	// 用途: 防缓存穿透，拦截不存在的 activityID/productID 组合
	KeySeckillBloomItems = "seckill:bloom:items"

	// TopicSeckillRestock 补货 Pub/Sub 频道名
	// 消息格式: JSON {"activityId": 1, "productId": 100}
	// 用途: 运营补货后发布消息，各实例订阅后清除本地售罄标记
	TopicSeckillRestock = "seckill:pubsub:restock"

	// KeyBenchmarkSummaryPrefix 压测汇总指标 Key 前缀
	// 完整 Key: benchmark:summary:{activityID}:{productID}
	// 值类型: HASH
	KeyBenchmarkSummaryPrefix = "benchmark:summary:"
)

// -------------------- RocketMQ Topic --------------------

const (
	// TopicSeckillTx 秒杀主流程落库 Topic
	//
	// 消息格式: JSON SeckillOrderMessage
	//   {"order_no": "xxx", "user_id": 123, "activity_id": 456, "product_id": 789}
	//
	// 生产端: SeckillService.DoSeckill() 先写主消息 outbox，再由调度器同步发送
	// 消费端: SeckillConsumer 链路 1 — 开启 DB 事务建单 → HDEL pending
	TopicSeckillTx = "seckill_tx_topic"
)

// -------------------- 订单超时取消 Topic --------------------

const (
	// TopicSeckillOrderTimeout 订单支付超时取消 Topic
	//
	// 消息格式: 与 TopicSeckillTx 完全相同的 JSON SeckillOrderMessage
	// 延迟级别: RocketMQ DelayLevel=16（30 分钟）
	//
	// 生产端: MQ 消费者链路 1 建单成功后立即发送延迟消息
	// 消费端: SeckillConsumer 链路 3 — 30 分钟后检查订单状态
	//   已支付 → ACK
	//   未支付 → 取消订单 + 回滚 DB 库存 + 回滚 Redis 库存/已购
	TopicSeckillOrderTimeout = "seckill_order_timeout_topic"
)

// -------------------- MQ 消费者组 --------------------

const (
	// GroupSeckillTxConsumer 主流程落库消费者组
	GroupSeckillTxConsumer = "seckill_tx_consumer_group"

	// GroupSeckillOrderTimeoutConsumer 订单超时取消消费者组
	GroupSeckillOrderTimeoutConsumer = "seckill_order_timeout_consumer_group"
)

// -------------------- Key 构建辅助函数 --------------------

// BuildStockKey 构建秒杀库存 Key
//
//	seckill:stock:{activityID}:{productID}
func BuildStockKey(activityID, productID int64) string {
	return fmt.Sprintf("%s%d:%d", KeySeckillStockPrefix, activityID, productID)
}

// BuildPurchasedKey 构建秒杀已购集合 Key
//
//	seckill:purchased:{activityID}:{productID}
func BuildPurchasedKey(activityID, productID int64) string {
	return fmt.Sprintf("%s%d:%d", KeySeckillPurchasedPrefix, activityID, productID)
}

// BuildPendingKey 构建秒杀悬空预扣 Pending Hash Key
//
//	seckill:pending:{activityID}
func BuildPendingKey(activityID int64) string {
	return fmt.Sprintf("%s%d", KeySeckillPendingPrefix, activityID)
}

// BuildCanceledOrderKey 构建已作废订单标记 Key
//
//	seckill:canceled:{orderNo}
func BuildCanceledOrderKey(orderNo string) string {
	return KeySeckillCanceledPrefix + orderNo
}

// BuildProcessingOrderKey 构建订单处理中标记 Key
//
//	seckill:processing:{orderNo}
func BuildProcessingOrderKey(orderNo string) string {
	return KeySeckillProcessingPrefix + orderNo
}

// BuildBloomMember 构建 BloomFilter 成员值
//
//	{activityID}_{productID}
func BuildBloomMember(activityID, productID int64) string {
	return fmt.Sprintf("%d_%d", activityID, productID)
}

// BuildSoldOutMapKey 构建本地售罄标记的 Map Key
//
//	{activityID}_{productID}
func BuildSoldOutMapKey(activityID, productID int64) string {
	return fmt.Sprintf("%d_%d", activityID, productID)
}

// BuildBenchmarkSummaryKey 构建压测汇总指标 Key
//
//	benchmark:summary:{activityID}:{productID}
func BuildBenchmarkSummaryKey(activityID, productID int64) string {
	return fmt.Sprintf("%s%d:%d", KeyBenchmarkSummaryPrefix, activityID, productID)
}
