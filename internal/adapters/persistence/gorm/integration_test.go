package gorm

// 本文件对本包的 SQL 做端到端校验：起一个真实的 Postgres 容器，
// 用 migrations/ 下那套真实迁移建库，然后让仓储对着它真跑一遍。
//
// 为什么是全真库而不是 SQL 替身：本包最容易出事的东西恰恰由数据库自己定义
// ——部分唯一索引 idx_tenants_slug_active、deleted 一致性 CHECK、
// status 与时间戳的 chk_memberships_status_timestamps、以及 set_updated_at
// 触发器。替身（sqlmock 之类）只能核对"发出去的 SQL 长什么样"，
// 核对不了"这些约束到底有没有按预期生效"，
// 于是"我以为它挡住了"与"它真的挡住了"之间永远隔着一层。
//
// 代价是慢，因此这里的用例刻意在少量而关键：
// 每个用例对应一条写在 migrations/ 注释里、却无法在应用层自证的约定。
// 纯 DTO 转换、错误翻译规则等不依赖数据库的部分仍在
// *_repository_test.go 里，那里快且不需要 Docker。
//
// 运行方式：
//
//	go test ./internal/...                 默认跑（需要可用的 Docker）
//	go test -short ./internal/...          跳过本文件
//
// Docker 不可用时会打印醒目告警并跳过，而不是让整包测试失败：
// 本机能否跑集成用例取决于运行环境，不应把它作为 CI 的硬性前置。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/golang-migrate/migrate/v4"

	// 注册 PostgreSQL 数据库驱动，缺失会报 "database driver unknown"。
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membership"
	"github.com/memories-cn/user-management-api/internal/domain/membershiprole"
	"github.com/memories-cn/user-management-api/internal/domain/permission"
	"github.com/memories-cn/user-management-api/internal/domain/role"
	"github.com/memories-cn/user-management-api/internal/domain/tenant"
	"github.com/memories-cn/user-management-api/migrations"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 集成库所在库连接要用的端到端等待时间。
//
// 首次运行要拉取镜像，比每次容器启动本身久得多，因此给到 2 分钟。
const integrationStartupTimeout = 2 * time.Minute

// integrationDB 是本包全部集成用例共用的连接，由 TestMain 建立。
var integrationDB *gorm.DB

// skipIntegration 非空表示集成环境不可用，各用例据此跳过并给出同一份理由。
var skipIntegration string

// TestMain 起一次容器、建一次库，跑完再拆掉。
//
// 容器只起一次而不是每用例一次：拉镜像与跑迁移是这里的主要开销，
// 让它们摊到整套用例上，单用例的隔离改由每张用例开头的清表来保证。
func TestMain(m *testing.M) {
	// testing.Short 必须等 flag 解析完才有值，TestMain 里这一步得自己来。
	flag.Parse()
	os.Exit(runIntegrationSuite(m))
}

func runIntegrationSuite(m *testing.M) int {
	if testing.Short() {
		skipIntegration = "-short 已跳过集成用例（本文件需要 Docker）"
		return m.Run()
	}

	db, stop, err := startPostgres()
	if err != nil {
		skipIntegration = fmt.Sprintf("启动 Postgres 容器失败：%v", err)

		// CI 上不可跳过：本文件覆盖的正是只有真库能证明的约定——
		// 部分唯一索引、五条 CHECK、set_updated_at 触发器。
		// 跳过它们之后 CI 的结果是 PASS，
		// 一次缺失的基础设施会被当成一次通过，这些约定从此无人看守。
		// 本地没有 Docker 时仍然跳过，不把环境问题变成开发阻塞。
		if os.Getenv("CI") != "" {
			fmt.Fprintf(os.Stderr,
				"\n错误：%s\nCI 环境下集成用例不可跳过：本包依赖真实数据库的约定将无人看守。\n\n",
				skipIntegration)
			return 1
		}

		fmt.Fprintf(os.Stderr,
			"\n告警：%s\n本包 %s 依赖真实数据库的用例将被跳过；"+
				"其余不需要数据库的单元测试照常执行。\n\n",
			skipIntegration, "integration_test.go")
		return m.Run()
	}
	defer stop()

	integrationDB = db
	return m.Run()
}

// startPostgres 启动一个 Postgres 容器，套用真实迁移，返回可用的连接。
//
// TranslateError 必须开： TranslateError 把 23505/23514 翻成
// gorm.ErrDuplicatedKey / ErrCheckConstraintViolated，
// 各仓储的错误翻译才认得出来（见 bootstrap.OpenDB）。少了它，
// 撞约束时会返回一个无从归类的驱动原生错误。
func startPostgres() (*gorm.DB, func(), error) {
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("uma_it"),
		tcpostgres.WithUsername("uma"),
		tcpostgres.WithPassword("uma"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(integrationStartupTimeout),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 Postgres 容器失败：%w", err)
	}

	stop := func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "告警：销毁 Postgres 容器失败：%v\n", err)
		}
	}

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("取连接串失败：%w", err)
	}

	if err := applyMigrations(dsn); err != nil {
		stop()
		return nil, nil, err
	}

	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{DSN: dsn}),
		&gorm.Config{TranslateError: true})
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("打开集成数据库失败：%w", err)
	}
	return db, stop, nil
}

