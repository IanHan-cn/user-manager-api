package gin

import (
	"github.com/gin-gonic/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/handlers"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/middleware"
)

func NewRouter(probe handlers.ReadinessProbe) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestID(), middleware.Logging())

	health := handlers.NewHealthHandler(probe)
	r.GET("/health/live", health.Live)
	r.GET("/health/ready", health.Ready)

	return r
}
