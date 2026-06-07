package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response 统一响应结构
type Response struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// Success 成功响应
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code: 0,
		Msg:  "success",
		Data: data,
	})
}

// SuccessWithMsg 成功响应（自定义消息）
func SuccessWithMsg(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code: 0,
		Msg:  msg,
		Data: data,
	})
}

// Error 错误响应
func Error(c *gin.Context, code int, msg string) {
	c.JSON(http.StatusOK, Response{
		Code: code,
		Msg:  msg,
	})
}

// ErrorWithData 错误响应（带数据）
func ErrorWithData(c *gin.Context, code int, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code: code,
		Msg:  msg,
		Data: data,
	})
}

// BadRequest 400 错误
func BadRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, Response{
		Code: http.StatusBadRequest,
		Msg:  msg,
	})
}

// Unauthorized 401 错误
func Unauthorized(c *gin.Context, msg string) {
	c.JSON(http.StatusUnauthorized, Response{
		Code: http.StatusUnauthorized,
		Msg:  msg,
	})
}

// Forbidden 403 错误
func Forbidden(c *gin.Context, msg string) {
	c.JSON(http.StatusForbidden, Response{
		Code: http.StatusForbidden,
		Msg:  msg,
	})
}

// NotFound 404 错误
func NotFound(c *gin.Context, msg string) {
	c.JSON(http.StatusNotFound, Response{
		Code: http.StatusNotFound,
		Msg:  msg,
	})
}

// InternalServerError 500 错误
func InternalServerError(c *gin.Context, msg string) {
	c.JSON(http.StatusInternalServerError, Response{
		Code: http.StatusInternalServerError,
		Msg:  msg,
	})
}
