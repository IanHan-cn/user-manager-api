// Package middleware 存放与传输层相关但与业务无关的 Gin 中间件。
package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/responses"
)

// Logging 记录每个请求的访问日志。
//
// 安全约束：不得记录 Authorization 头、请求体或任何凭据。
func Logging() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		attrs := []any{
			"method", c.Request.Method,
			"path", requestPath(c),
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString(responses.RequestIDContextKey),
			"client_ip", c.ClientIP(),
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}

		level := slog.LevelInfo
		switch {
		case c.Writer.Status() >= 500:
			level = slog.LevelError
		case c.Writer.Status() >= 400:
			level = slog.LevelWarn
		}
		slog.Log(c.Request.Context(), level, "http request", attrs...)
	}
}

// requestPath 优先使用已匹配的路由模板，避免高基数路径污染日志。
func requestPath(c *gin.Context) string {
	if full := c.FullPath(); full != "" {
		return full
	}
	return c.Request.URL.Path
}
