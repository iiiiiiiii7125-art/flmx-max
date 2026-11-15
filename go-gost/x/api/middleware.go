package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/auth"
	"github.com/go-gost/core/logger"
)

func mwLogger() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		// start time
		startTime := time.Now()
		// Processing request
		ctx.Next()
		duration := time.Since(startTime)

		logger.Default().WithFields(map[string]any{
			"kind":     "api",
			"method":   ctx.Request.Method,
			"uri":      ctx.Request.RequestURI,
			"code":     ctx.Writer.Status(),
			"client":   ctx.ClientIP(),
			"duration": duration,
		}).Infof("| %3d | %13v | %15s | %-7s %s",
			ctx.Writer.Status(), duration, ctx.ClientIP(), ctx.Request.Method, ctx.Request.RequestURI)
	}
}

func mwBasicAuth(auther auth.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if auther == nil {
			return
		}
		u, p, _ := c.Request.BasicAuth()
		if _, ok := auther.Authenticate(c, u, p); !ok {
			c.Writer.Header().Set("WWW-Authenticate", "Basic")
			c.JSON(http.StatusUnauthorized, Response{
				Code: http.StatusUnauthorized,
				Msg:  "Unauthorized",
			})
			c.Abort()
		}
	}
}

// GlobalInterceptor 全局HTTP请求拦截器。无有效 Basic 认证头的请求返回
// 401 JSON（与 mwBasicAuth 同形），不再静默掐断连接：静默掐断让客户端
// 只看到空回复、无法区分未认证与服务故障。
func GlobalInterceptor() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !hasValidAuth(c) {
			c.Writer.Header().Set("WWW-Authenticate", "Basic")
			c.JSON(http.StatusUnauthorized, Response{
				Code: http.StatusUnauthorized,
				Msg:  "Unauthorized",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// hasValidAuth 检查请求是否包含有效的认证信息
func hasValidAuth(c *gin.Context) bool {
	// 获取Authorization头
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return false
	}

	// 检查是否是Basic认证
	if !strings.HasPrefix(authHeader, "Basic ") {
		return false
	}

	// 解析Basic认证
	_, _, ok := c.Request.BasicAuth()
	if !ok {
		return false
	}

	return true
}