// applyMigrations 把内嵌的迁移文件应用到集成库，与 cmd/migrate 走同一份源。
//
// 刻意走 migrations/ 那份内嵌源，而不是另写一套"够用的建表语句"：
// 集成库的结构必须与线上完全一致，包括 CHECK、部分索引与触发器，
// 少一样都会让本文件存在的意义归零。
func applyMigrations(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("加载内嵌迁移文件失败：%w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("初始化迁移实例失败：%w", err)
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			fmt.Fprintf(os.Stderr, "告警：关闭迁移实例失败：%v / %v\n", srcErr, dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("应用迁移失败：%w", err)
	}
	return nil
}

// integrationFixture 是集成用例的运行环境：一个刚清空的库与各聚合的仓储。
//
// 每个用例开头清一次表：库在同一个容器里被所有用例共用，
// 用例之间不做数据隔离会让某个用例的残留成为另一个用例的隐藏前提。
type integrationFixture struct {
	ctx         context.Context
	tenants     *TenantRepository
	memberships *MembershipRepository
	memberRoles *MembershipRoleRepository
	roles       *RoleRepository
	permissions *PermissionRepository
	raw         *gorm.DB
}

func newIntegrationFixture(t *testing.T) integrationFixture {
	t.Helper()

	if skipIntegration != "" {
		t.Skip(skipIntegration)
	}
	if integrationDB == nil {
		t.Fatal("集成数据库未就绪")
	}

	// 全仓各表一律无外键（见 000008 的注释），因此可以放心一次性清空；
	// 这里只清本文件会碰到的几张表。
	if err := integrationDB.Exec(
		"TRUNCATE TABLE tenants, memberships, membership_roles, roles, permissions",
	).Error; err != nil {
		t.Fatalf("清空集成表失败：%v", err)
	}

	fixture := integrationFixture{ctx: context.Background(), raw: integrationDB}

	repos := []func() error{
		func() (err error) { fixture.tenants, err = NewTenantRepository(integrationDB); return },
		func() (err error) { fixture.memberships, err = NewMembershipRepository(integrationDB); return },
		func() (err error) { fixture.memberRoles, err = NewMembershipRoleRepository(integrationDB); return },
		func() (err error) { fixture.roles, err = NewRoleRepository(integrationDB); return },
		func() (err error) { fixture.permissions, err = NewPermissionRepository(integrationDB); return },
	}
	for _, build := range repos {
		if err := build(); err != nil {
			t.Fatalf("构造仓储失败：%v", err)
		}
	}
	return fixture
}

// mustCreateTenant 建一个租户并把落库结果返回，失败即终止用例。
func (f integrationFixture) mustCreateTenant(t *testing.T, slug string) *tenant.Tenant {
	t.Helper()

	tn, err := tenant.NewSystemTenant(slug, slug+"公司")
	if err != nil {
		t.Fatalf("构造租户失败：%v", err)
	}
	if err := f.tenants.Create(f.ctx, tn); err != nil {
		t.Fatalf("写入租户失败：%v", err)
	}
	return tn
}

// Test租户软删除释放slug且GetByID仍能取回
//
// 这一条同时钉住三件事，缺一件就会错：
//   - GetBySlug 自带 deleted_at IS NULL —— 否则软删除后还会查到墓碑行；
//   - GetByID 不带软删除条件 —— 否则"删除后回看"无从实现；
//   - idx_tenants_slug_active 是部分索引 —— 否则同一个 slug 无法在删除后复用。
//
// 三者都是 migrations/000006 写在注释里的约定，只有真库能证明它们生效。
func Test租户软删除释放slug且GetByID仍能取回(t *testing.T) {
	f := newIntegrationFixture(t)

	tn := f.mustCreateTenant(t, "acme")

	got, err := f.tenants.GetBySlug(f.ctx, "acme")
	if err != nil || got == nil || got.ID != tn.ID {
		t.Fatalf("未删除时应能按 slug 查到，实际 (%+v, %v)", got, err)
	}

	// 软删除：status 与 deleted_at 必须成对翻转，
	// 只改一个会被 chk_tenants_deleted_consistency 拒绝。
	now := time.Now()
	tn.Status = tenant.TenantStatusDeleted
	tn.DeletedAt = now
	if err := f.tenants.Update(f.ctx, tn); err != nil {
		t.Fatalf("软删除失败：%v", err)
	}

	// slug 已释放：同一租户不再能通过 slug 查到。
	if got, err := f.tenants.GetBySlug(f.ctx, "acme"); got != nil || err != nil {
		t.Fatalf("软删除后 GetBySlug 应返回 (nil, nil)，实际 (%+v, %v)", got, err)
	}

	// 但按主键仍能回看这条租户。
	got, err = f.tenants.GetByID(f.ctx, tn.ID)
	if err != nil || got == nil || got.Status != tenant.TenantStatusDeleted {
		t.Fatalf("GetByID 应能取回已删除的租户，实际 (%+v, %v)", got, err)
	}

	// slug 真正释放：可以用同一个 slug 再建一个租户。
	reborn := f.mustCreateTenant(t, "acme")
	got, err = f.tenants.GetBySlug(f.ctx, "acme")
	if err != nil {
		t.Fatalf("复用 slug 后查询失败：%v", err)
	}
	if got == nil || got.ID != reborn.ID {
		t.Fatalf("slug 复用后应查到新租户，实际 %+v", got)
	}
}

// Test租户slug重复返回ErrTenantExists
//
// 23P05 由 idx_tenants_slug_active 触发，经 TranslateError 翻成
// gorm.ErrDuplicatedKey，再由 translateTenantWriteError 认领。
// 整条链只有真库能连起来，缺了 TranslateError 就会退化成不可归类的原生错误。
func Test租户slug重复返回ErrTenantExists(t *testing.T) {
	f := newIntegrationFixture(t)
	f.mustCreateTenant(t, "acme")

	dup, err := tenant.NewSystemTenant("acme", "同名的另一家")
	if err != nil {
		t.Fatalf("构造租户失败：%v", err)
	}

	err = f.tenants.Create(f.ctx, dup)
	if !errors.Is(err, tenant.ErrTenantExists) {
		t.Fatalf("err = %v，期望 ErrTenantExists", err)
	}
	if errors.Is(err, tenant.ErrInvalidTenantRecord) {
		t.Fatalf("err = %v，不应归类到记录不合法", err)
	}
}

// Test租户状态与软删除时间不同步被数据库拒绝
//
// 应用层本不应当产出这种组合，这里是最后一道防线：
// 真库必须把它挡回来，而且要以可归类的 ErrInvalidTenantRecord 挡回来。
func Test租户状态与软删除时间不同步被数据库拒绝(t *testing.T) {
	f := newIntegrationFixture(t)
	tn := f.mustCreateTenant(t, "acme")

	// 只翻 status、不动 deleted_at：chk_tenants_deleted_consistency 应拒绝。
	tn.Status = tenant.TenantStatusDeleted

	err := f.tenants.Update(f.ctx, tn)
	if !errors.Is(err, tenant.ErrInvalidTenantRecord) {
		t.Fatalf("err = %v，期望被 CHECK 拒绝并归入 ErrInvalidTenantRecord", err)
	}

	// 被拒绝的行必须原样留在库里，不能变成半删除状态。
	got, err := f.tenants.GetByID(f.ctx, tn.ID)
	if err != nil || got == nil {
		t.Fatalf("被拒绝的更新不应影响库中的行：%v", err)
	}
	if got.Status != tenant.TenantStatusActive || !got.DeletedAt.IsZero() {
		t.Fatalf("更新被拒后 status 与 deleted_at 应保持原值，实际 %+v", got)
	}
}

// Test租户Update不存在的行返回记录不存在
func Test租户Update不存在的行返回记录不存在(t *testing.T) {
	f := newIntegrationFixture(t)
	tn := f.mustCreateTenant(t, "acme")

	// 换成一个库里没有的主键：Update 影响 0 行，约定返回 ErrTenantNotFound。
	tn.ID = uuid.NewV7().String()
	err := f.tenants.Update(f.ctx, tn)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("err = %v，期望 ErrTenantNotFound", err)
	}
}

