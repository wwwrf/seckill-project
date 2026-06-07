package handler

import (
	"strconv"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ==================== 管理后台 Handler ====================
//
// 提供活动预热和测试数据生成接口。
// 生产环境中应加 Admin 权限校验中间件，
// 当前开发阶段为便于测试暂不鉴权。

// AdminHandler 管理后台 Handler
type AdminHandler struct {
	seckillCache *cache.SeckillCache
	db           *gorm.DB
}

// NewAdminHandler 创建管理后台 Handler
func NewAdminHandler(seckillCache *cache.SeckillCache, db *gorm.DB) *AdminHandler {
	return &AdminHandler{seckillCache: seckillCache, db: db}
}

// WarmUpRequest 活动预热请求
type WarmUpRequest struct {
	ActivityID int64 `json:"activity_id" binding:"required,gt=0"`
}

type BenchmarkMetricsQuery struct {
	ActivityID int64 `form:"activity_id" binding:"required,gt=0"`
	ProductID  int64 `form:"product_id" binding:"required,gt=0"`
}

// WarmUp 活动预热：将库存写入 Redis + BloomFilter
//
// @Summary      秒杀活动预热
// @Description  将指定秒杀活动的库存加载到 Redis 缓存中（布隆过滤器 + 库存 Key）
// @Tags         管理后台
// @Accept       json
// @Produce      json
// @Param        body  body      WarmUpRequest  true  "预热请求参数"
// @Success      200   {object}  response.Response  "预热成功"
// @Router       /api/v1/admin/warmup [post]
func (h *AdminHandler) WarmUp(c *gin.Context) {
	var req WarmUpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "参数错误：activity_id 必须为正整数")
		return
	}

	// 查 DB 获取活动信息
	var activity model.SeckillActivity
	if err := h.db.First(&activity, req.ActivityID).Error; err != nil {
		logger.Error("预热查询活动失败", zap.Int64("activityID", req.ActivityID), zap.Error(err))
		response.Error(c, e.CodeBadRequest, "活动不存在")
		return
	}

	// 执行预热
	if err := h.seckillCache.WarmUpActivity(
		c.Request.Context(),
		activity.ID,
		activity.ProductID,
		activity.AvailableStock,
	); err != nil {
		logger.Error("活动预热失败", zap.Int64("activityID", activity.ID), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "预热失败")
		return
	}

	response.Success(c, gin.H{
		"message":     "活动预热成功",
		"activity_id": activity.ID,
		"product_id":  activity.ProductID,
		"stock":       activity.AvailableStock,
	})
}

// SeedTestData 创建测试数据（仅开发环境使用）
//
// @Summary      创建测试数据
// @Description  一键创建测试商品和秒杀活动并自动预热（开发环境使用）
// @Tags         管理后台
// @Accept       json
// @Produce      json
// @Success      200  {object}  response.Response  "测试数据创建成功"
// @Router       /api/v1/admin/seed [post]
func (h *AdminHandler) SeedTestData(c *gin.Context) {
	// 创建测试商品
	product := &model.Product{
		Title:         "iPhone 16 Pro Max 256GB",
		Description:   "Apple 年度旗舰，A18 Pro 芯片",
		OriginalPrice: 999900, // 9999.00 元（分）
		Status:        1,
	}
	if err := h.db.Create(product).Error; err != nil {
		logger.Error("创建测试商品失败", zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "创建测试数据失败")
		return
	}

	// 创建秒杀活动
	now := time.Now()
	activity := &model.SeckillActivity{
		ProductID:      product.ID,
		ActivityName:   "测试秒杀活动 - iPhone 直降8000",
		SeckillPrice:   199900, // 1999.00 元（分）
		TotalStock:     100,
		AvailableStock: 100,
		Version:        0,
		StartTime:      now,
		EndTime:        now.Add(24 * time.Hour),
		Status:         2, // 进行中
	}
	if err := h.db.Create(activity).Error; err != nil {
		logger.Error("创建测试活动失败", zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "创建测试数据失败")
		return
	}

	// 自动预热
	if err := h.seckillCache.WarmUpActivity(
		c.Request.Context(),
		activity.ID,
		activity.ProductID,
		activity.AvailableStock,
	); err != nil {
		logger.Warn("自动预热失败，请手动预热", zap.Error(err))
	}

	response.Success(c, gin.H{
		"message":     "测试数据创建成功并已预热",
		"product_id":  product.ID,
		"activity_id": activity.ID,
		"stock":       activity.AvailableStock,
	})
}

// BenchmarkMetrics 返回压测汇总指标
func (h *AdminHandler) BenchmarkMetrics(c *gin.Context) {
	var req BenchmarkMetricsQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "参数错误：activity_id 和 product_id 必须为正整数")
		return
	}

	summary, err := h.seckillCache.GetBenchmarkSummary(c.Request.Context(), req.ActivityID, req.ProductID)
	if err != nil {
		logger.Error("读取压测指标失败", zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "读取压测指标失败")
		return
	}

	acceptedTotal := parseInt64(summary["accepted_total"])
	createdTotal := parseInt64(summary["created_total"])
	latencyTotalMs := parseInt64(summary["create_latency_total_ms"])
	pendingKey := e.BuildPendingKey(req.ActivityID)
	pendingCount, _ := h.seckillCache.GetPendingAll(c.Request.Context(), pendingKey)
	backlog := len(pendingCount)

	avgCreateLatencyMs := int64(0)
	if createdTotal > 0 && latencyTotalMs > 0 {
		avgCreateLatencyMs = latencyTotalMs / createdTotal
	}

	response.Success(c, gin.H{
		"activity_id":            req.ActivityID,
		"product_id":             req.ProductID,
		"accepted_total":         acceptedTotal,
		"created_total":          createdTotal,
		"backlog_pending":        backlog,
		"avg_create_latency_ms":  avgCreateLatencyMs,
		"last_accepted_at_ms":    parseInt64(summary["last_accepted_at_ms"]),
		"last_created_at_ms":     parseInt64(summary["last_created_at_ms"]),
	})
}

func parseInt64(raw string) int64 {
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}
