package middleware

import (
	"fmt"
	"net/http"
	"time"

	"seckill-system/pkg/logger"

	"github.com/gin-gonic/gin"
	gocache "github.com/patrickmn/go-cache"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// ==================== 中间件包 ====================
//
// 本包存放所有 HTTP 中间件，包括：
//   - TraceMiddleware: 链路追踪（见 trace.go）
//   - JWTAuth:         JWT 鉴权（见 auth.go）
//   - RateLimiter:     用户级令牌桶限流（见本文件）

// ==================== 用户级令牌桶限流器 ====================
//
// 设计要点：
//
//  1. 基于 UserID 限流（非 IP 限流）
//     IP 限流的缺陷：NAT / 代理 / CDN 场景下，大量真实用户共享同一 IP，
//     导致正常用户被误伤。DDoS 攻击者也可轻松切换 IP 绕过限制。
//     UserID 限流可精准控制每个用户的请求速率，不受网络拓扑影响。
//
//  2. 令牌桶算法（Token Bucket）
//     使用 golang.org/x/time/rate 实现，每秒恢复 rate 个令牌，
//     突发上限为 burst 个。适合秒杀场景的短时突发 + 平稳限速需求。
//
//  3. 内存管理：go-cache TTL 自动回收
//     每个用户的 Limiter 对象存储在 go-cache 中，默认 60s 过期。
//     长时间不活跃的用户 Limiter 会被自动清理，避免内存泄漏。
//     秒杀高峰期百万级 Limiter 内存开销约 50~80MB，完全可控。
//
//  4. 中间件执行顺序前置要求：
//     本中间件必须在 JWTAuth 之后执行，因为需要从 gin.Context
//     中读取 JWTAuth 注入的 UserID。

// userLimiters 全局用户限流器缓存
//
// Key: "rl:{userID}"
// Value: *rate.Limiter
// TTL: 60s（用户 60 秒无请求后自动回收，防止内存泄漏）
// Cleanup: 每 120s 扫描一次过期条目
var userLimiters = gocache.New(60*time.Second, 120*time.Second)

// ipLimiters IP 级限流器缓存（Key: "ip:{clientIP}"，TTL: 60s）
var ipLimiters = gocache.New(60*time.Second, 120*time.Second)

// getLimiterFromCache 从指定缓存中获取或创建令牌桶限流器
//
// 复用逻辑：用户级（getUserLimiter）和 IP 级（getIPLimiter）完全相同，
// 抽取为公共函数消除重复。
func getLimiterFromCache(c *gocache.Cache, key string, rateLimit float64, burst int) *rate.Limiter {
	if val, found := c.Get(key); found {
		return val.(*rate.Limiter)
	}
	limiter := rate.NewLimiter(rate.Limit(rateLimit), burst)
	// Add 仅在 Key 不存在时写入，保证并发安全
	_ = c.Add(key, limiter, gocache.DefaultExpiration)
	if val, found := c.Get(key); found {
		return val.(*rate.Limiter)
	}
	return limiter
}

// getUserLimiter 获取（或创建）指定用户的令牌桶限流器
func getUserLimiter(userID int64, rateLimit float64, burst int) *rate.Limiter {
	return getLimiterFromCache(userLimiters, fmt.Sprintf("rl:%d", userID), rateLimit, burst)
}

// getIPLimiter 获取指定 IP 的令牌桶限流器
func getIPLimiter(ip string, rateLimit float64, burst int) *rate.Limiter {
	return getLimiterFromCache(ipLimiters, fmt.Sprintf("ip:%s", ip), rateLimit, burst)
}

// IPRateLimiter 基于 ClientIP 的前置限流中间件（秒杀风控）
//
// 配置来源（config/local.yaml）：
//   ratelimit.ip.enabled: true/false
//   ratelimit.ip.rps: 10
//   ratelimit.ip.burst: 20
//
// 压测建议：仅在压测环境放宽 rps/burst，不建议关闭用户级限流。
func IPRateLimiter() gin.HandlerFunc {
	enabled := true
	if viper.IsSet("ratelimit.ip.enabled") {
		enabled = viper.GetBool("ratelimit.ip.enabled")
	}

	ratePerSecond := 10.0
	if viper.IsSet("ratelimit.ip.rps") {
		ratePerSecond = viper.GetFloat64("ratelimit.ip.rps")
	}
	if ratePerSecond <= 0 {
		ratePerSecond = 10.0
	}

	burstSize := 20
	if viper.IsSet("ratelimit.ip.burst") {
		burstSize = viper.GetInt("ratelimit.ip.burst")
	}
	if burstSize <= 0 {
		burstSize = 20
	}

	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		ip := c.ClientIP()
		limiter := getIPLimiter(ip, ratePerSecond, burstSize)

		if !limiter.Allow() {
			logger.Warn("IP 级限流拦截",
				zap.String("clientIP", ip),
				zap.String("path", c.Request.URL.Path),
			)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code": 42900,
				"msg":  "请求过于频繁，请稍后再试",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RateLimiter 基于 UserID 的令牌桶限流中间件
//
// 限流策略：
//   - 速率：每秒 ratePerSecond 个令牌（默认 2）
//   - 突发：最多允许 burst 个并发请求（默认 3）
//   - 超限响应：HTTP 429 + JSON {"code": 42900, "msg": "操作过于频繁"}
//
// 配置建议：
//
//	秒杀场景：ratePerSecond=2, burst=3
//	  表示用户正常每秒最多 2 次请求，偶尔可突发到 3 次
//	  恶意脚本的高频请求（>3/s）会被果断拒绝
//
// 前置条件：
//
//	本中间件必须在 JWTAuth 之后注册，否则无法获取 UserID。
//	如果 UserID 不存在（理论上不会到达，被 JWTAuth 拦截），降级放行。
func RateLimiter() gin.HandlerFunc {
	enabled := true
	if viper.IsSet("ratelimit.user.enabled") {
		enabled = viper.GetBool("ratelimit.user.enabled")
	}

	ratePerSecond := 2.0
	if viper.IsSet("ratelimit.user.rps") {
		ratePerSecond = viper.GetFloat64("ratelimit.user.rps")
	}
	if ratePerSecond <= 0 {
		ratePerSecond = 2.0
	}

	burstSize := 3
	if viper.IsSet("ratelimit.user.burst") {
		burstSize = viper.GetInt("ratelimit.user.burst")
	}
	if burstSize <= 0 {
		burstSize = 3
	}

	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		// 从 JWTAuth 注入的 Context 中提取 UserID
		userIDVal, exists := c.Get(ContextKeyUserID)
		if !exists {
			// UserID 不存在说明 JWTAuth 未执行或未注入
			// 理论上不会到达此处（JWTAuth 会先 Abort）
			// 降级放行，避免误杀
			c.Next()
			return
		}

		userID, ok := userIDVal.(int64)
		if !ok {
			c.Next()
			return
		}

		limiter := getUserLimiter(userID, ratePerSecond, burstSize)

		if !limiter.Allow() {
			logger.Warn("用户级限流拦截",
				zap.Int64("userID", userID),
				zap.String("path", c.Request.URL.Path),
				zap.String("clientIP", c.ClientIP()),
			)

			// 返回 HTTP 429 Too Many Requests
			// 业务 code 42900 对齐 HTTP 429 语义
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code": 42900,
				"msg":  "操作过于频繁，请稍后再试",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
