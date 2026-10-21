package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
)

func requireAuth[T authentication.Ctx](a *authentication.Authenticator[T]) gin.HandlerFunc {
	return func(c *gin.Context) {
		authCtx, err := a.IsAuthenticated(c.Request)
		if err != nil {
			a.Authenticate(c.Writer, c.Request, c.Request.RequestURI)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(authentication.WithAuthContext(c.Request.Context(), authCtx))
		c.Next()
	}
}

// checkAuth：可选会话，无会话也放行
func checkAuth[T authentication.Ctx](a *authentication.Authenticator[T]) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authCtx, err := a.IsAuthenticated(c.Request); err == nil {
			c.Request = c.Request.WithContext(authentication.WithAuthContext(c.Request.Context(), authCtx))
		}
		c.Next()
	}
}
