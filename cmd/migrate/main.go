// Command migrate 以独立、可审计的步骤执行数据库迁移。
//
// 设计约定：API 服务启动**不执行**迁移，只校验迁移状态，
// 避免多个实例同时执行结构变更。迁移文件通过 go:embed 编入本二进制。
//
// 用法：
//
//	migrate up              应用全部待执行迁移
//	migrate up -steps 1     只向前应用 1 个版本
//	migrate down            回滚全部迁移
//	migrate version         打印当前版本与 dirty 状态
//	migrate force -steps 1  强制把版本标记为 1（仅用于修复 dirty 状态）
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"

	// 注册 PostgreSQL 数据库驱动，缺失会报 "database driver unknown"。
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/memories-cn/user-management-api/internal/config"
	"github.com/memories-cn/user-management-api/migrations"
)

// 支持的迁移命令名称。
const (
	cmdUp      = "up"
	cmdDown    = "down"
	cmdVersion = "version"
	cmdForce   = "force"
)

// commandName 是本二进制的名称，用作 FlagSet 名称与用法提示。
const commandName = "migrate"

func main() {
	opts := parseFlags()

	// 由 run 之外的 main 负责退出码：os.Exit 会跳过 defer，
	// 资源释放必须发生在 run 内部。
	if err := run(opts); err != nil {
		slog.Error("迁移执行失败", "error", err)
		os.Exit(1)
	}
}

// options 是命令行参数的解析结果。
type options struct {
	// cmd 是要执行的迁移命令。
	cmd string
	// steps 是步数，0 表示不限步数；force 命令下表示要写入的版本号。
	steps int
}

// command 描述一条迁移命令的名称、说明与执行逻辑。
type command struct {
	name string
	desc string
	run  func(m *migrate.Migrate, steps int) error
}

// commands 是全部受支持的迁移命令，同时用于生成提示信息与校验入参，
// 新增命令只需在此追加一项。
var commands = []command{
	{name: cmdUp, desc: "应用待执行迁移", run: applyUp},
	{name: cmdDown, desc: "回滚已应用的迁移", run: applyDown},
	{name: cmdVersion, desc: "打印当前版本与 dirty 状态", run: printVersion},
	{name: cmdForce, desc: "强制写入版本号，仅用于修复 dirty 状态", run: forceVersion},
}

// parseFlags 解析命令行参数：命令是首个位置参数，其后可跟 -steps。
//
// flag 包遇到位置参数即停止解析，因此这里分两段：先取首个位置参数作为命令，
// 再解析它后面残留的 flag。这样下面两种写法都能识别：
//
//	migrate up -steps 1
//	migrate -steps 1 up
func parseFlags() options {
	var opts options

	fs := flag.NewFlagSet(commandName, flag.ExitOnError)
	fs.Usage = printUsage
	fs.IntVar(&opts.steps, "steps", 0, "步数，0 表示全部；force 命令表示要写入的版本号")
	// ExitOnError 模式下解析失败会直接退出进程，返回值无意义。
	_ = fs.Parse(os.Args[1:])

	opts.cmd = cmdUp
	if fs.NArg() > 0 {
		opts.cmd = fs.Arg(0)
		_ = fs.Parse(fs.Args()[1:])
	}

	return opts
}

// printUsage 以日志形式输出中文用法，替换 flag 包默认的英文帮助。
func printUsage() {
	slog.Info("用法："+commandName+" <命令> [-steps <步数>]", "默认命令", cmdUp)
	for _, cmd := range commands {
		slog.Info("可用命令", "cmd", cmd.name, "说明", cmd.desc)
	}
}

// run 依次完成：加载配置 → 构建迁移实例 → 执行指定命令。
func run(opts options) error {
	// 迁移只使用 DatabaseDSN，但配置校验是全量执行的，
	// 因此本命令的环境变量要求与 API 服务保持一致。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}

	m, closeMigrator, err := newMigrator(cfg.Database.DSN)
	if err != nil {
		return err
	}
	defer closeMigrator()

	return dispatch(m, opts)
}

// migrateLogger 把 golang-migrate 的内部日志桥接到 slog。
//
// Migrate.Log 默认为 nil，迁移过程中的每个文件都不会留下痕迹；
// 赋值后每执行完一个迁移文件会回调一次 Printf，参数为
// “<版本>/<方向> <文件名> (<耗时>)”，例如 "3/u 000003_create_roles (12ms)"。
type migrateLogger struct {
	// applied 是本轮已执行的文件数，用于体现“跑到了第几个文件”。
	applied int
	// total 是迁移源中的文件总数，用于给出进度参照。
	total int
}

// Printf 实现 migrate.Logger，每个迁移文件执行完成时回调一次。
func (l *migrateLogger) Printf(format string, v ...interface{}) {
	l.applied++
	slog.Info("迁移文件执行完成",
		"本轮第", l.applied,
		"源文件总数", l.total,
		"文件", strings.TrimSpace(fmt.Sprintf(format, v...)),
	)
}

// Verbose 返回 false：关掉调度、缓冲等冗余信息，
// 单个文件的完成记录不受该开关影响，仍会正常输出。
func (*migrateLogger) Verbose() bool { return false }