// Test租户的更新时间由触发器维护
//
// trg_tenants_set_updated_at 独占这一列：应用层给出了值也会被触发器覆盖。
// 这里先睡一小会儿再改，是为了让新旧值有可比较的先后关系。
func Test租户的更新时间由触发器维护(t *testing.T) {
	f := newIntegrationFixture(t)
	tn := f.mustCreateTenant(t, "acme")

	before, err := f.tenants.GetByID(f.ctx, tn.ID)
	if err != nil || before == nil {
		t.Fatalf("取回租户失败：%v", err)
	}

	time.Sleep(20 * time.Millisecond)
	before.Name = "改过的名字"
	if err := f.tenants.Update(f.ctx, before); err != nil {
		t.Fatalf("更新租户失败：%v", err)
	}

	// 内存里的实体不带最新的 updated_at（Convert 应用层不写这一列），
	// 必须重新查一次才能看到触发器的成果。
	after, err := f.tenants.GetByID(f.ctx, tn.ID)
	if err != nil || after == nil {
		t.Fatalf("取回租户失败：%v", err)
	}
	if after.Name != "改过的名字" {
		t.Fatalf("业务字段未更新：%+v", after)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at 未被触发器推进：%v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// Test成员关系Update不得改到别的租户的行
//
// Update 的 WHERE 带着 tenant_id，这是端口"任何写入都必须以 tenant_id 为条件"
// 的唯一落地。这里让调用方把关系伪装到另一个租户上真写一次：
// 数据库必须一所无获，而不是去改别人的行。
func Test成员关系Update不得改到别的租户的行(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantA, tenantB, user := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	m, err := membership.NewSystem(tenantA, user)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	if err := f.memberships.Create(f.ctx, m); err != nil {
		t.Fatalf("写入成员关系失败：%v", err)
	}

	// 同时让另一个租户也有一条成员关系，确认写入不会串到它身上。
	mB, err := membership.NewSystem(tenantB, user)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	if err := f.memberships.Create(f.ctx, mB); err != nil {
		t.Fatalf("写入成员关系失败：%v", err)
	}

	// 假装这条关系属于租户 B，再走一次全量更新。
	now := time.Now()
	if err := m.Activate(now); err != nil {
		t.Fatalf("激活成员关系失败：%v", err)
	}
	m.TenantID = tenantB.String()

	err = f.memberships.Update(f.ctx, m)
	if !errors.Is(err, membership.ErrMembershipNotFound) {
		t.Fatalf("err = %v，期望因租户条件不匹配而记为未命中", err)
	}

	// 库中两行都必须原样不动：A 的仍是 invited，B 的也仍是 invited。
	for id, assertion := range map[uuid.UUID]string{
		tenantA: "原行不应被改动",
		tenantB: "别的租户的行更不应被改动",
	} {
		got, err := f.memberships.GetByTenantAndUser(f.ctx, id, user)
		if err != nil || got == nil {
			t.Fatalf("%s：取回失败 %v", assertion, err)
		}
		if got.Status != membership.StatusInvited {
			t.Fatalf("%s：实际状态 %q", assertion, got.Status)
		}
	}
}

// Test成员关系重复加入同一租户返回已存在
//
// idx_memberships_tenant_user 是全表唯一而不是"只覆盖未离开成员"的部分索引：
// 离开 ≠ 释放，重新加入必须复用同一行（走 Reinvite），再插一条就得被挡住。
func Test成员关系重复加入同一租户返回已存在(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID, userID := uuid.NewV7(), uuid.NewV7()
	first, err := membership.NewSystem(tenantID, userID)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	if err := f.memberships.Create(f.ctx, first); err != nil {
		t.Fatalf("写入成员关系失败：%v", err)
	}

	second, err := membership.NewSystem(tenantID, userID)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	err = f.memberships.Create(f.ctx, second)
	if !errors.Is(err, membership.ErrMembershipExists) {
		t.Fatalf("err = %v，期望 ErrMembershipExists", err)
	}
}

// Test成员关系状态与时间戳不自洽被数据库拒绝
//
// 应用层由 Activate / Suspend / Leave 守护状态与业务时间戳的一致性，
// 这里是数据库侧的兜底：绕过应用层直接把 status 改成 active 而 JoinedAt 为空
// 时，chk_memberships_status_timestamps 必须拒绝。
func Test成员关系状态与时间戳不自洽被数据库拒绝(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID, userID := uuid.NewV7(), uuid.NewV7()
	m, err := membership.NewSystem(tenantID, userID)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	if err := f.memberships.Create(f.ctx, m); err != nil {
		t.Fatalf("写入成员关系失败：%v", err)
	}

	// 绕过 Activate 直接翻状态：joined_at 仍为空，应被 CHECK 拒绝。
	m.Status = membership.StatusActive

	err = f.memberships.Update(f.ctx, m)
	if !errors.Is(err, membership.ErrInvalidMembershipRecord) {
		t.Fatalf("err = %v，期望被 CHECK 拒绝并归入 ErrInvalidMembershipRecord", err)
	}

	got, err := f.memberships.GetByTenantAndUser(f.ctx, tenantID, userID)
	if err != nil || got == nil {
		t.Fatalf("被拒绝的更新不应影响库中的行：%v", err)
	}
	if got.Status != membership.StatusInvited || !got.JoinedAt.IsZero() {
		t.Fatalf("更新被拒后状态与加入时间应保持原值，实际 %+v", got)
	}
}

// Test成员关系列表按租户隔离并按固定次序返回
//
// 同时覆盖三件小事：列表方法带回的子行属于指定租户、
// GetByUserID 是唯一允许跨租户的查询、以及 created_at + id 的排序口径。
func Test成员关系列表按租户隔离并按固定次序返回(t *testing.T) {
	f := newIntegrationFixture(t)

	// (tenant_id, user_id) 全表唯一，因此同一个租户要凑两条只能用两个不同的用户。
	tenantA, tenantB := uuid.NewV7(), uuid.NewV7()
	user1, user2 := uuid.NewV7(), uuid.NewV7()
	for _, pair := range []struct{ tenant, user uuid.UUID }{
		{tenantA, user1}, {tenantA, user2}, {tenantB, user1},
	} {
		m, err := membership.NewSystem(pair.tenant, pair.user)
		if err != nil {
			t.Fatalf("构造成员关系失败：%v", err)
		}
		if err := f.memberships.Create(f.ctx, m); err != nil {
			t.Fatalf("写入成员关系失败：%v", err)
		}
	}

	byTenant, err := f.memberships.GetByTenantID(f.ctx, tenantA)
	if err != nil {
		t.Fatalf("按租户查询失败：%v", err)
	}
	if len(byTenant) != 2 {
		t.Fatalf("GetByTenantID 应只返回本租户的 2 条，实际 %d", len(byTenant))
	}
	for _, m := range byTenant {
		if m.TenantID != tenantA.String() {
			t.Fatalf("GetByTenantID 串出了别的租户的数据：%+v", m)
		}
	}

	// created_at 相同时靠 id 兜底，顺序必须在两次调用之间保持稳定。
	for i := 1; i < len(byTenant); i++ {
		prev, cur := byTenant[i-1], byTenant[i]
		if cur.CreatedAt.Before(prev.CreatedAt) || cur.ID < prev.ID {
			t.Fatalf("列表未按 created_at, id 升序返回：%+v", byTenant)
		}
	}

	// GetByUserID 是唯一允许跨租户的查询：user1 在两个租户各有一条。
	byUser, err := f.memberships.GetByUserID(f.ctx, user1)
	if err != nil {
		t.Fatalf("按用户查询失败：%v", err)
	}
	if len(byUser) != 2 {
		t.Fatalf("GetByUserID 应跨租户返回 user1 的 2 条，实际 %d", len(byUser))
	}
}

// Test权限批量查询按入参顺序还原且缺项报错
//
// 顺序一致是端口明写的契约：调用方按位置对应自己传入的语义。
// 这里让库里的行顺序与入参相反，逼 orderPermissionsByCodes 真的被调用。
func Test权限批量查询按入参顺序还原且缺项报错(t *testing.T) {
	f := newIntegrationFixture(t)

	// 逆序写入，确保"查回的顺序"与"库的物理顺序"不是同一件事。
	rows := []permissionRecord{
		newFixturePermission("tenant:write"),
		newFixturePermission("tenant:read"),
	}
	if err := f.raw.Create(&rows).Error; err != nil {
		t.Fatalf("写入权限目录失败：%v", err)
	}

	got, err := f.permissions.FindByCodes(f.ctx, []string{"Tenant:Read", "tenant:write"})
	if err != nil {
		t.Fatalf("批量查询失败：%v", err)
	}
	if len(got) != 2 || got[0].Code != "tenant:read" || got[1].Code != "tenant:write" {
		t.Fatalf("结果未按入参顺序还原：%+v", got)
	}

	// 缺项必须报出来：静默少返回一项会让调用方以为权限齐了。
	if _, err := f.permissions.FindByCodes(f.ctx, []string{"tenant:read", "tenant:delete"}); !errors.Is(err, permission.ErrUnknownCode) {
		t.Fatalf("err = %v，期望 ErrUnknownCode", err)
	}
}

// newFixturePermission 造一条权限行，落库时间与主键由这里给出。
func newFixturePermission(code string) permissionRecord {
	resource, action, err := permission.ParseCode(code)
	if err != nil {
		panic("测试用例写死了非法权限代码：" + err.Error())
	}
	return permissionRecord{
		ID:        uuid.NewV7().String(),
		Code:      permission.BuildCode(resource, action),
		Resource:  resource,
		Action:    action,
		CreatedAt: time.Now(),
	}
}

// mustCreateMembership 建一条成员关系并落库，返回它的主键，失败即终止用例。
//
// 授权行没有外键兜底，但成员关系这一侧仍然真实建出来：
// 集成库里的 membership_id 应当指向一条真实存在的成员关系，
// 否则本文件的用例就与线上形态脱节了。
func (f integrationFixture) mustCreateMembership(t *testing.T, tenantID, userID uuid.UUID) uuid.UUID {
	t.Helper()

	m, err := membership.NewSystem(tenantID, userID)
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	if err := f.memberships.Create(f.ctx, m); err != nil {
		t.Fatalf("写入成员关系失败：%v", err)
	}
	return mustParseUUID(t, m.ID)
}

// mustCreateRole 建一个系统预置角色并落库，返回它的主键，失败即终止用例。
func (f integrationFixture) mustCreateRole(t *testing.T, tenantID uuid.UUID, key string) uuid.UUID {
	t.Helper()

	r, err := role.NewSystemRole(tenantID, key, key+"角色", "")
	if err != nil {
		t.Fatalf("构造角色失败：%v", err)
	}
	if err := f.roles.Create(f.ctx, r); err != nil {
		t.Fatalf("写入角色失败：%v", err)
	}
	return mustParseUUID(t, r.ID)
}

// mustGrantRole 以系统身份把角色授予指定成员关系并落库，失败即终止用例。
// expiresAt 为零值表示长期有效。
func (f integrationFixture) mustGrantRole(
	t *testing.T, tenantID, membershipID, roleID uuid.UUID, expiresAt time.Time,
) *membershiprole.MembershipRole {
	t.Helper()

	grant, err := membershiprole.NewSystem(tenantID, membershipID, roleID, time.Now(), expiresAt, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, grant); err != nil {
		t.Fatalf("写入授权失败：%v", err)
	}
	return grant
}

// mustParseUUID 解析实体主键，供需要 uuid.UUID 的仓储方法使用。
func mustParseUUID(t *testing.T, id string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("解析主键 %q 失败：%v", id, err)
	}
	return parsed
}

// Test成员角色重复授权同一角色返回已存在且撤销后可重新分配
//
// 这一条同时钉住三件事，缺一件就会错：
//   - idx_membership_roles_membership_role_active 是部分索引 ——
//     未失效的授权撞约束必须被挡住并翻译成 ErrMembershipRoleExists；
//   - 撤销之后的旧行不再算作"未失效" —— 同一个角色可以被重新分配；
//   - 重新分配是新插一行而不是复活旧行 —— 历史行与其撤销原因必须原样保留。
//
// 三者都是 migrations/000009 写在注释里的约定，只有真库能证明它们生效。
func Test成员角色重复授权同一角色返回已存在且撤销后可重新分配(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	first := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})

	// 未失效的授权重复插入：撞上部分唯一索引。
	dup, err := membershiprole.NewSystem(tenantID, membershipID, roleID, time.Now(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, dup); !errors.Is(err, membershiprole.ErrMembershipRoleExists) {
		t.Fatalf("err = %v，期望 ErrMembershipRoleExists", err)
	}

	// 撤销：旧行转为 revoked，从此不再占用这对取值。
	if err := first.Revoke("误授权"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if err := f.memberRoles.Update(f.ctx, first); err != nil {
		t.Fatalf("落库撤销失败：%v", err)
	}
	// 同一次业务判断里的多个查询用同一个 now：
	// "能不能重新分配"与随后的写入必须按同一时刻判定。
	now := time.Now()
	if got, err := f.memberRoles.GetByMembershipAndRole(f.ctx, tenantID, membershipID, roleID, now); got != nil || err != nil {
		t.Fatalf("撤销后不应再查到未失效的授权，实际 (%+v, %v)", got, err)
	}

	// 重新分配：新插一行，成功而不是撞约束。
	second := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})

	// 历史行与其撤销原因必须原样保留。
	all, err := f.memberRoles.ListByMembershipID(f.ctx, tenantID, membershipID)
	if err != nil {
		t.Fatalf("查询授权列表失败：%v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应有 2 行（旧的历史行 + 新的生效行），实际 %d", len(all))
	}
	if all[0].Status != membershiprole.StatusRevoked || all[0].Reason != "误授权" {
		t.Fatalf("历史行被改写：%+v", all[0])
	}
	if all[1].Status != membershiprole.StatusActive || all[1].ID != second.ID {
		t.Fatalf("新行应为生效状态且排在后面：%+v", all[1])
	}

	// 当前生效的那条要能被单独取回。
	got, err := f.memberRoles.GetByMembershipAndRole(f.ctx, tenantID, membershipID, roleID, now)
	if err != nil || got == nil || got.ID != second.ID {
		t.Fatalf("应取回重新分配的那条，实际 (%+v, %v)", got, err)
	}
}

