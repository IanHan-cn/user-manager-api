package ports

import (
	"context"
	"net/http"
)

// Principal 表示一个已通过认证的调用方身份。
//
// 这里只保留与业务无关的最小信息：认证适配器负责把各身份提供方
// （ZITADEL、OIDC、自建账号体系……）的身份统一收敛成同一个 Principal，
// 上层因此不必再 import 任何第三方认证 SDK。
type Principal struct {
	// Subject 是身份提供方分配的用户唯一标识。
	Subject string
}

// Authenticator 是认证能力的端口定义。
//
// 之所以不复用 zitadel-go 的 *authentication.Authenticator：
//   - 后者是泛型类型（authentication.Authenticator[T Ctx]），
//     泛型参数一旦出现在端口签名里，调用方就被迫依赖具体实现；
//   - 它的 IsAuthenticated 返回类型 T 在具体化之前无法提供用户标识，
//     业务代码必须自己依赖具体的 Context 类型才能拿到身份；
//   - 它的会话上下文使用第三方私有的 context key，
//     读取身份必须 import 第三方包。
//
// 因此端口只暴露三个与业务无关的最小方法：
//   - 嵌入 http.Handler：让认证回调端点（login/callback/logout）
//     能作为普通 handler 挂载到路由上；
//   - Authenticate：发起一次认证跳转，并记住用户原本想访问的 URI；
//   - Principal：查询当前请求的调用方身份。
type Authenticator interface {
	http.Handler

	Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string)
	Principal(r *http.Request) (Principal, error)
}

// principalContextKey 是 Principal 在 context.Context 中的键。
//
// 使用私有的空结构体类型而不是字符串，可以避免与其它包使用的
// 字符串键发生冲突，同时不会让外部包伪造出 Principal。
type principalContextKey struct{}

// WithPrincipal 返回一个携带 Principal 的新 context，原 context 不受影响。
// 认证中间件用它把身份传递给后续的 handler 与用例。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext 从 context 中读取 Principal。
// 未认证、或写入的值不是 Principal 时返回零值与 false。
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}
