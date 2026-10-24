// Package middleware 存放与传输层相关但与业务无关的 Gin 中间件。
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/ports"
)

// RequireAuth 是强制认证中间件：
//   - 已登录：把 Principal 写入请求 context，继续处理；
//   - 未登录：发起认证跳转（携带用户原本想访问的 URI），并中断当前请求链。
//
// 本中间件只依赖 ports.Authenticator 端口，不感知具体认证实现，
// 因此换认证供应商或注入测试替身都不需要改动本文件。
func RequireAuth(a ports.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, err := a.Principal(c.Request)
		// 既要判错，也要判空 Subject：
		// 端口契约允许实现返回 (零值, nil)，
		// 若只判 err，这种"已认证但身份为空"会被误当成放行。
		if err != nil || principal.Subject == "" {
			// 认证失败（无会话、会话过期、身份不完整）都按未登录处理。
			a.Authenticate(c.Writer, c.Request, c.Request.RequestURI)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(ports.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// CheckAuth 是可选认证中间件：有会话就注入 Principal，没有会话也照常放行。
//
// 适用于「登录后体验更好，但未登录同样可访问」的接口。
func CheckAuth(a ports.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if principal, err := a.Principal(c.Request); err == nil && principal.Subject != "" {
			c.Request = c.Request.WithContext(ports.WithPrincipal(c.Request.Context(), principal))
		}
		c.Next()
	}
}

// PrincipalFrom 返回当前请求的调用方身份；未认证时返回零值与 false。
//
// handler 侧推荐用它取身份，而不是直接读 context，
// 这样可以统一「取不到身份」的处理方式。
func PrincipalFrom(c *gin.Context) (ports.Principal, bool) {
	return ports.PrincipalFromContext(c.Request.Context())
}
