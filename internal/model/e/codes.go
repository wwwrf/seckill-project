package e

// ==================== 业务响应码定义 ====================
//
// HTTP 状态码 vs 业务 Code 的设计约定：
//
//	所有业务逻辑错误均返回 HTTP 200，通过 Response.Code 区分具体错误类型。
//	这是国内主流互联网公司的 API 设计惯例，前端统一通过 code 字段判断：
//	  code == 0      → 操作成功
//	  code == 4xxxx  → 客户端/业务可预期错误
//	  code == 5xxxx  → 服务端/系统级错误
//
// 仅在真正的协议层错误时使用非 200 HTTP 状态码：
//
//	400 → 请求参数解析失败（JSON 格式错误等）
//	401 → 未认证（Token 缺失或过期）
//	403 → 无权限
//	429 → 触发限流（返回 Retry-After Header）
//	500 → 未捕获的服务端 Panic

const (
	// CodeSuccess 操作成功
	CodeSuccess = 0

	// -------------------- 通用业务错误 (400xx) --------------------

	// CodeBadRequest 请求参数错误
	// 触发场景：请求体 JSON 缺少必填字段，或字段值不合法
	CodeBadRequest = 40000

	// CodeUnauthorized 未认证
	// 触发场景：Authorization Header 缺失、Token 过期、签名校验失败
	CodeUnauthorized = 40100

	// -------------------- 用户模块错误 (400xx) --------------------

	// CodeUsernameTaken 用户名已被注册
	CodeUsernameTaken = 40004

	// CodeInvalidCredentials 用户名或密码错误
	CodeInvalidCredentials = 40005

	// -------------------- 秒杀业务错误 (400xx) --------------------

	// CodeSeckillSoldOut 秒杀商品已售罄
	// 前端收到此 code 应立即展示"已售罄"并禁用秒杀按钮，停止后续轮询
	CodeSeckillSoldOut = 40001

	// CodeSeckillRepeatBuy 重复购买（一人一单限制）
	// 前端收到此 code 应提示"您已参与过该活动"并跳转订单列表
	CodeSeckillRepeatBuy = 40002

	// CodeSeckillNotStart 秒杀活动未开始或已结束
	// 前端收到此 code 应展示活动状态提示（如倒计时 / 已结束）
	CodeSeckillNotStart = 40003

	// -------------------- 秒杀体验类状态码 (200xx) --------------------

	// CodeSeckillQueuing 用户已在排队中（非错误，是正常业务状态）
	//
	// 触发场景：
	//   1. Redis Lua 返回 -1（重复购买，用户已在 purchased 集合中）
	//   2. 进程级连击拦截（go-cache 3s TTL 内重复请求）
	//
	// 前端收到此 code 应进入订单结果轮询模式：
	//   GET /api/v1/seckill/result 间隔 1~2s 轮询，直到 SUCCESS 或 FAILED
	CodeSeckillQueuing = 20002

	// -------------------- 限流类状态码 (429xx) --------------------

	// CodeRateLimited 用户级限流拦截
	// 触发场景：同一用户请求速率超过令牌桶阈值（默认 2 req/s, burst 3）
	// 前端收到此 code 应禁用按钮 1~2 秒并提示"操作过于频繁"
	CodeRateLimited = 42900

	// -------------------- 服务端错误 (500xx) --------------------

	// CodeSystemBusy 系统繁忙
	// 触发场景：MQ 发送失败、Redis 不可用等内部错误
	// 前端收到此 code 应提示"系统繁忙，请稍后查看订单列表"
	CodeSystemBusy = 50001
)