// Test成员角色失效时间不晚于生效时间被数据库拒绝
//
// 应用层由构造函数与 Approve 守护这一条，这里是数据库侧的兜底：
// 绕过它们直接把 expires_at 改成等于 effective_at 时，
// chk_membership_roles_expires_after_effective 必须拒绝。
//
// 拒绝时给出的错误必须与领域层同一口径（ErrInvalidEffectiveRange）：
// 同一个非法取值从两条路径进来若给出不同的错误，
// 上层按基类分流就会走到不同分支——这是按约束名分流存在的理由。
func Test成员角色失效时间不晚于生效时间被数据库拒绝(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})

	// 等于生效时间即"从未成立过"：数据库必须挡住。
	grant.ExpiresAt = grant.EffectiveAt

	err := f.memberRoles.Update(f.ctx, grant)
	if !errors.Is(err, membershiprole.ErrInvalidEffectiveRange) {
		t.Fatalf("err = %v，期望被 CHECK 拒绝并归入 ErrInvalidEffectiveRange", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
	}

	got, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || got == nil {
		t.Fatalf("被拒绝的更新不应影响库中的行：%v", err)
	}
	if !got.ExpiresAt.IsZero() {
		t.Fatalf("更新被拒后 expires_at 应保持为 NULL，实际 %v", got.ExpiresAt)
	}
}

// Test成员角色Update不得改到别的租户的行
//
// Update 的 WHERE 带着 tenant_id，这是端口"任何写入都必须以 tenant_id 为条件"
// 的唯一落地。这里让调用方把授权伪装到另一个租户上真写一次：
// 数据库必须一所无获，而不是去改别人的行。
func Test成员角色Update不得改到别的租户的行(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantA, tenantB := uuid.NewV7(), uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantA, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantA, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantA, membershipID, roleID, time.Time{})

	if err := grant.Revoke("不再需要"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	// 假装这条授权属于租户 B，再走一次更新。
	grant.TenantID = tenantB.String()

	err := f.memberRoles.Update(f.ctx, grant)
	if !errors.Is(err, membershiprole.ErrMembershipRoleNotFound) {
		t.Fatalf("err = %v，期望因租户条件不匹配而记为未命中", err)
	}

	// 原行必须原样不动：仍在本租户、仍是 active。
	got, err := f.memberRoles.GetByMembershipAndRole(f.ctx, tenantA, membershipID, roleID, time.Now())
	if err != nil || got == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if got.Status != membershiprole.StatusActive {
		t.Fatalf("别的租户的写入不应改动本行，实际状态 %q", got.Status)
	}
}

// Test到期授权按到期先后被扫描且失效后不再重复出现
//
// 这一条覆盖 ListDueForExpiry 的三件事：
//   - 长期有效（expires_at 为 NULL）与尚未到期的行都不该被扫出来；
//   - 到期越久的越靠前（expires_at、id 升序），便于分批与断点续跑；
//   - 推进为 expired 之后不再重复出现（状态条件把它排除在外）。
func Test到期授权按到期先后被扫描且失效后不再重复出现(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	keys := []string{role.KeyOwner, role.KeyAdmin, role.KeyMember, role.KeyViewer}

	// 长期有效：expires_at 为 NULL，永远不该被扫出来。
	f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, keys[0]), time.Time{})
	// 尚未到期：到期时间在未来。
	f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, keys[1]), time.Now().Add(time.Hour))

	// 两行已到期：构造函数拒收过去的到期时间，因此先按未来建行，
	// 再用裸 SQL 把 effective_at / expires_at 一起挪到过去
	// （仍满足 expires_at > effective_at，不会撞 CHECK）。
	dueLate := f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, keys[2]), time.Now().Add(time.Hour))
	dueEarly := f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, keys[3]), time.Now().Add(time.Hour))
	for _, tc := range []struct {
		id        string
		offset    string
		effective string
	}{
		{dueLate.ID, "1 hour", "2 hours"},
		{dueEarly.ID, "3 hours", "4 hours"},
	} {
		if err := f.raw.Exec(
			"UPDATE membership_roles SET expires_at = now() - interval '"+tc.offset+
				"', effective_at = now() - interval '"+tc.effective+"' WHERE id = ?",
			tc.id,
		).Error; err != nil {
			t.Fatalf("把授权挪到过去失败：%v", err)
		}
	}

	now := time.Now()
	due, err := f.memberRoles.ListDueForExpiry(f.ctx, tenantID, now, 100)
	if err != nil {
		t.Fatalf("扫描到期授权失败：%v", err)
	}
	if len(due) != 2 {
		t.Fatalf("只应扫出已到期的 2 条，实际 %d", len(due))
	}
	// 到期越久的越靠前。
	if due[0].ID != dueEarly.ID || due[1].ID != dueLate.ID {
		t.Fatalf("未按 expires_at 升序返回：%s, %s", due[0].ID, due[1].ID)
	}

	// 逐个推进：now 与扫描时同一个值，两次判定必须一致。
	for _, grant := range due {
		if err := grant.Expire(now); err != nil {
			t.Fatalf("判为到期失败：%v", err)
		}
		if err := f.memberRoles.Update(f.ctx, grant); err != nil {
			t.Fatalf("落库到期状态失败：%v", err)
		}
	}

	again, err := f.memberRoles.ListDueForExpiry(f.ctx, tenantID, time.Now(), 100)
	if err != nil {
		t.Fatalf("再次扫描失败：%v", err)
	}
	if len(again) != 0 {
		t.Fatalf("推进为 expired 后不应再被扫出，实际 %d 条", len(again))
	}

	// 已到期的授权不再算作"未失效"。
	got, err := f.memberRoles.GetByMembershipAndRole(
		f.ctx, tenantID, membershipID, mustParseUUID(t, dueLate.RoleID), time.Now())
	if got != nil || err != nil {
		t.Fatalf("到期后不应再查到未失效的授权，实际 (%+v, %v)", got, err)
	}
}

