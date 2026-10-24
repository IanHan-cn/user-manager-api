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
//
// 边界：Deps 只承载**依赖**（用例、探针、认证端口），不承载**路由表**。
// URL 与 handler 的对应关系属于传输层的事务，归本包所有；
// 装配层（bootstrap）只负责把依赖注入进来，不参与路由注册。
type Deps struct {
	// Readiness 供 /health/ready 做真实依赖检查。
	Readiness handlers.ReadinessProbe

	// Auth 提供认证能力：既作为 OIDC 回调端点的 handler，
	// 也是登录态与调用方身份的来源。
	// 声明为端口接口，HTTP 层因此不依赖任何具体认证实现。
	Auth ports.Authenticator

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

	// 认证端点：/login、/callback、/logout 是 OIDC 协议约定的标准端点，
	// 依赖的是协议而非某个具体认证实现。
	//
	// 这里逐条登记，而不是用 /auth/*action 通配：
	// gin 的通配路由会独占整个 /auth 命名空间，
	// 之后便无法再注册 /auth/me 这类业务端点（注册时会 panic）。
	//
	// 方法按各端点的协议语义逐一收紧：
	//   - /login：发起认证跳转，属于浏览器导航，只接受 GET；
	//   - /callback：既可能是 IdP 的 302 回跳（query 传参），
	//     也可能是 form_post 响应模式下的表单提交，故 GET 与 POST 都接受；
	//   - /logout：RP-Initiated Logout 由用户代理发起，只接受 GET。
	r.GET("/auth/login", gin.WrapH(deps.Auth))
	r.GET("/auth/callback", gin.WrapH(deps.Auth))
	r.POST("/auth/callback", gin.WrapH(deps.Auth))
	r.GET("/auth/logout", gin.WrapH(deps.Auth))

	registerV1(newV1Group(r, deps.Auth), deps)

	return r
}

// newV1Group 创建 /api/v1 分组并挂载强制认证中间件。
//
// 单独抽出是为了让"业务路由默认要求登录"这条规则只有一个落点，
// 后续新增业务分组时不会出现有的挂了中间件、有的没挂的情况。
func newV1Group(r *gin.Engine, a ports.Authenticator) *gin.RouterGroup {
	v1 := r.Group("/api/v1")
	// 业务接口默认要求登录；如需公开访问的接口，
	// 放在单独的 group 上使用 middleware.CheckAuth。
	v1.Use(middleware.RequireAuth(a))
	return v1
}

// registerV1 在 /api/v1 分组下注册业务路由。
//
// 路由表归本包所有：URL 与 handler 的对应关系是传输层的事务，
// 装配层不参与，只把用例依赖注入 Deps。
// 新增业务接口时在这里追加，不要让外部在分组上自行注册，
// 否则容易绕过这里的认证中间件与统一响应格式。
func registerV1(v1 *gin.RouterGroup, deps Deps) {
	// TODO(user): user handler 就绪后在此追加，例如：
	users := handlers.NewUsersHandler()
	v1.GET("/user", users.GetUsersList)
}
