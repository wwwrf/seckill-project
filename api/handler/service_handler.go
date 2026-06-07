package handler

import (
	"seckill-system/internal/middleware"
	"seckill-system/internal/model/e"
	"seckill-system/internal/service"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ServiceHandler struct {
	agentSvc *service.AgentService
}

func NewServiceHandler(agentSvc *service.AgentService) *ServiceHandler {
	return &ServiceHandler{agentSvc: agentSvc}
}

type ChatRequest struct {
	Message string `json:"message" binding:"required,min=2,max=2000"`
}

type ComplaintRequest struct {
	Message string `json:"message" binding:"required,min=2,max=2000"`
}

func (h *ServiceHandler) Chat(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		return
	}

	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "message 参数不合法")
		return
	}

	reply, err := h.agentSvc.Chat(c.Request.Context(), userID, req.Message)
	if err != nil {
		logger.Error("客服咨询处理失败", zap.Int64("userID", userID), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
		return
	}

	response.Success(c, reply)
}

func (h *ServiceHandler) Complaint(c *gin.Context) {
	userID, ok := extractUserID(c)
	if !ok {
		return
	}

	var req ComplaintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "message 参数不合法")
		return
	}

	reply, err := h.agentSvc.HandleComplaint(c.Request.Context(), userID, req.Message)
	if err != nil {
		logger.Error("投诉处理失败", zap.Int64("userID", userID), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
		return
	}

	response.Success(c, reply)
}

func extractUserID(c *gin.Context) (int64, bool) {
	userIDVal, exists := c.Get(middleware.ContextKeyUserID)
	if !exists {
		response.Unauthorized(c, "用户未登录")
		return 0, false
	}
	userID, ok := userIDVal.(int64)
	if !ok {
		response.Error(c, e.CodeSystemBusy, "系统错误")
		return 0, false
	}
	return userID, true
}