// Test成员角色的更新时间由触发器维护
//
// trg_membership_roles_set_updated_at 独占这一列：应用层给出了值也会被触发器覆盖。
// 这里先睡一小会儿再改，是为了让新旧值有可比较的先后关系。
func Test成员角色的更新时间由触发器维护(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})

	before, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || before == nil {
		t.Fatalf("取回授权失败：%v", err)
	}

	time.Sleep(20 * time.Millisecond)
	if err := before.Revoke("离职"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if err := f.memberRoles.Update(f.ctx, before); err != nil {
		t.Fatalf("落库撤销失败：%v", err)
	}

	// 内存里的实体不带最新的 updated_at（应用层不写这一列），
	// 必须重新查一次才能看到触发器的成果。
	after, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || after == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if after.Status != membershiprole.StatusRevoked || after.Reason != "离职" {
		t.Fatalf("业务字段未更新：%+v", after)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at 未被触发器推进：%v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// Test成员角色乐观锁版本号冲突
//
// 验证基于整型 version 的乐观锁：
// 当管理员 A 持有旧版本（version=1）尝试更新时，若记录已被管理员 B 并发修改（version 已变为 2），
// 仓储层必须拒绝静默覆盖，并准确抛出 ErrMembershipRoleConflict。
//
// 这里让 B 只推进版本号而不改状态（行仍是 active）：
// 冲突与终态是两种不同的结局，分开验证才不会互相掩盖——
// 详见同文件的"终态授权不可被复活"。
func Test成员角色乐观锁版本号冲突(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	// 1. 初始创建一个授权（初始 version 应为 1）
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})
	if grant.Version != 1 {
		t.Fatalf("初始 version 应为 1，实际 %d", grant.Version)
	}

	// 2. 模拟管理员 A 读取并持有一份内存副本（携带 version = 1）
	replicaA, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil {
		t.Fatalf("取回授权失败：%v", err)
	}

	// 3. 模拟管理员 B 先一步修改了这条记录（version 在库中自增为 2，行仍是 active）。
	//    用裸 SQL 只推版本号、不碰状态，是为了把"版本号对不上"这一种结局单独拎出来。
	if err := f.raw.Exec(
		"UPDATE membership_roles SET version = version + 1 WHERE id = ?", grant.ID,
	).Error; err != nil {
		t.Fatalf("推进版本号失败：%v", err)
	}

	// 4. 管理员 A 尝试基于其手中的陈旧副本（version = 1）进行 Update
	// 因为 replicaA 里的 Version 已经落后于数据库当前的 Version (2)，
	// WHERE 条件无法命中，仓储层应识别并发冲突并准确抛出 ErrMembershipRoleConflict。
	err = f.memberRoles.Update(f.ctx, replicaA)
	if !errors.Is(err, membershiprole.ErrMembershipRoleConflict) {
		t.Fatalf("err = %v，期望触发并发冲突 ErrMembershipRoleConflict", err)
	}
}

// Test终态授权不可被复活
//
// 状态机（canTransition）只守护 Approve / Revoke / Expire 三个领域方法，
// 管不到"读入一条 revoked 的行、把导出的 Status 字段改成 active 再 Update"
// 这条路径。少了 Update 的终态守卫，一次已撤销的授权就能被静默复活，
// 并立刻出现在鉴权查询结果里——这是权限提升。
//
// 这里显式复现那个攻击动作：取回 revoked 的行、改成 active、落库。
func Test终态授权不可被复活(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})
	if err := grant.Revoke("管理员合规撤销"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if err := f.memberRoles.Update(f.ctx, grant); err != nil {
		t.Fatalf("落库撤销失败：%v", err)
	}

	// 攻击者取回这条已撤销的行，直接把状态改成 active 再落库。
	revoked, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || revoked == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if revoked.Status != membershiprole.StatusRevoked {
		t.Fatalf("前置条件不成立：状态应为 revoked，实际 %q", revoked.Status)
	}
	revoked.Status = membershiprole.StatusActive

	err = f.memberRoles.Update(f.ctx, revoked)
	if !errors.Is(err, membershiprole.ErrInvalidStatusTransition) {
		t.Fatalf("err = %v，期望 ErrInvalidStatusTransition", err)
	}
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidState", err)
	}
	// 不能报成冲突：那会让调用方"重新读取再重试"，
	// 而终态行重试多少次都不会成功。
	if errors.Is(err, membershiprole.ErrMembershipRoleConflict) {
		t.Fatalf("err = %v，终态不应被误报成并发冲突", err)
	}

	// 库里的那一行必须仍是 revoked：一次被拒绝的写入不能留下任何痕迹。
	after, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || after == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if after.Status != membershiprole.StatusRevoked {
		t.Fatalf("被拒绝的写入不应改变状态，实际 %q", after.Status)
	}
}

// Test已到期未推进的授权不参与鉴权也不挡住重新分配
//
// expired 只能由定时任务推进，而定时任务尚未落地：
// 一条过了 expires_at 但状态仍是 active 的行，在库里与"仍然生效"无法区分。
// 这一条锁住两个后果：
//   - 鉴权侧不得把它算作生效（GetByMembershipAndRole 与 ListActiveByMembershipID
//     都要排除它），否则就是一个真实的越权窗口；
//   - 重新分配不得被它挡住：Create 必须先把它推进为 expired，
//     否则它会一直占着部分唯一索引的槽位，让新分配撞 23505。
func Test已到期未推进的授权不参与鉴权也不挡住重新分配(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	// 先按未来到期建行，再用裸 SQL 把 effective_at / expires_at 一起挪到过去：
	// 构造函数拒收过去的到期时间，而这里要的就是"已经到期但状态还没推进"的那一行。
	// 仍然满足 expires_at > effective_at，不会撞 CHECK。
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Now().Add(time.Hour))
	if err := f.raw.Exec(
		"UPDATE membership_roles SET expires_at = now() - interval '1 hour',"+
			" effective_at = now() - interval '2 hours' WHERE id = ?",
		grant.ID,
	).Error; err != nil {
		t.Fatalf("把授权挪到过去失败：%v", err)
	}

	now := time.Now()

	// 一、鉴权侧不得把它算作生效。
	got, err := f.memberRoles.GetByMembershipAndRole(f.ctx, tenantID, membershipID, roleID, now)
	if got != nil || err != nil {
		t.Fatalf("已到期的授权不应被当作未失效，实际 (%+v, %v)", got, err)
	}

	active, err := f.memberRoles.ListActiveByMembershipID(f.ctx, tenantID, membershipID, now)
	if err != nil {
		t.Fatalf("查询生效授权失败：%v", err)
	}
	if len(active) != 0 {
		t.Fatalf("已到期的授权不应出现在生效列表里，实际 %d 条", len(active))
	}

	// 二、重新分配不能被它挡住：Create 先推进再插入。
	regrant, err := membershiprole.NewSystem(tenantID, membershipID, roleID, time.Now(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, regrant); err != nil {
		t.Fatalf("重新分配应先推进到期行再插入，实际 %v", err)
	}

	// 旧行必须被推进为 expired，而不是留在 active 上继续占着唯一槽位。
	old, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || old == nil {
		t.Fatalf("取回旧行失败：%v", err)
	}
	if old.Status != membershiprole.StatusExpired {
		t.Fatalf("旧行应被推进为 expired，实际 %q", old.Status)
	}
	// 推进是一次真实的写：版本号必须反映出来，
	// 否则持有旧副本的调用方会拿着同一个版本号覆盖掉这次推进。
	if old.Version != grant.Version+1 {
		t.Fatalf("推进后版本号 = %d，期望 %d", old.Version, grant.Version+1)
	}

	// 新行才是当前生效的那一条。
	got, err = f.memberRoles.GetByMembershipAndRole(f.ctx, tenantID, membershipID, roleID, now)
	if err != nil || got == nil || got.ID != regrant.ID {
		t.Fatalf("应取回重新分配的那条，实际 (%+v, %v)", got, err)
	}
}

// Test未到期的授权仍然占用唯一约束
//
// 这是上面那条的对照：写路径的"先推进"只针对真正过期的行。
// 尚未到期的授权必须照旧挡住重复分配——
// 否则"到期推进"就变成了"绕过唯一性"。
func Test未到期的授权仍然占用唯一约束(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Now().Add(time.Hour))

	dup, err := membershiprole.NewSystem(tenantID, membershipID, roleID, time.Now(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, dup); !errors.Is(err, membershiprole.ErrMembershipRoleExists) {
		t.Fatalf("err = %v，期望 ErrMembershipRoleExists", err)
	}

	// 尚未到期的行不得被推进：它还是生效的授权。
	still, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || still == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if still.Status != membershiprole.StatusActive {
		t.Fatalf("尚未到期的授权不应被推进，实际状态 %q", still.Status)
	}
	if still.Version != grant.Version {
		t.Fatalf("未被推进的行版本号不应变化：%d → %d", grant.Version, still.Version)
	}
}

