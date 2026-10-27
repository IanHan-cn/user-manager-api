// Command seed 把代码里定义的权限目录幂等同步进数据库。
//
// 设计约定：表结构由 migrate 命令管理的迁移负责，本命令只写数据。
// 因此它可以在每次部署后重复执行，多次执行的结果与执行一次相同。
//
// 用法：
//
//	seed
//
// 前置条件：数据库已经执行过 migrate up（permissions 表由 000004 创建）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	gormpersist "github.com/memories-cn/user-management-api/internal/adapters/persistence/gorm"
	"github.com/memories-cn/user-management-api/internal/application/permissionseed"
	"github.com/memories-cn/user-management-api/internal/bootstrap"
	"github.com/memories-cn/user-management-api/internal/config"
)

// catalogSyncTimeout 是本次同步的整体超时上限。
//
// 权限目录只有十余项，正常在毫秒级完成；设限是为了防止网络连接异常时
// 命令无限期挂起，让它在 CI 里变成一个可以被发现的问题而不是卡住的流水线。
const catalogSyncTimeout = 30 * time.Second

func main() {
	// 由 run 之外的 main 负责退出码：os.Exit 会跳过 defer，
	// 资源释放必须发生在 run 内部。
	if err := run(); err != nil {
		slog.Error("权限目录同步失败", "error", err)
		os.Exit(1)
	}
}

// run 依次完成：加载配置 → 打开数据库 → 执行同步 → 汇报结果。
func run() error {
	// 与迁移命令一致：本命令只用 DatabaseDSN，
	// 但配置校验是全量执行的，环境变量要求与 API 服务保持一致。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}

	db, err := bootstrap.OpenDB(cfg)
	if err != nil {
		return err
	}
	// 连接必须在同步完成后释放：它们属于操作系统资源，
	// 而进程退出并不会替本命令把池清干净。
	defer func() {
		if err := bootstrap.CloseDB(db); err != nil {
			slog.Warn("关闭数据库连接失败", "error", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), catalogSyncTimeout)
	defer cancel()

	writer, err := gormpersist.NewPermissionWriter(db)
	if err != nil {
		return fmt.Errorf("创建权限目录写入器失败：%w", err)
	}

	seeder, err := permissionseed.NewSeeder(writer)
	if err != nil {
		return fmt.Errorf("创建权限目录同步器失败：%w", err)
	}

	result, err := seeder.EnsureCatalog(ctx)
	if err != nil {
		return fmt.Errorf("同步权限目录失败：%w", err)
	}

	if !result.Changed() {
		slog.Info("权限目录已是最新，本次没有写入任何数据", "目录项", result.Total())
		return nil
	}

	slog.Info("权限目录同步完成",
		"新增", result.Inserted,
		"更新", result.Updated,
		"未变", result.Unchanged,
		"目录项", result.Total(),
	)
	return nil
}
