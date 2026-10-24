// Package zitadel 是 ZITADEL 认证能力的适配器实现。
// 整个仓库中只有本包直接依赖 zitadel-go，
// 对外一律以 ports.Authenticator 端口暴露。
package zitadel

import (
	"context"
	"fmt"
	"net/http"

	"github.com/memories-cn/user-management-api/internal/config"
	"github.com/memories-cn/user-management-api/internal/ports"
	"github.com/redis/go-redis/v9"
	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
)

type zitadelAuthenticator struct {
	authN *authentication.Authenticator[*openid.DefaultContext]
}

// 编译期断言：本适配器必须完整实现 ports.Authenticator 端口。
// 接口方法集一旦变动，这里会立即编译失败，而不是等到运行时才发现。
var _ ports.Authenticator = (*zitadelAuthenticator)(nil)

func New(cfg config.ZitadelConfig, redisClient *redis.Client) (ports.Authenticator, error) {
	ctx := context.Background()
	domain := cfg.Domain
	key := cfg.Key
	clientID := cfg.ClientID
	redirectURI := cfg.RedirectURI

	authN, err := authentication.New(ctx, zitadel.New(domain), key,
		openid.DefaultAuthentication(clientID, redirectURI, key),
		// WithSessionStore 需要的是 authentication.Sessions[*openid.DefaultContext]，
		// 会话类型与上面 DefaultAuthentication 推导出的 Authenticator 类型参数保持一致。
		authentication.WithSessionStore(NewRedisSessionStore[*openid.DefaultContext](redisClient, cfg)),
		// 可选：authentication.WithPostLogoutRedirectURI("http://localhost:8089/"),
		// 可选：authentication.WithCookieSession[*oidc.DefaultContext](), // 无状态会话
		// 可选：authentication.WithExternalSecure(true),                  // 反代 https 后面
	)
	if err != nil {
		return nil, fmt.Errorf("create ZITADEL authenticator: %w", err)
	}
	return &zitadelAuthenticator{authN: authN}, nil
}

func (a *zitadelAuthenticator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.authN.ServeHTTP(w, r)
}

func (a *zitadelAuthenticator) Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string) {
	a.authN.Authenticate(w, r, requestedURI)
}

func (a *zitadelAuthenticator) Principal(r *http.Request) (ports.Principal, error) {
	authCtx, err := a.authN.IsAuthenticated(r)
	if err != nil {
		return ports.Principal{}, fmt.Errorf("check ZITADEL session: %w", err)
	}
	if authCtx == nil {
		return ports.Principal{}, fmt.Errorf("ZITADEL session is empty")
	}

	userInfo := authCtx.GetUserInfo()
	if userInfo == nil || userInfo.GetSubject() == "" {
		return ports.Principal{}, fmt.Errorf("ZITADEL user subject is empty")
	}

	return ports.Principal{Subject: userInfo.GetSubject()}, nil
}