// Test成员角色Update不存在的行返回记录不存在
//
// 乐观锁改造之后，Update 的 WHERE 里多了一个 version 前提，
// 命中 0 行时必须区分"记录不存在"与"已被他人改过"：
// 否则一次针对不存在行的更新会被误报成并发冲突（409），
// 上层会把它当成"再读一次就好"，而真相是这条记录从来就不存在。
func Test成员角色Update不存在的行返回记录不存在(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})

	// 把主键换成一个不存在的 id：更新命中 0 行，存在性复查也查不到。
	grant.ID = uuid.NewV7().String()

	err := f.memberRoles.Update(f.ctx, grant)
	if !errors.Is(err, membershiprole.ErrMembershipRoleNotFound) {
		t.Fatalf("err = %v，期望 ErrMembershipRoleNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
	}
}

// Test成员角色主键冲突归入主键重复
//
// 主键重复与业务唯一冲突必须分开：
// 前者是 ID 生成或重试逻辑的缺陷，后者是"该成员已持有该角色"。
// 混为一谈会让排障去找业务键，而真正该查的是 ID。
//
// 这一条也只有真库能证明：约束名是驱动在报错时才给出的，
// 单测里的伪造错误走不到这条路径。
func Test成员角色主键冲突归入主键重复(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	grant := f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, role.KeyOwner), time.Time{})

	// 换一条全新的业务键，只复用主键：撞的应当是主键而不是业务唯一索引。
	dup, err := membershiprole.NewSystem(tenantID, membershipID, f.mustCreateRole(t, tenantID, role.KeyAdmin), time.Now(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	dup.ID = grant.ID

	err = f.memberRoles.Create(f.ctx, dup)
	if !errors.Is(err, domain.ErrDuplicateID) {
		t.Fatalf("err = %v，期望 domain.ErrDuplicateID", err)
	}
	// 关键断言：不能被误报成"该成员已持有该角色"。
	if errors.Is(err, membershiprole.ErrMembershipRoleExists) {
		t.Fatalf("err = %v，主键冲突不应被误报为已持有该角色", err)
	}
}

// Test成员角色原因超长归入字段级错误
//
// 原因超长在领域层是 ErrInvalidReason（400 语义），
// 从数据库 CHECK 兜底时也必须是同一个错误：
// 同一语义给出两个状态码，上层没法统一归类。
func Test成员角色原因超长归入字段级错误(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)

	grant, err := membershiprole.NewSystem(tenantID, membershipID, roleID, time.Now(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	// 绕过领域层的校验，直接把超长的原因塞进实体：
	// 这里要验的正是"领域层被绕过时，数据库 CHECK 兜底给出的是不是同一个错误"。
	grant.Reason = strings.Repeat("理", 513)

	err = f.memberRoles.Create(f.ctx, grant)
	if !errors.Is(err, membershiprole.ErrInvalidReason) {
		t.Fatalf("err = %v，期望 ErrInvalidReason", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
	}
	// 记录级错误是 409 语义，与字段级错误不能混。
	if errors.Is(err, membershiprole.ErrInvalidMembershipRoleRecord) {
		t.Fatalf("err = %v，字段级错误不应归入记录级错误", err)
	}
}

// Test成员角色五条CHECK在真库上都拒绝
//
// 领域层的 Validate 已经能在入口拦下这些取值，
// 但 CHECK 是最后一道防线：绕过领域层直接写库时必须仍然挡住。
// 这里用裸 SQL 绕过入口校验，逐条确认数据库真的会拒绝，
// 且拒绝时 GORM 的翻译能认出这是 CHECK 违反——
// 否则上层拿到的是驱动原生错误而不是可归类的领域错误。
func Test成员角色五条CHECK在真库上都拒绝(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Now().Add(time.Hour))

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"状态取值越界", "UPDATE membership_roles SET status = ? WHERE id = ?", []any{"paused", grant.ID}},
		{"分配人为纯空白", "UPDATE membership_roles SET assigned_by = ? WHERE id = ?", []any{"   ", grant.ID}},
		{"失效不晚于生效", "UPDATE membership_roles SET expires_at = effective_at WHERE id = ?", []any{grant.ID}},
		{"expired 无到期时间", "UPDATE membership_roles SET status = ?, expires_at = NULL WHERE id = ?", []any{string(membershiprole.StatusExpired), grant.ID}},
		{"原因为纯空白", "UPDATE membership_roles SET reason = ? WHERE id = ?", []any{"   ", grant.ID}},
		{"原因超长", "UPDATE membership_roles SET reason = ? WHERE id = ?", []any{strings.Repeat("理", 513), grant.ID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.raw.Exec(tc.sql, tc.args...).Error
			if err == nil {
				t.Fatalf("数据库应拒绝：%s", tc.name)
			}
			if !errors.Is(err, gorm.ErrCheckConstraintViolated) {
				t.Fatalf("err = %v，期望归入 gorm.ErrCheckConstraintViolated", err)
			}

			// 行必须原样不动：拒绝的写入不能留下半截状态。
			got, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
			if err != nil || got == nil {
				t.Fatalf("取回授权失败：%v", err)
			}
			if got.Status != membershiprole.StatusActive {
				t.Fatalf("被拒绝的写入不应改变状态，实际 %q", got.Status)
			}
			if got.ExpiresAt.IsZero() {
				t.Fatal("被拒绝的写入不应清空到期时间")
			}
		})
	}
}

// Test成员角色Update不改写不可写列
//
// 白名单是"不可写"约定的唯一落地：Select 只放行四列，
// 传入实体里的其余字段一律不进 SET 子句。
// 这条用例把不可写的列全改一遍再落库，逐列确认库里的值没动——
// 其中 ExpiresAt 最关键：把它清零也要写不进去，
// 否则一条有期限的临时授权会被静默改成永久有效。
func Test成员角色Update不改写不可写列(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	grant := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Now().Add(time.Hour))

	before, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || before == nil {
		t.Fatalf("取回授权失败：%v", err)
	}

	// 把不可写的列全部改掉，只保留 id 与 tenant_id 作为 WHERE 的条件。
	mutated := *before
	mutated.MembershipID = uuid.NewV7().String()
	mutated.RoleID = uuid.NewV7().String()
	mutated.AssignedBy = "篡改进来的人"
	mutated.AssignedAt = time.Now().Add(-1000 * time.Hour)
	mutated.CreatedAt = time.Now().Add(-2000 * time.Hour)
	mutated.UpdatedAt = time.Now().Add(-3000 * time.Hour)
	// 关键：到期时间清零也不该写进去（expires_at 已移出可写列）。
	mutated.ExpiresAt = time.Time{}
	// 可写的三列：状态、原因与生效时间。
	mutated.Status = membershiprole.StatusRevoked
	mutated.Reason = "撤销理由"
	mutated.EffectiveAt = before.EffectiveAt.Add(time.Minute)

	if err := f.memberRoles.Update(f.ctx, &mutated); err != nil {
		t.Fatalf("落库撤销失败：%v", err)
	}

	after, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, grant.ID))
	if err != nil || after == nil {
		t.Fatalf("取回授权失败：%v", err)
	}

	// 不可写的列必须原样不动。
	if after.MembershipID != before.MembershipID {
		t.Fatalf("MembershipID 被改写：%q → %q", before.MembershipID, after.MembershipID)
	}
	if after.RoleID != before.RoleID {
		t.Fatalf("RoleID 被改写：%q → %q", before.RoleID, after.RoleID)
	}
	if after.AssignedBy != before.AssignedBy {
		t.Fatalf("AssignedBy 被改写：%q → %q", before.AssignedBy, after.AssignedBy)
	}
	if !after.AssignedAt.Equal(before.AssignedAt) {
		t.Fatalf("AssignedAt 被改写：%v → %v", before.AssignedAt, after.AssignedAt)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("CreatedAt 被改写：%v → %v", before.CreatedAt, after.CreatedAt)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("ExpiresAt 被改写：%v → %v", before.ExpiresAt, after.ExpiresAt)
	}
	// updated_at 归触发器：传入的值被覆盖，且一定推进了。
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at 未被触发器推进：%v → %v", before.UpdatedAt, after.UpdatedAt)
	}

	// 可写的三列必须生效，版本号必须自增。
	if after.Status != membershiprole.StatusRevoked {
		t.Fatalf("Status = %q，期望 revoked", after.Status)
	}
	if after.Reason != "撤销理由" {
		t.Fatalf("Reason = %q，期望撤销理由", after.Reason)
	}
	if !after.EffectiveAt.Equal(mutated.EffectiveAt) {
		t.Fatalf("EffectiveAt = %v，期望 %v", after.EffectiveAt, mutated.EffectiveAt)
	}
	if after.Version != before.Version+1 {
		t.Fatalf("Version = %d，期望 %d", after.Version, before.Version+1)
	}
}

