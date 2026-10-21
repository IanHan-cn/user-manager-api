package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/handlers"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/middleware"
	"github.com/memories-cn/user-management-api/internal/ports"
)

// Deps 汇总 HTTP 层需要的用例依赖。
// 契约由消费方（本包）声明、字段一律为接口：
// 既避免 inbound 层反向依赖 bootstrap 造成导入循环，
// 也让 NewRouter 的入参自解释，单测时可直接注入替身。
type Deps struct {
	// Readiness 供 /health/ready 做真实依赖检查。
	Readiness handlers.ReadinessProbe

	// Auth 提供认证能力：既作为 OIDC 回调端点的 handler，
	// 也是登录态与调用方身份的来源。
	// 声明为端口接口，HTTP 层因此不依赖任何具体认证实现。
	Auth ports.Authenticator
}

// NewRouter 按给定依赖装配路由树。
func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID(), middleware.Logging())

	health := handlers.NewHealthHandler(deps.Readiness)
	r.GET("/health/live", health.Live)
	r.GET("/health/ready", health.Ready)

	// 认证端点：/auth/login、/auth/callback、/auth/logout 等。
	// 认证实现内部以 /auth 为前缀挂载自己的路由，
	// 这里整段交给它处理，不必在本层重复登记具体端点。
	r.Any("/auth/*action", gin.WrapH(deps.Auth))

	v1 := r.Group("/api/v1")
	// 业务接口默认要求登录；如需公开访问的接口，
	// 放在单独的 group 上使用 middleware.CheckAuth。
	v1.Use(middleware.RequireAuth(deps.Auth))

	return r
}
