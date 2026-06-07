package handler

import (
	"errors"

	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/internal/service"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ==================== 用户 HTTP Handler ====================

// UserHandler 用户接口 Handler
type UserHandler struct {
	svc *service.UserService
}

// NewUserHandler 创建用户 Handler 实例
func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// RegisterRequest 注册请求参数
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=6,max=64"`
}

// LoginRequest 登录请求参数
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Register 用户注册
//
// @Summary      用户注册
// @Description  通过用户名和密码注册新用户
// @Tags         用户
// @Accept       json
// @Produce      json
// @Param        body  body      RegisterRequest  true  "注册请求参数"
// @Success      200   {object}  response.Response  "注册成功"
// @Router       /api/v1/user/register [post]
func (h *UserHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "参数错误：用户名3-32位，密码6-64位")
		return
	}

	if err := h.svc.Register(c.Request.Context(), req.Username, req.Password); err != nil {
		if errors.Is(err, repository.ErrUsernameTaken) {
			response.Error(c, e.CodeBadRequest, "用户名已被注册")
			return
		}
		logger.Error("用户注册失败", zap.String("username", req.Username), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
		return
	}

	response.SuccessWithMsg(c, "注册成功", nil)
}

// Login 用户登录
//
// @Summary      用户登录
// @Description  使用用户名和密码登录，返回 JWT Token
// @Tags         用户
// @Accept       json
// @Produce      json
// @Param        body  body      LoginRequest  true  "登录请求参数"
// @Success      200   {object}  response.Response  "登录成功，返回 token 和 user_id"
// @Router       /api/v1/user/login [post]
func (h *UserHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, e.CodeBadRequest, "参数错误：请填写用户名和密码")
		return
	}

	token, userID, err := h.svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			response.Error(c, e.CodeUnauthorized, "用户名或密码错误")
			return
		}
		logger.Error("用户登录失败", zap.String("username", req.Username), zap.Error(err))
		response.Error(c, e.CodeSystemBusy, "系统繁忙，请稍后重试")
		return
	}

	response.SuccessWithMsg(c, "登录成功", gin.H{
		"token":   token,
		"user_id": userID,
	})
}