// Test待审批授权的完整流程在真库上成立
//
// pending 这条分支此前在真库上从未跑过：
// 本包的核心特性是"pending → active 的审批流"，
// 而所有集成用例都只造 active 行。
// 这里把"pending 占槽位、不参与鉴权、审批后改写生效时间"逐条钉住。
func Test待审批授权的完整流程在真库上成立(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	now := time.Now()

	pending, err := membershiprole.NewSystemPending(tenantID, membershipID, roleID, now, time.Time{}, "待审批")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, pending); err != nil {
		t.Fatalf("写入授权失败：%v", err)
	}

	// pending 也算"未失效"：它占着部分唯一索引的槽位。
	got, err := f.memberRoles.GetByMembershipAndRole(f.ctx, tenantID, membershipID, roleID, now)
	if err != nil || got == nil {
		t.Fatalf("pending 授权应能被取回，实际 (%+v, %v)", got, err)
	}
	if got.Status != membershiprole.StatusPending {
		t.Fatalf("Status = %q，期望 pending", got.Status)
	}

	// pending 不参与鉴权：生效列表里没有它。
	active, err := f.memberRoles.ListActiveByMembershipID(f.ctx, tenantID, membershipID, now)
	if err != nil {
		t.Fatalf("查询生效授权失败：%v", err)
	}
	if len(active) != 0 {
		t.Fatalf("pending 不应出现在生效列表里，实际 %d 条", len(active))
	}

	// pending 同样占用唯一槽位：同一对取值不能再分配。
	dup, err := membershiprole.NewSystem(tenantID, membershipID, roleID, now, time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, dup); !errors.Is(err, membershiprole.ErrMembershipRoleExists) {
		t.Fatalf("err = %v，期望 ErrMembershipRoleExists", err)
	}

	// 审批通过并落库。
	approveAt := now.Add(time.Minute)
	if err := pending.Approve(approveAt); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	if err := f.memberRoles.Update(f.ctx, pending); err != nil {
		t.Fatalf("落库审批失败：%v", err)
	}

	after, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, pending.ID))
	if err != nil || after == nil {
		t.Fatalf("取回授权失败：%v", err)
	}
	if after.Status != membershiprole.StatusActive {
		t.Fatalf("Status = %q，期望 active", after.Status)
	}
	// 审批时刻才是真正开始生效的时刻。
	if !after.EffectiveAt.Equal(approveAt.Truncate(time.Microsecond)) {
		t.Fatalf("EffectiveAt = %v，期望审批时刻 %v", after.EffectiveAt, approveAt)
	}
	// 分配时间记录授权动作本身，审批不得改写它。
	if !after.AssignedAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatalf("AssignedAt = %v，不应被审批改写", after.AssignedAt)
	}

	// 审批之后就该出现在生效列表里了。
	active, err = f.memberRoles.ListActiveByMembershipID(f.ctx, tenantID, membershipID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("查询生效授权失败：%v", err)
	}
	if len(active) != 1 || active[0].ID != pending.ID {
		t.Fatalf("生效列表应只有审批通过的那一条，实际 %d 条", len(active))
	}
}

// Test生效列表与角色反向列表在真库上正确
//
// ListActiveByMembershipID 被端口注释标为"鉴权热路径"，ListByRoleID 是同一个
// 多对多关联的反向查询，此前两者在真库上都是零覆盖：
// 条件或排序写错只有线上鉴权出问题才会被发现。
func Test生效列表与角色反向列表在真库上正确(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantA, tenantB := uuid.NewV7(), uuid.NewV7()
	membershipA := f.mustCreateMembership(t, tenantA, uuid.NewV7())
	membershipB := f.mustCreateMembership(t, tenantB, uuid.NewV7())
	now := time.Now()

	// 同一个成员关系上三条：active（长期有效）、revoked、pending。
	activeGrant := f.mustGrantRole(t, tenantA, membershipA, f.mustCreateRole(t, tenantA, role.KeyOwner), time.Time{})
	roleAdmin := f.mustCreateRole(t, tenantA, role.KeyAdmin)
	revokedGrant := f.mustGrantRole(t, tenantA, membershipA, roleAdmin, time.Time{})
	if err := revokedGrant.Revoke("不再需要"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if err := f.memberRoles.Update(f.ctx, revokedGrant); err != nil {
		t.Fatalf("落库撤销失败：%v", err)
	}
	pendingGrant, err := membershiprole.NewSystemPending(tenantA, membershipA, f.mustCreateRole(t, tenantA, role.KeyMember), now, time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, pendingGrant); err != nil {
		t.Fatalf("写入授权失败：%v", err)
	}

	// 生效列表只含 active 的那一条。
	active, err := f.memberRoles.ListActiveByMembershipID(f.ctx, tenantA, membershipA, now)
	if err != nil {
		t.Fatalf("查询生效授权失败：%v", err)
	}
	if len(active) != 1 || active[0].ID != activeGrant.ID {
		t.Fatalf("生效列表应只有 active 的那一条，实际 %d 条", len(active))
	}

	// 历史列表含全部三条（含终态与 pending）。
	all, err := f.memberRoles.ListByMembershipID(f.ctx, tenantA, membershipA)
	if err != nil {
		t.Fatalf("查询授权列表失败：%v", err)
	}
	if len(all) != 3 {
		t.Fatalf("历史列表应有 3 条，实际 %d 条", len(all))
	}

	// 反向：按角色查被授予给哪些成员关系。
	byRole, err := f.memberRoles.ListByRoleID(f.ctx, tenantA, roleAdmin)
	if err != nil {
		t.Fatalf("按角色查询失败：%v", err)
	}
	if len(byRole) != 1 || byRole[0].ID != revokedGrant.ID {
		t.Fatalf("按角色查询应返回那条终态历史行，实际 %d 条", len(byRole))
	}

	// 别租户的行不会串进来：反向查询同样带租户条件。
	f.mustGrantRole(t, tenantB, membershipB, f.mustCreateRole(t, tenantB, role.KeyOwner), time.Time{})
	otherTenant, err := f.memberRoles.ListByRoleID(f.ctx, tenantA, roleAdmin)
	if err != nil {
		t.Fatalf("按角色查询失败：%v", err)
	}
	if len(otherTenant) != 1 {
		t.Fatalf("别租户的行不应串进结果，实际 %d 条", len(otherTenant))
	}

	// 生效列表也不含别租户的授权。
	activeB, err := f.memberRoles.ListActiveByMembershipID(f.ctx, tenantA, membershipB, now)
	if err != nil {
		t.Fatalf("查询生效授权失败：%v", err)
	}
	if len(activeB) != 0 {
		t.Fatalf("别租户的成员关系不应出现在租户 A 的生效列表里，实际 %d 条", len(activeB))
	}
}

// Test读路径不跨租户返回数据
//
// 端口要求"除 GetByID 外所有查询都必须带 tenant_id"——
// 而 GetByID 现在也带上了。少了任何一个条件就是一次跨租户数据读取，
// 而此前没有任何用例验证过读路径的租户隔离。
func Test读路径不跨租户返回数据(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantA, tenantB := uuid.NewV7(), uuid.NewV7()
	membershipA := f.mustCreateMembership(t, tenantA, uuid.NewV7())
	roleA := f.mustCreateRole(t, tenantA, role.KeyOwner)
	grantA := f.mustGrantRole(t, tenantA, membershipA, roleA, time.Time{})
	now := time.Now()

	// 用租户 B 的标识去查租户 A 的授权：全部应为空而不是错误。
	got, err := f.memberRoles.GetByID(f.ctx, tenantB, mustParseUUID(t, grantA.ID))
	if got != nil || err != nil {
		t.Fatalf("跨租户的 GetByID 应返回 (nil, nil)，实际 (%+v, %v)", got, err)
	}

	got, err = f.memberRoles.GetByMembershipAndRole(f.ctx, tenantB, membershipA, roleA, now)
	if got != nil || err != nil {
		t.Fatalf("跨租户的 GetByMembershipAndRole 应返回 (nil, nil)，实际 (%+v, %v)", got, err)
	}

	for name, list := range map[string]func() ([]*membershiprole.MembershipRole, error){
		"ListByMembershipID": func() ([]*membershiprole.MembershipRole, error) {
			return f.memberRoles.ListByMembershipID(f.ctx, tenantB, membershipA)
		},
		"ListActiveByMembershipID": func() ([]*membershiprole.MembershipRole, error) {
			return f.memberRoles.ListActiveByMembershipID(f.ctx, tenantB, membershipA, now)
		},
		"ListByRoleID": func() ([]*membershiprole.MembershipRole, error) {
			return f.memberRoles.ListByRoleID(f.ctx, tenantB, roleA)
		},
		"ListDueForExpiry": func() ([]*membershiprole.MembershipRole, error) {
			return f.memberRoles.ListDueForExpiry(f.ctx, tenantB, now, 100)
		},
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := list()
			if err != nil {
				t.Fatalf("%s 不应报错：%v", name, err)
			}
			if len(rows) != 0 {
				t.Fatalf("%s 不应返回租户 A 的数据，实际 %d 条", name, len(rows))
			}
		})
	}

	// 反向确认：用本租户的标识能取回，证明上面的空不是因为数据没写进去。
	got, err = f.memberRoles.GetByID(f.ctx, tenantA, mustParseUUID(t, grantA.ID))
	if err != nil || got == nil || got.ID != grantA.ID {
		t.Fatalf("本租户应能取回自己的授权，实际 (%+v, %v)", got, err)
	}

	rows, err := f.memberRoles.ListByMembershipID(f.ctx, tenantA, membershipA)
	if err != nil {
		t.Fatalf("查询授权列表失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("本租户应能看到自己的那条授权，实际 %d 条", len(rows))
	}
}

