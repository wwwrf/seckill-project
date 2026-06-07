package middleware

import (
	"crypto/subtle"

	"seckill-system/pkg/logger"
	"seckill-system/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// AdminTokenAuth 管理接口鉴权中间件
//
// 约定：请求头携带 X-Admin-Token，值必须与配置 admin.token 完全一致。
// 说明：
//   - 使用固定时间比较，避免计时侧信道泄漏
//   - 未配置 admin.token 时默认拒绝，防止误开放管理接口
func AdminTokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := viper.GetString("admin.token")
		if expected == "" {
			logger.Error("管理接口鉴权失败：admin.token 未配置")
			response.Forbidden(c, "管理接口未开放")
			c.Abort()
			return
		}

		provided := c.GetHeader("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			logger.Warn("管理接口鉴权失败",
				zap.String("path", c.Request.URL.Path),
				zap.String("clientIP", c.ClientIP()),
			)
			response.Forbidden(c, "无管理权限")
			c.Abort()
			return
		}

		c.Next()
	}
}
