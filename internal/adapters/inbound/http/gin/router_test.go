package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/ports"
)

// stubReadiness 是就绪探针的测试替身，永远返回就绪。
type stubReadiness struct{}

func (stubReadiness) Check(ctx context.Context) error { return nil }

// stubAuth 是认证端口的测试替身，只记录收到的请求路径与认证跳转。
type stubAuth struct {
	// hitPaths 记录被访问过的路径。
	hitPaths []string
	// authenticated 记录是否发生过认证跳转。
	authenticated bool
}

func (s *stubAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hitPaths = append(s.hitPaths, r.URL.Path)
	w.WriteHeader(http.StatusNoContent)
}

func (s *stubAuth) Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string) {
	s.authenticated = true
}

// Principal 始终返回 (零值, nil)，模拟"没有有效登录态"的适配器。
// 依赖中间件对空 Subject 的兜底判断，认证端点才会触发认证跳转。
func (s *stubAuth) Principal(r *http.Request) (ports.Principal, error) {
	return ports.Principal{}, nil
}

func TestNewRouter_注册认证端点(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth := &stubAuth{}
	r := NewRouter(Deps{Readiness: stubReadiness{}, Auth: auth})

	for _, path := range []string{"/auth/login", "/auth/callback", "/auth/logout"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s 状态码 = %d, 期望 %d", path, w.Code, http.StatusNoContent)
		}
	}
}

// TestNewRouter_认证端点只接受约定的方法
//
// 锁定各端点的协议语义：
//   - /auth/login、/auth/logout 由用户代理发起，只接受 GET；
//   - /auth/callback 需要兼容 query 回跳与 form_post 表单提交。
//
// 其余方法不应命中认证实现。
func TestNewRouter_认证端点只接受约定的方法(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		method string
		path   string
		// allowed 表示该方法是否应当被接受。
		allowed bool
	}{
		{http.MethodGet, "/auth/login", true},
		{http.MethodPost, "/auth/login", false},
		{http.MethodGet, "/auth/callback", true},
		{http.MethodPost, "/auth/callback", true},
		{http.MethodDelete, "/auth/callback", false},
		{http.MethodGet, "/auth/logout", true},
		{http.MethodPost, "/auth/logout", false},
	}

	for _, c := range cases {
		auth := &stubAuth{}
		r := NewRouter(Deps{Readiness: stubReadiness{}, Auth: auth})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))

		accepted := w.Code == http.StatusNoContent
		if accepted != c.allowed {
			t.Fatalf("%s %s 被接受 = %v (状态码 %d), 期望 %v",
				c.method, c.path, accepted, w.Code, c.allowed)
		}
	}
}

// TestNewRouter_认证命名空间可继续扩展
//
// 这条用例锁定的是"不用通配路由"这个决定：
// 若把三条认证路由改回 /auth/*action，本用例会在注册时 panic。
func TestNewRouter_认证命名空间可继续扩展(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(Deps{Readiness: stubReadiness{}, Auth: &stubAuth{}})

	// 业务端点可以安全地落在 /auth 命名空间下。
	r.GET("/auth/me", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/auth/me 状态码 = %d, 期望 %d", w.Code, http.StatusOK)
	}
}

// TestV1Group_业务接口默认要求登录
//
// 断言未认证时业务 handler 不会被执行：
// 中间件应发起认证跳转并中断请求链。
//
// 走 newV1Group 而不是 NewRouter，是因为路由表归本包所有，
// 外部无法在分组上自行注册路由。
func TestV1Group_业务接口默认要求登录(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth := &stubAuth{}
	reached := false

	r := gin.New()
	v1 := newV1Group(r, auth)
	v1.GET("/users", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if reached {
		t.Fatal("未认证请求不应进入业务 handler")
	}
	if !auth.authenticated {
		t.Fatal("未认证请求应当触发认证跳转")
	}
}
