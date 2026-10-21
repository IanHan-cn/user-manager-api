package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/ports"
)

// fakeAuthenticator 是 ports.Authenticator 的测试替身。
//
// 它的存在本身就是解耦是否成功的证明：
// middleware 只依赖端口，因此单测无需启动 ZITADEL、
// 无需 OIDC discovery 端点，也不 import 任何第三方认证 SDK。
type fakeAuthenticator struct {
	// principal 是已登录时的身份，Subject 为空表示未登录。
	principal ports.Principal
	// principalErr 非空时 Principal 直接返回该错误。
	principalErr error
	// authenticated 记录是否发生过认证跳转。
	authenticated bool
	// requestedURI 记录认证跳转时携带的原始请求 URI。
	requestedURI string
}

func (f *fakeAuthenticator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAuthenticator) Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string) {
	f.authenticated = true
	f.requestedURI = requestedURI
}

func (f *fakeAuthenticator) Principal(r *http.Request) (ports.Principal, error) {
	if f.principalErr != nil {
		return ports.Principal{}, f.principalErr
	}
	if f.principal.Subject == "" {
		return ports.Principal{}, errors.New("未登录")
	}
	return f.principal, nil
}

// newTestRouter 构造一个只挂目标中间件的最小路由。
func newTestRouter(mw gin.HandlerFunc, reached *bool, got *ports.Principal, found *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	r.GET("/api/v1/users", func(c *gin.Context) {
		*reached = true
		*got, *found = PrincipalFrom(c)
		c.Status(http.StatusOK)
	})
	return r
}

func TestRequireAuth_已登录放行并注入身份(t *testing.T) {
	fake := &fakeAuthenticator{principal: ports.Principal{Subject: "u-1"}}
	var reached bool
	var got ports.Principal
	var found bool
	r := newTestRouter(RequireAuth(fake), &reached, &got, &found)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if !reached {
		t.Fatal("已登录请求应当继续处理")
	}
	if !found {
		t.Fatal("已登录请求应当能取到 Principal")
	}
	if got.Subject != "u-1" {
		t.Fatalf("Principal.Subject = %q, 期望 %q", got.Subject, "u-1")
	}
	if fake.authenticated {
		t.Fatal("已登录请求不应触发认证跳转")
	}
}

func TestRequireAuth_未登录触发跳转并中断(t *testing.T) {
	fake := &fakeAuthenticator{}
	var reached bool
	var got ports.Principal
	var found bool
	r := newTestRouter(RequireAuth(fake), &reached, &got, &found)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if reached {
		t.Fatal("未登录请求必须中断，不能进入业务 handler")
	}
	if !fake.authenticated {
		t.Fatal("未登录请求应当触发认证跳转")
	}
	if fake.requestedURI != "/api/v1/users" {
		t.Fatalf("requestedURI = %q, 期望 %q", fake.requestedURI, "/api/v1/users")
	}
}

func TestRequireAuth_身份查询出错按未登录处理(t *testing.T) {
	fake := &fakeAuthenticator{principalErr: errors.New("会话损坏")}
	var reached bool
	var got ports.Principal
	var found bool
	r := newTestRouter(RequireAuth(fake), &reached, &got, &found)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if reached {
		t.Fatal("身份查询失败时必须中断请求")
	}
	if !fake.authenticated {
		t.Fatal("身份查询失败时应当触发认证跳转")
	}
}

func TestCheckAuth_未登录也放行(t *testing.T) {
	fake := &fakeAuthenticator{}
	var reached bool
	var got ports.Principal
	var found bool
	r := newTestRouter(CheckAuth(fake), &reached, &got, &found)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if !reached {
		t.Fatal("CheckAuth 允许未登录请求继续处理")
	}
	if found {
		t.Fatal("未登录请求不应取到 Principal")
	}
	if fake.authenticated {
		t.Fatal("CheckAuth 不应触发认证跳转")
	}
}

func TestCheckAuth_已登录注入身份(t *testing.T) {
	fake := &fakeAuthenticator{principal: ports.Principal{Subject: "u-2"}}
	var reached bool
	var got ports.Principal
	var found bool
	r := newTestRouter(CheckAuth(fake), &reached, &got, &found)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))

	if !reached {
		t.Fatal("已登录请求应当继续处理")
	}
	if !found || got.Subject != "u-2" {
		t.Fatalf("Principal = %+v (found=%v), 期望 Subject = %q", got, found, "u-2")
	}
}
