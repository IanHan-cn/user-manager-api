// Package bootstrap 负责在进程启动阶段完成配置、数据库、迁移校验与 Casbin 的装配。
// 这是唯一允许直接依赖 GORM 与 Casbin 具体实现的装配层。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
	"github.com/lmittmann/tint"
	router "github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/handlers"
	"github.com/memories-cn/user-management-api/internal/config"
	"gorm.io/gorm"
)

var _ handlers.ReadinessProbe = (*Bootstrap)(nil)

type Bootstrap struct {
	cfg    config.Config
	db     *gorm.DB
	casbin *casbin.Enforcer
	router *gin.Engine
}

// bootstrap.Build()
// config.Load(), 初始化日志,
// 初始化 PostgreSQL, 初始化 Redis, 初始化 Casbin, 创建 Repository,
// 创建 Application Services, 创建 Gin Router, 启动 HTTP Server
func Build() (*Bootstrap, error) {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// 初始化日志
	setupLogger(cfg)

	// 初始化 PostgreSQL
	db, err := OpenDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// 初始化 Redis

	// 初始化 Casbin
	enforcer, err := NewCasbin(db)
	if err != nil {
		return nil, fmt.Errorf("initialize casbin: %w", err)
	}

	// 创建 Repository

	// 创建 Application Services

	// 创建 Gin Router
	router := router.NewRouter(&app)

	// 启动 HTTP Server
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}
	if err := NewServer(cfg, router); err != nil {
		return nil, fmt.Errorf("start http server: %w", err)
	}
	app := &Bootstrap{
		cfg:    cfg,
		db:     db,
		casbin: enforcer,
		router: router,
	}
	return app, nil

}

// Check 实现了 handlers.ReadinessProbe 接口，用于就绪检查。
func (app *Bootstrap) Check(ctx context.Context) error {
	return errors.New("not ready")
}

// setupLogger 根据运行环境选择结构化日志格式。
func setupLogger(cfg config.Config) {
	opts := &slog.HandlerOptions{Level: cfg.LogLevelSlog()}
	var handler slog.Handler
	if cfg.IsProduction() {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		// handler = slog.NewTextHandler(os.Stdout, opts)
		tOpts := &tint.Options{
			Level: cfg.LogLevelSlog(),
			// TimeFormat: "2006-01-02T15:04:05.999999999Z07:00",
			AddSource: true,
		}
		handler = tint.NewTextHandler(os.Stdout, tOpts)
	}
	slog.SetDefault(slog.New(handler))
}
