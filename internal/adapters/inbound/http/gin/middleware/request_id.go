package middleware

import (
	"regexp"
	"strings"
	"uuid"

	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/responses"
)

// requestIDMaxLen 限制请求 ID 长度，避免超长请求头进入日志与存储。
const requestIDMaxLen = 128

// requestIDPattern 限定请求 ID 的字符集，杜绝换行等可用于日志注入的字符。
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// RequestID 保证每个请求都有可用的 request_id：
//   - 客户端提供的请求 ID 合法时原样透传，便于跨服务串联链路；
//   - 缺失或非法（超长、含换行等）时重新生成，不信任客户端输入。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitizeRequestID(c.GetHeader(responses.HeaderRequestID))
		if id == "" {
			id = uuid.NewV7().String()
		}
		c.Set(responses.RequestIDContextKey, id)
		c.Writer.Header().Set(responses.HeaderRequestID, id)
		c.Next()
	}
}

// sanitizeRequestID 返回合法的请求 ID，非法输入返回空串表示应重新生成。
func sanitizeRequestID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > requestIDMaxLen {
		return ""
	}
	if !requestIDPattern.MatchString(trimmed) {
		return ""
	}
	return trimmed
}
