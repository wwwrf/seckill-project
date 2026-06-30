package handler

import (
	"errors"

	"seckill-system/internal/middleware"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ==================== 订单查询 Handler ====================

// OrderHandler 订单查询 Handler
type OrderHandler struct {
	orderRepo *repository.OrderRepo
}

// NewOrderHandler 创建订单 Handler
func NewOrderHandler(orderRepo *repository.OrderRepo) *OrderHandler {
	return &OrderHandler{orderRepo: orderRepo}
}

// mustUserID 从 gin.Context 提取 JWT 注入的 userID。
// 提取失败时直接写入 401 响应并返回 false，调用方应立即 return。
func (h *OrderHandler) mustUserID(c *gin.Context) (int64, bool) {
	val, exists := c.Get(middleware.ContextKeyUserID)
	if !exists {
		response.Unauthorized(c, "用户未登录")
		return 0, false
	}
	return val.(int64), true
}

// ListOrdersRequest 订单列表查询参数
type ListOrdersRequest struct {
	Page     int `form:"page" binding:"omitempty,min=1"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=50"`
}

// ListOrders 查询当前用户的订单列表
//
// @Summary      查询订单列表
// @Description  分页查询当前登录用户的订单列表
// @Tags         订单
// @Accept       json
// @Produce      json
// @Param        page           query     int     false  "页码（默认1）"
// @Param        page_size      query     int     false  "每页数量（默认10，最大50）"
// @Success      200            {object}  response.Response  "查询成功"
// @Security     ApiKeyAuth
// @Router       /api/v1/orders [get]
func (h *OrderHandler) ListOrders(c *gin.Context) {
	userID, ok := h.mustUserID(c)
	if !ok {
		return
	}

	var req ListOrdersRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "参数错误")
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 10
	}

	orders, total, err := h.orderRepo.ListByUserID(c.Request.Context(), userID, req.Page, req.PageSize)
	if err != nil {
		logger.Error("查询订单列表失败", zap.Int64("userID", userID), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙")
		return
	}

	response.Success(c, gin.H{
		"orders":    orders,
		"total":     total,
		"page":      req.Page,
		"page_size": req.PageSize,
	})
}

// GetOrder 查询订单详情
//
// @Summary      查询订单详情
// @Description  根据订单号查询订单详情（仅能查看自己的订单）
// @Tags         订单
// @Accept       json
// @Produce      json
// @Param        orderNo        path      string  true  "订单号"
// @Success      200            {object}  response.Response  "查询成功"
// @Security     ApiKeyAuth
// @Router       /api/v1/order/{orderNo} [get]
func (h *OrderHandler) GetOrder(c *gin.Context) {
	userID, ok := h.mustUserID(c)
	if !ok {
		return
	}

	orderNo := c.Param("orderNo")
	if orderNo == "" {
		response.Error(c, e.CodeBadRequest, "订单号不能为空")
		return
	}

	order, err := h.orderRepo.FindByOrderNo(c.Request.Context(), orderNo)
	if err != nil {
		logger.Error("查询订单详情失败", zap.String("orderNo", orderNo), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙")
		return
	}

	if order == nil || order.UserID != userID {
		response.Error(c, e.CodeBadRequest, "订单不存在")
		return
	}

	response.Success(c, order)
}

// PayOrder 模拟订单支付
//
// @Summary      支付订单
// @Description  模拟支付回调，将订单状态从待支付更新为已支付
// @Tags         订单
// @Accept       json
// @Produce      json
// @Param        orderNo        path      string  true  "订单号"
// @Success      200            {object}  response.Response  "支付成功"
// @Security     ApiKeyAuth
// @Router       /api/v1/order/{orderNo}/pay [post]
func (h *OrderHandler) PayOrder(c *gin.Context) {
	userID, ok := h.mustUserID(c)
	if !ok {
		return
	}

	orderNo := c.Param("orderNo")
	if orderNo == "" {
		response.Error(c, e.CodeBadRequest, "订单号不能为空")
		return
	}

	if err := h.orderRepo.PayOrder(c.Request.Context(), orderNo, userID); err != nil {
		if errors.Is(err, repository.ErrOrderCannotPay) {
			response.Error(c, e.CodeBadRequest, "订单不存在或不可支付")
			return
		}
		logger.Error("支付订单失败", zap.String("orderNo", orderNo), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙")
		return
	}

	response.SuccessWithMsg(c, "支付成功", gin.H{"order_no": orderNo})
}

