// Package responses 统一 HTTP 成功与失败响应格式。
//
//   - 成功：{"data": ..., "request_id": "..."}
//   - 失败：{"error": {"code": "...", "message": "..."}, "request_id": "..."}
//
// request_id 的上下文键定义在本包，避免 middleware 与 responses 相互 import 造成循环依赖。
package responses

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	// RequestIDContextKey 是 request_id 在 Gin 上下文中的键名。
	RequestIDContextKey = "request_id"
	// HeaderRequestID 是请求 ID 的响应头名称。
	HeaderRequestID = "X-Request-ID"
)

// 错误码常量集中定义在本包，作为**唯一来源**。
//
// 传输层的 handlers 与 middleware 都引用这些常量，避免同一语义散落多处、
// 甚至出现硬编码字符串导致前后端口径不一致。
const (
	// CodeUnauthenticated 表示缺少或无效的身份凭证（401）。
	CodeUnauthenticated = "UNAUTHENTICATED"
	// CodeForbidden 表示身份有效但权限不足（403）。
	CodeForbidden = "FORBIDDEN"
	// CodeNotFound 表示资源不存在，或按防枚举策略隐藏了资源存在性（404）。
	CodeNotFound = "NOT_FOUND"
	// CodeDependencyUnavailable 表示必要依赖暂时不可用（503）。
	CodeDependencyUnavailable = "DEPENDENCY_UNAVAILABLE"
	// CodeTenantSuspended 表示租户已被停用（403）。
	CodeTenantSuspended = "TENANT_SUSPENDED"
	// CodeMembershipInactive 表示成员关系不是 active（403）。
	CodeMembershipInactive = "MEMBERSHIP_INACTIVE"
	// CodeInvalidArgument 表示请求参数不合法（400）。
	CodeInvalidArgument = "INVALID_ARGUMENT"
	// CodeInternal 表示未预期的服务端错误（500）。
	CodeInternal = "INTERNAL"
	// CodeRateLimited 表示请求频率超限（429）。
	// 与 CodeForbidden 严格区分：前者表示"稍后重试"，后者表示"无权访问"。
	CodeRateLimited = "RATE_LIMITED"
)

// Pagination 描述分页元信息。
type Pagination struct {
	// Limit 是实际生效的每页条数。
	Limit int `json:"limit"`
	// Offset 是实际生效的偏移量。
	Offset int `json:"offset"`
	// Total 是满足条件的记录总数。
	Total int64 `json:"total"`
}

// ListEnvelope 是分页列表的统一响应体。
//
// 所有管理类列表接口都返回该信封，避免前端为每个接口各写一套分页解析。
type ListEnvelope struct {
	// Items 是当页数据。
	Items any `json:"items"`
	// Pagination 是分页元信息。
	Pagination Pagination `json:"pagination"`
}

// OKList 以 200 返回分页列表的统一信封。
func OKList(c *gin.Context, items any, limit, offset int, total int64) {
	c.JSON(http.StatusOK, Envelope{
		Data:      ListEnvelope{Items: items, Pagination: Pagination{Limit: limit, Offset: offset, Total: total}},
		RequestID: requestID(c),
	})
}

// Envelope 是成功响应的统一外层结构。
type Envelope struct {
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id"`
}

// ErrorBody 描述错误码与面向客户端的错误信息。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorEnvelope 是失败响应的统一外层结构。
type ErrorEnvelope struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

// OK 以 200 状态码返回统一格式的成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Data: data, RequestID: requestID(c)})
}

// Fail 以指定状态码返回统一格式的失败响应，并保证响应头携带同一 request_id。
func Fail(c *gin.Context, status int, code, message string) {
	id := requestID(c)
	if id != "" {
		c.Writer.Header().Set(HeaderRequestID, id)
	}
	c.JSON(status, ErrorEnvelope{
		Error:     ErrorBody{Code: code, Message: message},
		RequestID: id,
	})
}

// requestID 依次从 Gin 上下文与响应头中取请求 ID，取不到时返回空串。
func requestID(c *gin.Context) string {
	if id := c.GetString(RequestIDContextKey); id != "" {
		return id
	}
	return c.Writer.Header().Get(HeaderRequestID)
}
