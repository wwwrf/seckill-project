package handler

import (
	"errors"

	"seckill-system/internal/middleware"
	"seckill-system/internal/model/e"
	"seckill-system/internal/service"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SeckillHandler 秒杀接口 Handler
type SeckillHandler struct {
	svc *service.SeckillService
}

// NewSeckillHandler 创建秒杀 Handler 实例
func NewSeckillHandler(svc *service.SeckillService) *SeckillHandler {
	return &SeckillHandler{svc: svc}
}

// SeckillRequest 秒杀下单请求参数
// activity_id 和 product_id 从请求 Body（JSON）中解析。
// user_id 从 JWT Token 中提取，不在此结构体中 —— 安全准则。
type SeckillRequest struct {
	ActivityID int64 `json:"activity_id" binding:"required,gt=0"` // 秒杀活动 ID
	ProductID  int64 `json:"product_id" binding:"required,gt=0"`  // 商品 ID
}

// SeckillResultQuery 秒杀结果轮询请求参数
type SeckillResultQuery struct {
	ActivityID int64 `form:"activity_id" binding:"required,gt=0"` // 秒杀活动 ID
	ProductID  int64 `form:"product_id" binding:"required,gt=0"`  // 商品 ID
	OrderNo    string `form:"order_no"`                             // 订单号（可选，建议携带以精准判断 pending）
}

// DoSeckill 秒杀下单接口
// @Summary      秒杀下单
// @Description  提交秒杀下单请求（异步处理），需要先登录获取 Token
// @Tags         秒杀
// @Accept       json
// @Produce      json
// @Param        body           body      SeckillRequest  true  "秒杀请求参数"
// @Success      200            {object}  response.Response  "抢购成功，排队处理中"
// @Security     ApiKeyAuth
// @Router       /api/v1/seckill [post]
func (h *SeckillHandler) DoSeckill(c *gin.Context) {

	// ---- 1. 从 JWT 上下文中提取 userID ----
	userID, ok := h.extractUserID(c)
	if !ok {
		return // extractUserID 内部已写入响应
	}

	// ---- 2. 解析并校验请求参数 ----
	var req SeckillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Warn("秒杀请求参数校验失败",
			zap.Int64("userID", userID),
			zap.Error(err),
		)
		response.Error(c, e.CodeBadRequest, "请求参数错误：activity_id 和 product_id 必须为正整数")
		return
	}

	// ---- 3. 调用 Service 层执行秒杀逻辑 ----
	result, err := h.svc.DoSeckill(c.Request.Context(), userID, req.ActivityID, req.ProductID)
	if err != nil {
		h.handleSeckillError(c, err, userID, req.ActivityID, req.ProductID)
		return
	}

	// ---- 4. 根据 SeckillResult.Code 返回差异化响应 ----
	if result.Code == e.CodeSeckillQueuing {
		// 已在排队中（重复购买 / 连击冷却）
		// 返回 HTTP 200 + code 20002，前端进入轮询模式
		response.Error(c, e.CodeSeckillQueuing, result.Msg)
		return
	}

	// 排队成功（Code=0），返回 OrderNo
	response.SuccessWithMsg(c, result.Msg, gin.H{
		"order_no": result.OrderNo,
	})
}

// QueryResult 秒杀结果轮询接口
//
// @Summary      查询秒杀结果
// @Description  轮询查询秒杀下单结果（SUCCESS / PROCESSING / FAILED）
// @Tags         秒杀
// @Accept       json
// @Produce      json
// @Param        activity_id    query     int     true   "活动ID"
// @Param        product_id     query     int     true   "商品ID"
// @Success      200            {object}  response.Response  "查询成功"
// @Security     ApiKeyAuth
// @Router       /api/v1/seckill/result [get]
func (h *SeckillHandler) QueryResult(c *gin.Context) {

	// ---- 1. 从 JWT 上下文中提取 userID ----
	userID, ok := h.extractUserID(c)
	if !ok {
		return
	}

	// ---- 2. 解析 Query 参数 ----
	var req SeckillResultQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		logger.Warn("轮询请求参数校验失败",
			zap.Int64("userID", userID),
			zap.Error(err),
		)
		response.Error(c, e.CodeBadRequest, "请求参数错误：activity_id 和 product_id 必须为正整数")
		return
	}

	// ---- 3. 调用 Service 查询秒杀结果 ----
	result, err := h.svc.QuerySeckillResult(c.Request.Context(), userID, req.ActivityID, req.ProductID, req.OrderNo)
	if err != nil {
		logger.Error("查询秒杀结果失败",
			zap.Int64("userID", userID),
			zap.Int64("activityID", req.ActivityID),
			zap.Int64("productID", req.ProductID),
			zap.Error(err),
		)
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
		return
	}

	// ---- 4. 返回查询结果 ----
	response.Success(c, result)
}

// ==================== 内部辅助方法 ====================

// extractUserID 从 gin.Context 中提取 JWT 注入的 userID
//
// 提取公共逻辑，避免 DoSeckill 和 QueryResult 重复代码。
// 如果提取失败，会直接写入 HTTP 响应并返回 false。
func (h *SeckillHandler) extractUserID(c *gin.Context) (int64, bool) {
	userIDVal, exists := c.Get(middleware.ContextKeyUserID)
	if !exists {
		logger.Warn("请求缺少 userID，JWT 中间件可能未正确注入",
			zap.String("path", c.Request.URL.Path),
			zap.String("clientIP", c.ClientIP()),
		)
		response.Unauthorized(c, "用户未登录")
		return 0, false
	}

	userID, ok := userIDVal.(int64)
	if !ok {
		logger.Error("userID 类型断言失败（检查 JWTAuth 实现）",
			zap.Any("userIDVal", userIDVal),
		)
		response.Error(c, e.CodeSystemBusy, "系统错误")
		return 0, false
	}

	return userID, true
}

// handleSeckillError 根据哨兵错误类型返回差异化的业务响应
func (h *SeckillHandler) handleSeckillError(c *gin.Context, err error, userID, activityID, productID int64) {
	switch {
	case errors.Is(err, e.ErrSeckillSoldOut):
		// 售罄：高频预期场景，Cache 层已写日志，此处不重复打印
		response.Error(c, e.CodeSeckillSoldOut, "活动已售罄")

	case errors.Is(err, e.ErrSeckillActivityNotStart):
		// 活动未开始或已结束
		response.Error(c, e.CodeSeckillNotStart, "活动未开始或已结束")

	default:
		// 未预期的错误类型，补打 ERROR 日志用于排查
		logger.Error("秒杀接口未预期错误",
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
	}
}
