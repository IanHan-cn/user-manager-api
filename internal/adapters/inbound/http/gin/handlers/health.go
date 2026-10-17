// Package handlers 存放 HTTP 传输层的请求处理器。
package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/responses"
)

// ReadinessProbe 抽象就绪检查，便于在单元测试中用替身覆盖失败路径。
type ReadinessProbe interface {
	// Check 返回 nil 表示服务具备处理请求的条件，否则返回失败原因。
	Check(ctx context.Context) error
}

// HealthHandler 提供存活与就绪检查端点。
type HealthHandler struct {
	probe ReadinessProbe
}

// NewHealthHandler 使用给定探针构造健康检查处理器。
func NewHealthHandler(probe ReadinessProbe) *HealthHandler {
	return &HealthHandler{probe: probe}
}

// Live 存活检查：只要进程仍在运行即返回 200，不触碰任何外部依赖，
// 避免数据库抖动导致容器被编排平台误杀。
func (h *HealthHandler) Live(c *gin.Context) {
	responses.OK(c, map[string]string{"status": "ok"})
}

// Ready 就绪检查：真实校验数据库、迁移状态与策略加载情况。
// 依赖不可用必须返回 503（fail closed），且不在响应中泄漏内部错误细节。
func (h *HealthHandler) Ready(c *gin.Context) {
	if err := h.probe.Check(c.Request.Context()); err != nil {
		slog.WarnContext(c.Request.Context(), "readiness check failed", "error", err)
		responses.Fail(c, http.StatusServiceUnavailable, "NOT_READY", "service not ready")
		return
	}
	responses.OK(c, map[string]string{"status": "ok"})
}
