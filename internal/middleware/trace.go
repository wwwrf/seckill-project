package middleware

import (
	"seckill-system/pkg/utils"

	"github.com/gin-gonic/gin"
)

// ContextKeyTraceID 链路追踪 ID 在 gin.Context 中的 Key
//
// 使用规范：
//
//	写入（TraceMiddleware）：c.Set(ContextKeyTraceID, traceID)
//	读取（任意层）：         val, _ := c.Get(ContextKeyTraceID)
//
// 同时会写入响应 Header X-Trace-ID，便于前端 / 运维在日志中定位完整链路。
const ContextKeyTraceID = "traceID"

// TraceMiddleware 链路追踪中间件
//
// 核心职责：
//  1. 优先从请求 Header X-Trace-ID 提取上游透传的 TraceID
//  2. 若上游未传，则使用雪花算法自动生成全局唯一 TraceID
//  3. 将 TraceID 注入 gin.Context（供 Service/Repository 层日志使用）
//  4. 将 TraceID 回写到响应 Header（前端可据此联系运维排查问题）
//
// 中间件执行顺序约定：
//
//	TraceMiddleware（最先） → JWTAuth → RateLimiter → Handler
//
// TraceID 必须在最前面注入，确保后续所有中间件和 Handler 的日志都能关联同一链路。
func TraceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" {
			// 复用雪花算法生成全局唯一 ID，避免引入额外 UUID 依赖
			traceID = utils.GenOrderNo()
		}

		c.Set(ContextKeyTraceID, traceID)
		c.Header("X-Trace-ID", traceID)
		c.Next()
	}
}
