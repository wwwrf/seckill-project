package e

import "errors"

// ==================== 秒杀业务哨兵错误 ====================
//
// 哨兵错误（Sentinel Error）是 Go 中通过全局变量定义的、可用 errors.Is 精确匹配的错误值。
// 它们是整个秒杀链路中各层（Cache → Service → Handler）进行错误分类和流转的统一语言。
//
// 使用规范：
//   - Cache 层：Lua 脚本返回码 → 映射为对应哨兵错误
//   - Service 层：errors.Is(err, e.ErrSeckillSoldOut) 做分支判断
//   - Handler 层：根据哨兵错误类型返回不同的 HTTP 响应码和提示语

var (
	// ErrSeckillSoldOut 库存已售罄
	//
	// 触发场景：
	//   1. Redis Lua 脚本检测到 stock <= 0（返回码 0）
	//   2. 本地 soldOutMap 命中售罄标记（快速短路，不打 Redis）
	//
	// 处理策略：
	//   - 立即写入本地售罄标记，后续请求在进程内直接拦截
	//   - 返回 HTTP 200 + 业务码告知「已售罄」
	ErrSeckillSoldOut = errors.New("seckill: stock zero")

	// ErrSeckillRepeatBuy 重复购买（一人一单拦截）
	//
	// 触发场景：
	//   1. Redis Lua 脚本 SISMEMBER 检测到 userID 已在已购集合中（返回码 -1）
	//   2. MySQL 层 uk_user_activity 唯一键冲突（DB 层兜底）
	//
	// 处理策略：
	//   - Redis 层拦截：直接返回，不进 MQ
	//   - DB 层拦截：MQ 消费端收到 DuplicateKey 错误后 ACK（幂等）
	ErrSeckillRepeatBuy = errors.New("seckill: duplicate purchase")

	// ErrSeckillActivityNotStart 活动未预热或已结束
	//
	// 触发场景：
	//   1. Redis Lua 脚本检测到库存 Key 不存在（返回码 -2）
	//      这说明运营尚未将活动库存预热到 Redis，或活动已过期被清理
	//   2. BloomFilter 检测到 activityID_productID 不存在
	//
	// 处理策略：
	//   - 直接拒绝请求，不打 DB，防止缓存穿透
	ErrSeckillActivityNotStart = errors.New("seckill: activity not started")
)
