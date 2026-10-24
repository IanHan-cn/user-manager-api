// Package bootstrap 负责在进程启动阶段完成配置、数据库、Casbin 与 HTTP 路由的装配。
// 这是唯一允许直接依赖 GORM 与 Casbin 具体实现的装配层。
package bootstrap

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/casbin/casbin/v3"
	"github.com/gin-gonic/gin"
	"github.com/lmittmann/tint"
	zitadelauth "github.com/memories-cn/user-management-api/internal/adapters/authentication/zitadel"
	ginrouter "github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin"
	"github.com/memories-cn/user-management-api/internal/config"
	"gorm.io/gorm"
)

// Bootstrap 只持有需要生命周期管理的基础设施资源，
// 不承载用例依赖图（repository / service / handler），
// 避免装配层退化成无所不包的容器。
type Bootstrap struct {
	cfg       config.Config
	db        *gorm.DB
	casbin    *casbin.Enforcer
	router    *gin.Engine
	readiness *ReadinessChecker
}

// Build 按顺序完成：加载配置 → 初始化日志 → 设置 Gin 模式 →
// 打开 PostgreSQL → 初始化 Casbin → 装配依赖图 → 创建 Gin Router。
// Build 只负责装配，不启动服务：进程入口需自行调用 Run 与 Close。
func Build() (*Bootstrap, error) {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// 初始化日志：必须早于其他组件，后续装配过程的日志才会被正确输出。
	setupLogger(cfg)
	// Gin 模式必须在创建 Engine 之前设置。
	setGinMode(cfg)

	// 初始化 PostgreSQL
	db, err := OpenDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// TODO(redis): 待引入缓存/会话需求后在此初始化 Redis 客户端，并纳入 Close 释放。

	// 初始化 Casbin
	enforcer, err := InitializeCasbin(db)
	if err != nil {
		closeDB(db)
		return nil, fmt.Errorf("initialize casbin: %w", err)
	}

	// 就绪探针
	readiness, err := NewReadinessChecker(db, enforcer)
	if err != nil {
		closeDB(db)
		return nil, fmt.Errorf("initialize readiness checker: %w", err)
	}

	// 创建 Repository 与 Application Services。
	// 依赖图在此处组装后只以接口形式流入 HTTP 层，不写入 Bootstrap 字段。
	// TODO(user): 待 ports.Transactor 的 GORM 实现与 user handler 就绪后启用：
	//   userRepo, err := gormpersist.NewUserRepository(db)
	//   if err != nil { closeDB(db); return nil, err }
	//   tx := gormpersist.NewTransactor(db)
	//   userSvc, err := user.NewService(userRepo, tx)
	//   if err != nil { closeDB(db); return nil, err }
	// 用例就绪后在此注入 Deps.Users，具体路由由 router 包注册。

	// 初始化认证适配器：ZITADEL 是当前唯一的认证实现，
	// 对外只以 ports.Authenticator 端口注入。
	auth, err := zitadelauth.New(cfg.Zitadel)
	if err != nil {
		closeDB(db)
		return nil, fmt.Errorf("initialize zitadel authenticator: %w", err)
	}

	// 创建 Gin Router：契约由 router 包声明，这里只负责填充依赖。
	// 路由表由 router 包自己维护，装配层不参与注册。
	router := ginrouter.NewRouter(ginrouter.Deps{
		Readiness: readiness,
		Auth:      auth,
		// Users: userSvc,
	})

	return &Bootstrap{
		cfg:       cfg,
		db:        db,
		casbin:    enforcer,
		router:    router,
		readiness: readiness,
	}, nil
}

// Run 阻塞运行 HTTP 服务，直到收到中断信号并完成优雅关闭才返回。
func (app *Bootstrap) Run() error {
	if err := NewServer(app.cfg, app.router); err != nil {
		return fmt.Errorf("run http server: %w", err)
	}
	return nil
}

// Close 释放数据库连接等需要显式关闭的资源，供进程入口 defer 调用。
func (app *Bootstrap) Close() error {
	var err error
	if app.db != nil {
		err = closeDB(app.db)
	}
	// TODO(redis): Redis 客户端就绪后在此一并关闭。
	if err != nil {
		return err
	}
	slog.Info("resources released")
	return nil
}

// closeDB 关闭数据库底层连接池；装配失败时也用它回收已获取的资源。
func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败：%w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("关闭数据库连接失败：%w", err)
	}
	return nil
}

// setGinMode 根据运行环境设置 Gin 模式，务必在 gin.New() 之前调用。
func setGinMode(cfg config.Config) {
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
		return
	}
	gin.SetMode(gin.DebugMode)
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