// countMigrations 遍历迁移源统计版本数量，用于日志里的进度参照。
// 遍历结束（无论是否为 fs.ErrNotExist）都返回已统计到的数量，失败时返回 0。
func countMigrations(src source.Driver) int {
	version, err := src.First()
	if err != nil {
		return 0
	}

	count := 1
	for {
		version, err = src.Next(version)
		if err != nil {
			return count
		}
		count++
	}
}

// newMigrator 基于内嵌迁移文件与给定 DSN 构建迁移实例，
// 返回的 close 函数负责释放迁移源与数据库连接。
func newMigrator(dsn string) (*migrate.Migrate, func(), error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("加载内嵌迁移文件失败：%w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("初始化迁移实例失败：%w", err)
	}
	m.Log = &migrateLogger{total: countMigrations(src)}

	return m, func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Warn("关闭迁移实例失败", "迁移源错误", srcErr, "数据库错误", dbErr)
		}
	}, nil
}

// dispatch 校验命令与步数后执行对应逻辑。
func dispatch(m *migrate.Migrate, opts options) error {
	cmd, ok := lookupCommand(opts.cmd)
	if !ok {
		return fmt.Errorf("未知迁移命令 %q，仅支持：%s", opts.cmd, commandNames())
	}
	if err := validateSteps(cmd.name, opts.steps); err != nil {
		return err
	}

	slog.Info("开始执行迁移命令", "命令", cmd.name, "步数", opts.steps)
	return cmd.run(m, opts.steps)
}

// lookupCommand 按名称查找命令。
func lookupCommand(name string) (command, bool) {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

// validateSteps 校验步数：force 必须显式指定目标版本号，其余命令不接受负数。
func validateSteps(name string, steps int) error {
	switch {
	case name == cmdForce && steps <= 0:
		return fmt.Errorf("命令 %s 必须通过 -steps 指定要写入的版本号（大于 0）", name)
	case name != cmdForce && steps < 0:
		return fmt.Errorf("命令 %s 的步数不能为负数：%d", name, steps)
	}
	return nil
}

// commandNames 返回全部命令名称，用于参数提示与错误提示。
func commandNames() string {
	names := make([]string, 0, len(commands))
	for _, cmd := range commands {
		names = append(names, cmd.name)
	}
	return strings.Join(names, " | ")
}

// applyUp 向前应用迁移，已是最新版本时视为成功。
func applyUp(m *migrate.Migrate, steps int) error {
	err := move(m, steps, true)
	switch {
	case errors.Is(err, migrate.ErrNoChange):
		slog.Info("数据库已是最新版本，没有待执行的迁移", "当前版本", mustVersion(m))
		return nil
	case err != nil:
		return fmt.Errorf("应用迁移失败：%w", err)
	}

	slog.Info("迁移应用成功", "步数", steps, "当前版本", mustVersion(m))
	return nil
}

// applyDown 回滚迁移，已无版本可回滚时视为成功。
func applyDown(m *migrate.Migrate, steps int) error {
	err := move(m, steps, false)
	switch {
	case errors.Is(err, migrate.ErrNoChange):
		slog.Info("数据库没有可回滚的迁移", "当前版本", mustVersion(m))
		return nil
	case err != nil:
		return fmt.Errorf("回滚迁移失败：%w", err)
	}

	slog.Info("迁移回滚成功", "步数", steps, "当前版本", mustVersion(m))
	return nil
}

// mustVersion 读取当前迁移版本用于日志展示，读取失败时退化为 -1，
// 避免因为补充日志信息而中断一次已经执行成功的迁移。
func mustVersion(m *migrate.Migrate) int {
	version, _, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		return 0
	case err != nil:
		slog.Warn("读取迁移版本失败", "error", err)
		return -1
	}
	return int(version)
}

// move 按方向推进迁移，forward 为 true 表示向前应用、false 表示回滚；
// steps 小于等于 0 时一次性推进到底。
func move(m *migrate.Migrate, steps int, forward bool) error {
	if steps <= 0 {
		if forward {
			return m.Up()
		}
		return m.Down()
	}
	if forward {
		return m.Steps(steps)
	}
	return m.Steps(-steps)
}

// printVersion 输出当前迁移版本与 dirty 状态，步数参数不参与执行。
func printVersion(m *migrate.Migrate, _ int) error {
	version, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		slog.Info("当前迁移状态", "版本", "无", "dirty", false)
		return nil
	case err != nil:
		return fmt.Errorf("读取迁移版本失败：%w", err)
	}

	if dirty {
		slog.Warn("迁移处于 dirty 状态，需人工核对后修复", "版本", version)
		return nil
	}
	slog.Info("当前迁移状态", "版本", version, "dirty", dirty)
	return nil
}

// forceVersion 直接写入版本号，不执行任何 SQL，仅用于修复 dirty 状态。
func forceVersion(m *migrate.Migrate, steps int) error {
	if err := m.Force(steps); err != nil {
		return fmt.Errorf("强制写入迁移版本失败：%w", err)
	}

	slog.Warn("已强制写入迁移版本，请确认与数据库实际结构一致", "版本", steps)
	return nil
}
