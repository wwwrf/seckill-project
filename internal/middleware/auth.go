package middleware

import (
	"strconv"
	"strings"

	"seckill-system/pkg/auth"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ContextKeyUserID JWT 解析后在 gin.Context 中存储 userID 的 Key
//
// 使用规范：
//
//	写入（JWTAuth 中间件）：c.Set(ContextKeyUserID, userID)
//	读取（Handler 层）：    val, _ := c.Get(ContextKeyUserID); uid := val.(int64)
//
// 安全约定：该值只能由服务端 JWT 中间件写入，前端传入的任何 user_id 一律忽略。
const ContextKeyUserID = "userID"

// JWTAuth JWT 鉴权中间件
//
// 核心职责：
//  1. 从 Authorization 请求头提取 Bearer Token
//  2. 调用 auth.ParseToken 验证签名和有效期
//  3. 从 Token Claims 中提取 userID + username
//  4. 将 userID 注入 gin.Context，供后续 Handler 使用
//
// 降级策略（仅 Debug 模式）：
//
//	当 Authorization Header 缺失时，尝试读取 X-User-ID 请求头。
//	仅在 gin.DebugMode 下生效，生产环境（release 模式）严格要求 JWT。
//	方便开发阶段使用 curl / Postman / 集成测试快速调试。
//
// 技术选型说明：
//
//	| 方案               | 优点                      | 缺点                          |
//	|--------------------|---------------------------|-------------------------------|
//	| JWT(选用)          | 无状态，水平扩展友好       | Token 撤销需额外机制（黑名单）  |
//	| Session + Cookie   | 服务端可主动踢人           | 有状态，需 Redis 共享 Session   |
//	| OAuth 2.0          | 标准协议，适合第三方授权   | 复杂度高，秒杀系统不需要       |
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// ========== 优先尝试 JWT Bearer Token ==========
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := auth.ParseToken(tokenStr)
			if err != nil {
				logger.Warn("JWT 验证失败",
					zap.String("path", c.Request.URL.Path),
					zap.String("clientIP", c.ClientIP()),
					zap.Error(err),
				)
				response.Unauthorized(c, "Token 无效或已过期")
				c.Abort()
				return
			}

			// Token 有效 → 注入 UserID
			c.Set(ContextKeyUserID, claims.UserID)
			c.Next()
			return
		}

		// ========== Debug 模式降级：X-User-ID ==========
		if gin.Mode() == gin.DebugMode {
			userIDStr := c.GetHeader("X-User-ID")
			if userIDStr != "" {
				userID, err := strconv.ParseInt(userIDStr, 10, 64)
				if err == nil && userID > 0 {
					c.Set(ContextKeyUserID, userID)
					c.Next()
					return
				}
			}
		}

		// ========== 无有效凭证 ==========
		logger.Warn("请求缺少认证信息",
			zap.String("path", c.Request.URL.Path),
			zap.String("method", c.Request.Method),
			zap.String("clientIP", c.ClientIP()),
		)
		response.Unauthorized(c, "请先登录")
		c.Abort()
	}
}
