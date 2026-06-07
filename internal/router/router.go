package router

import (
	"seckill-system/api/handler"
	"seckill-system/internal/middleware"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
)

// SetupRouter 设置路由
//
// 路由分层设计（Phase 6 完整版）：
//
//	公开路由（无需认证）：
//	  GET  /ping                    → 健康检查
//	  POST /api/v1/user/register    → 用户注册
//	  POST /api/v1/user/login       → 用户登录
//	  GET  /api/v1/product/:id      → 商品详情查询（三级缓存）
//	  GET  /api/v1/activity/:id     → 秒杀活动查询（三级缓存）
//
//	鉴权路由（Trace → JWT → 限流）：
//	  POST /api/v1/seckill          → 秒杀下单
//	  GET  /api/v1/seckill/result   → 秒杀结果轮询
//	  GET  /api/v1/orders           → 订单列表（分页）
//	  GET  /api/v1/order/:orderNo   → 订单详情
//	  POST /api/v1/order/:orderNo/pay → 订单支付
//
//	管理路由（开发环境，无需鉴权）：
//	  POST /api/v1/admin/warmup     → 活动预热
//	  POST /api/v1/admin/seed       → 创建测试数据
//
// 中间件执行顺序设计理由：
//
//	TraceMiddleware → JWTAuth → RateLimiter → Handler
//
//	  1. Trace 最先：注入 TraceID，确保后续所有中间件和 Handler 的日志都能关联同一链路
//	  2. JWT 其次：验证 Token 并注入 UserID
//	  3. RateLimit 最后：从 Context 中读取 UserID 实现用户级限流
//	     如果先限流再认证，则无法拿到 UserID，只能基于 IP 限流，
//	     导致 NAT 场景下误伤正常用户、DDoS 攻击者切换 IP 绕过。
func SetupRouter(
	seckillHandler *handler.SeckillHandler,
	userHandler *handler.UserHandler,
	shopHandler *handler.ShopHandler,
	orderHandler *handler.OrderHandler,
	adminHandler *handler.AdminHandler,
	serviceHandler *handler.ServiceHandler,
) *gin.Engine {
	r := gin.New()

	// ==================== 全局中间件 ====================
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// ==================== 健康检查（不走任何中间件） ====================
	r.GET("/ping", func(c *gin.Context) {
		response.Success(c, gin.H{
			"message": "pong",
		})
	})

	// ==================== API v1 路由组 ====================
	apiV1 := r.Group("/api/v1")
	{
		// ---------- 公开路由（无需认证） ----------

		// 用户注册/登录
		user := apiV1.Group("/user")
		{
			user.POST("/register", userHandler.Register)
			user.POST("/login", userHandler.Login)
		}

		// 商品/活动查询（走三级缓存，不需要登录）
		apiV1.GET("/product/:id", shopHandler.GetProduct)
		apiV1.GET("/activity/:id", shopHandler.GetActivity)

		// ---------- 管理后台路由（开发环境使用） ----------
		admin := apiV1.Group("/admin")
		admin.Use(
			middleware.TraceMiddleware(),
			middleware.AdminTokenAuth(),
		)
		{
			admin.POST("/warmup", adminHandler.WarmUp)
			admin.POST("/seed", adminHandler.SeedTestData)
			admin.GET("/benchmark/metrics", adminHandler.BenchmarkMetrics)
		}

		// ---------- 鉴权路由（Trace → JWT → 用户级限流） ----------
		//
		// 中间件执行顺序：
		//   TraceMiddleware → 注入 X-Trace-ID（链路追踪）
		//   JWTAuth         → 验证 Token，注入 UserID
		//   RateLimiter     → 读取 UserID，执行用户级令牌桶限流
		authGroup := apiV1.Group("")
		authGroup.Use(
			middleware.TraceMiddleware(),
			middleware.JWTAuth(),
			middleware.RateLimiter(),
		)
		{
			// 秒杀下单接口（额外增加 IP 级前置限流，防御未鉴权的恶意请求）
			authGroup.POST("/seckill", middleware.IPRateLimiter(), seckillHandler.DoSeckill)

			// 秒杀结果轮询接口
			authGroup.GET("/seckill/result", seckillHandler.QueryResult)

			// 订单查询接口
			authGroup.GET("/orders", orderHandler.ListOrders)
			authGroup.GET("/order/:orderNo", orderHandler.GetOrder)

			// 订单支付接口
			authGroup.POST("/order/:orderNo/pay", orderHandler.PayOrder)

			// Agent 智能客服接口
			authGroup.POST("/service/chat", serviceHandler.Chat)
			authGroup.POST("/service/complaint", serviceHandler.Complaint)
		}
	}

	return r
}