// Test跨租户的成员关系或角色创建授权被拒绝
//
// 本表全仓一律不加外键（见 migrations/000009 的表注释），
// 引用完整性只能由写路径保证。少了归属校验，
// 租户 A 的 id 配上租户 B 的成员关系就能静默落库，
// 此后按 (tenant_id=A, membership_id=B成员) 查询照样查得到它。
func Test跨租户的成员关系或角色创建授权被拒绝(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantA, tenantB := uuid.NewV7(), uuid.NewV7()
	membershipA := f.mustCreateMembership(t, tenantA, uuid.NewV7())
	membershipB := f.mustCreateMembership(t, tenantB, uuid.NewV7())
	roleA := f.mustCreateRole(t, tenantA, role.KeyOwner)
	roleB := f.mustCreateRole(t, tenantB, role.KeyOwner)
	now := time.Now()

	cases := []struct {
		name  string
		grant func() (*membershiprole.MembershipRole, error)
		want  error
	}{
		{
			"成员关系属于别的租户",
			func() (*membershiprole.MembershipRole, error) {
				return membershiprole.NewSystem(tenantA, membershipB, roleA, now, time.Time{}, "")
			},
			domain.ErrInvalidMembershipID,
		},
		{
			"角色属于别的租户",
			func() (*membershiprole.MembershipRole, error) {
				return membershiprole.NewSystem(tenantA, membershipA, roleB, now, time.Time{}, "")
			},
			domain.ErrInvalidRoleID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grant, err := tc.grant()
			if err != nil {
				t.Fatalf("构造授权失败：%v", err)
			}
			err = f.memberRoles.Create(f.ctx, grant)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
			}
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
			}
		})
	}

	// 归属一致时照常写入：校验不能误伤正常路径。
	grant, err := membershiprole.NewSystem(tenantA, membershipA, roleA, now, time.Time{}, "")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	if err := f.memberRoles.Create(f.ctx, grant); err != nil {
		t.Fatalf("归属一致时应能写入，实际 err = %v", err)
	}
}

// Test写入入口拦下绕过构造函数的非法取值
//
// Create 与 Update 都先走 MembershipRole.Validate：
// 实体的字段是导出的，调用方可以不走构造函数直接改写它们。
// 少了这一道，非法取值只能等数据库 CHECK 兜底，
// 上层拿到的是记录级错误而不是可归类的入参错误。
func Test写入入口拦下绕过构造函数的非法取值(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())
	roleID := f.mustCreateRole(t, tenantID, role.KeyOwner)
	now := time.Now()

	newGrant := func() *membershiprole.MembershipRole {
		grant, err := membershiprole.NewSystem(tenantID, membershipID, roleID, now, time.Time{}, "")
		if err != nil {
			t.Fatalf("构造授权失败：%v", err)
		}
		return grant
	}

	t.Run("Create", func(t *testing.T) {
		grant := newGrant()
		grant.AssignedBy = "   "

		err := f.memberRoles.Create(f.ctx, grant)
		if !errors.Is(err, domain.ErrInvalidActor) {
			t.Fatalf("err = %v，期望 domain.ErrInvalidActor", err)
		}
		if !errors.Is(err, domain.ErrInvalidKey) {
			t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
		}
		// 入口就该拒绝，不能落到数据库去撞 CHECK。
		if errors.Is(err, gorm.ErrCheckConstraintViolated) {
			t.Fatalf("err = %v，应在入口拦下而不是等数据库拒绝", err)
		}
	})

	t.Run("Update", func(t *testing.T) {
		stored := f.mustGrantRole(t, tenantID, membershipID, roleID, time.Time{})
		loaded, err := f.memberRoles.GetByID(f.ctx, tenantID, mustParseUUID(t, stored.ID))
		if err != nil || loaded == nil {
			t.Fatalf("取回授权失败：%v", err)
		}
		loaded.Status = membershiprole.Status("weird")

		err = f.memberRoles.Update(f.ctx, loaded)
		if !errors.Is(err, membershiprole.ErrInvalidStatusValue) {
			t.Fatalf("err = %v，期望 ErrInvalidStatusValue", err)
		}
		if errors.Is(err, gorm.ErrCheckConstraintViolated) {
			t.Fatalf("err = %v，应在入口拦下而不是等数据库拒绝", err)
		}
	})
}

// Test到期扫描按上限截断并优先返回到期最久的
//
// limit 是这条定时任务热路径的护栏：没有上限时，
// 服务中断后恢复的积压量会在一次调用里全部读进内存。
func Test到期扫描按上限截断并优先返回到期最久的(t *testing.T) {
	f := newIntegrationFixture(t)

	tenantID := uuid.NewV7()
	membershipID := f.mustCreateMembership(t, tenantID, uuid.NewV7())

	// 三条都已到期（构造函数拒收过去的到期时间，故先建未来的再挪到过去）。
	grants := []*membershiprole.MembershipRole{
		f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, role.KeyOwner), time.Now().Add(time.Hour)),
		f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, role.KeyAdmin), time.Now().Add(time.Hour)),
		f.mustGrantRole(t, tenantID, membershipID, f.mustCreateRole(t, tenantID, role.KeyMember), time.Now().Add(time.Hour)),
	}
	// 两个时刻要一起挪：chk_membership_roles_expires_after_effective 要求
	// expires_at 严格晚于 effective_at，只挪前者会让约束拒绝。
	// effective_at 统一挪到 5 小时前，expires_at 各挪不同的小时数，
	// 到期先后因此由 expires_at 决定。
	for i, tc := range []struct {
		id       string
		expireAt string
	}{
		{grants[0].ID, "1"},
		{grants[1].ID, "3"},
		{grants[2].ID, "2"},
	} {
		// 用参数拼出 interval：直接写 "interval ?" 在 PostgreSQL 里不是合法的
		// 参数位置（语法错误 42601），因此把小时数作为文本参数参与构造。
		if err := f.raw.Exec(
			"UPDATE membership_roles SET"+
				" expires_at = now() - (? || ' hours')::interval,"+
				" effective_at = now() - interval '5 hours'"+
				" WHERE id = ?",
			tc.expireAt, tc.id,
		).Error; err != nil {
			t.Fatalf("把第 %d 条授权挪到过去失败：%v", i, err)
		}
	}

	now := time.Now()

	// 只要两条：上限必须生效。
	batch, err := f.memberRoles.ListDueForExpiry(f.ctx, tenantID, now, 2)
	if err != nil {
		t.Fatalf("扫描到期授权失败：%v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("上限为 2 时应返回 2 条，实际 %d 条", len(batch))
	}
	// 到期最久的排在前面：3 小时前到期的那条应当第一。
	if batch[0].ID != grants[1].ID {
		t.Fatalf("第一条 = %s，期望到期最久的 %s", batch[0].ID, grants[1].ID)
	}
	if batch[1].ID != grants[2].ID {
		t.Fatalf("第二条 = %s，期望次久的 %s", batch[1].ID, grants[2].ID)
	}

	// 上限不是正整数时必须报错，而不是返回空切片：
	// 上限为 0 与"确实没有到期授权"是两件不同的事，
	// 用空结果表达无效入参会让到期推进任务静默空转
	// （扫不到 ⇒ 不推进 ⇒ 过期数据一直留着），
	// 而"直到返回条数小于上限才停"的分批循环还会因此永不结束。
	if _, err := f.memberRoles.ListDueForExpiry(f.ctx, tenantID, now, 0); !errors.Is(err, domain.ErrInvalidLimit) {
		t.Fatalf("err = %v，期望 domain.ErrInvalidLimit", err)
	}
}
