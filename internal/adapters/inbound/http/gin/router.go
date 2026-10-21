package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/handlers"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/middleware"
)

// Deps 汇总 HTTP 层需要的用例依赖。
// 契约由消费方（本包）声明、字段一律为接口：
// 既避免 inbound 层反向依赖 bootstrap 造成导入循环，
// 也让 NewRouter 的入参自解释，单测时可直接注入替身。
type Deps struct {
	// Readiness 供 /health/ready 做真实依赖检查。
	Readiness handlers.ReadinessProbe

	// 后续用例按接口逐个追加，例如：
	// Users user.UseCase
}

// NewRouter 按给定依赖装配路由树。
func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID(), middleware.Logging())

	health := handlers.NewHealthHandler(deps.Readiness)
	r.GET("/health/live", health.Live)
	r.GET("/health/ready", health.Ready)

	v1 := r.Group("/api/v1")

	return r
}
