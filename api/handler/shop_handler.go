package handler

import (
	"strconv"

	"seckill-system/internal/model/e"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ==================== 商品/活动查询 Handler ====================
//   - 所有查询走 ProductCache 三级缓存（L1 本地 → L2 Redis → L3 DB）
//   - 内置缓存穿透（空值缓存）、击穿（singleflight）、雪崩（随机 TTL）防护
//   - 查询接口为公开接口（无需 JWT），用于前端展示活动/商品信息

// ShopHandler 商品/活动查询 Handler
type ShopHandler struct {
	productCache *cache.ProductCache
}

// NewShopHandler 创建商品查询 Handler
func NewShopHandler(productCache *cache.ProductCache) *ShopHandler {
	return &ShopHandler{productCache: productCache}
}

// GetProduct 查询商品详情
//
// @Summary      查询商品详情
// @Description  根据商品 ID 查询商品详情（三级缓存：L1 本地 → L2 Redis → L3 MySQL）
// @Tags         商品
// @Accept       json
// @Produce      json
// @Param        id   path      int  true  "商品ID"
// @Success      200  {object}  response.Response  "查询成功"
// @Router       /api/v1/product/{id} [get]
func (h *ShopHandler) GetProduct(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		response.Error(c, e.CodeBadRequest, "无效的商品 ID")
		return
	}

	product, err := h.productCache.GetProduct(c.Request.Context(), id)
	if err != nil {
		logger.Error("查询商品失败", zap.Int64("productID", id), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙")
		return
	}

	if product == nil {
		response.Error(c, e.CodeBadRequest, "商品不存在")
		return
	}

	response.Success(c, product)
}

// GetActivity 查询秒杀活动详情
//
// @Summary      查询秒杀活动详情
// @Description  根据活动 ID 查询秒杀活动详情（三级缓存）
// @Tags         商品
// @Accept       json
// @Produce      json
// @Param        id   path      int  true  "活动ID"
// @Success      200  {object}  response.Response  "查询成功"
// @Router       /api/v1/activity/{id} [get]
func (h *ShopHandler) GetActivity(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		response.Error(c, e.CodeBadRequest, "无效的活动 ID")
		return
	}

	activity, err := h.productCache.GetActivity(c.Request.Context(), id)
	if err != nil {
		logger.Error("查询活动失败", zap.Int64("activityID", id), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙")
		return
	}

	if activity == nil {
		response.Error(c, e.CodeBadRequest, "活动不存在")
		return
	}

	response.Success(c, activity)
}
