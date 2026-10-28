package gorm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membershiprole"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// pgConstraintError 造一个带约束名的 PostgreSQL 驱动错误，
// 并按 GORM 错误翻译的方式包起来（fmt.Errorf("%w: %w", ...)）。
//
// 必须包成两层：驱动错误在错误链的第二个位置上，
// 直接断言会落空——这正是生产环境里错误链的样子。
func pgConstraintError(code, constraint string, gormErr error) error {
	return fmt.Errorf("%w: %w", gormErr, &pgconn.PgError{
		Code:           code,
		ConstraintName: constraint,
		Message:        fmt.Sprintf("违反约束 %s", constraint),
	})
}

// Test按约束名分流数据库结局
//
// 同一类数据库错误可能来自不同约束，领域语义并不相同：
// 撞主键是写入本身的缺陷，撞业务唯一索引是"已存在"，
// 违反字段级 CHECK 是"取值不合法"。
// 不按约束名分流时，三者落进同一个结局，排障方向会被彻底带偏。
func Test按约束名分流数据库结局(t *testing.T) {
	targets := membershipRoleWriteTargets(newTestMembershipRole(t))

	cases := []struct {
		name     string
		err      error
		want     error
		wantNot  error
		wantBase error
	}{
		{
			name:     "主键重复归入数据完整性缺陷",
			err:      pgConstraintError("23505", "membership_roles_pkey", gorm.ErrDuplicatedKey),
			want:     domain.ErrDuplicateID,
			wantNot:  membershiprole.ErrMembershipRoleExists,
			wantBase: domain.ErrDataIntegrity,
		},
		{
			name:     "主键重复不归入冲突类",
			err:      pgConstraintError("23505", "membership_roles_pkey", gorm.ErrDuplicatedKey),
			want:     domain.ErrDuplicateID,
			wantNot:  domain.ErrConflict,
			wantBase: domain.ErrDataIntegrity,
		},
		{
			name:     "业务唯一索引归入已持有该角色",
			err:      pgConstraintError("23505", "idx_membership_roles_membership_role_active", gorm.ErrDuplicatedKey),
			want:     membershiprole.ErrMembershipRoleExists,
			wantNot:  domain.ErrDuplicateID,
			wantBase: domain.ErrConflict,
		},
		{
			name:     "原因超长归入字段级错误",
			err:      pgConstraintError("23514", "chk_membership_roles_reason", gorm.ErrCheckConstraintViolated),
			want:     membershiprole.ErrInvalidReason,
			wantNot:  membershiprole.ErrInvalidMembershipRoleRecord,
			wantBase: domain.ErrInvalidKey,
		},
		{
			name:     "有效期不合法归入字段级错误",
			err:      pgConstraintError("23514", "chk_membership_roles_expires_after_effective", gorm.ErrCheckConstraintViolated),
			want:     membershiprole.ErrInvalidEffectiveRange,
			wantNot:  membershiprole.ErrInvalidMembershipRoleRecord,
			wantBase: domain.ErrInvalidKey,
		},
		{
			name:     "分配人为空白归入操作者错误",
			err:      pgConstraintError("23514", "chk_membership_roles_assigned_by_not_blank", gorm.ErrCheckConstraintViolated),
			want:     domain.ErrInvalidActor,
			wantNot:  membershiprole.ErrInvalidMembershipRoleRecord,
			wantBase: domain.ErrInvalidKey,
		},
		{
			name:     "状态取值越界仍归记录级错误",
			err:      pgConstraintError("23514", "chk_membership_roles_status", gorm.ErrCheckConstraintViolated),
			want:     membershiprole.ErrInvalidMembershipRoleRecord,
			wantBase: domain.ErrInvalidState,
		},
		{
			name:     "未登记的约束按错误类别兜底",
			err:      pgConstraintError("23505", "idx_something_new", gorm.ErrDuplicatedKey),
			want:     membershiprole.ErrMembershipRoleExists,
			wantBase: domain.ErrConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyWriteError(tc.err, targets)
			if !errors.Is(got, tc.want) {
				t.Fatalf("err = %v，期望 %v", got, tc.want)
			}
			if tc.wantNot != nil && errors.Is(got, tc.wantNot) {
				t.Fatalf("err = %v，不应归类到 %v", got, tc.wantNot)
			}
			if !errors.Is(got, tc.wantBase) {
				t.Fatalf("err = %v，应归类到 %v", got, tc.wantBase)
			}
			// 底层错误必须保留：真正被违反的约束名只有驱动知道。
			if !errors.Is(got, gorm.ErrDuplicatedKey) && !errors.Is(got, gorm.ErrCheckConstraintViolated) {
				t.Fatalf("err = %v，应保留底层 GORM 错误", got)
			}
		})
	}
}

// Test主键冲突在各聚合都归入主键重复
//
// 主键重复是写入本身的缺陷，与任何业务键都无关。
// 各聚合若不登记自己的主键约束，它会落进"已存在"，
// 上层看到的提示会指向完全无关的业务键。
func Test主键冲突在各聚合都归入主键重复(t *testing.T) {
	cases := []struct {
		name       string
		constraint string
		targets    writeTargets
	}{
		{name: "成员关系", constraint: "memberships_pkey", targets: membershipWriteTargets(newTestMembership(t))},
		{name: "成员角色授权", constraint: "membership_roles_pkey", targets: membershipRoleWriteTargets(newTestMembershipRole(t))},
		{name: "角色", constraint: "roles_pkey", targets: roleWriteTargets(newTestRoleEntity(t))},
		{name: "租户", constraint: "tenants_pkey", targets: tenantWriteTargets(newTestTenant(t))},
		{name: "用户", constraint: "users_pkey", targets: userWriteTargets(newTestUser(t))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pgConstraintError("23505", tc.constraint, gorm.ErrDuplicatedKey)
			got := classifyWriteError(err, tc.targets)
			if !errors.Is(got, domain.ErrDuplicateID) {
				t.Fatalf("err = %v，期望 domain.ErrDuplicateID", got)
			}
			// 主键重复是服务端缺陷，不能被归成可重试的业务冲突。
			if errors.Is(got, domain.ErrConflict) {
				t.Fatalf("err = %v，不应归类到 domain.ErrConflict", got)
			}
		})
	}
}

// Test驱动的错误翻译保留约束名
//
// 按约束名分流的前提是：GORM 的错误翻译把驱动错误原样留在错误链上。
// 这个前提不该只靠真库集成测试守着——Docker 不可用时那批用例会整体跳过，
// 机制前提就变成无人看守。这里直接对 postgres 驱动的 Translate 求值，
// 不连数据库就能证明前提成立。
func Test驱动的错误翻译保留约束名(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "membership_roles_pkey",
		Message:        "duplicate key value violates unique constraint",
	}

	translated := gormpostgres.Dialector{}.Translate(pgErr)

	// GORM 的语义错误必须可判定。
	if !errors.Is(translated, gorm.ErrDuplicatedKey) {
		t.Fatalf("翻译后的错误 = %v，期望归入 gorm.ErrDuplicatedKey", translated)
	}
	// 驱动错误必须仍在错误链上，否则约束名取不到，分流无从谈起。
	var got *pgconn.PgError
	if !errors.As(translated, &got) {
		t.Fatalf("翻译后的错误 = %v，错误链上应仍能取到 *pgconn.PgError", translated)
	}
	if got.ConstraintName != "membership_roles_pkey" {
		t.Fatalf("约束名 = %q，期望 membership_roles_pkey", got.ConstraintName)
	}
}

// Test登记的约束名与迁移脚本一致
//
// byConstraint 的键是数据库里的约束名字面量，与迁移脚本之间没有任何编译期关联：
// 迁移里改了约束名，Go 侧会静默失效并退回按类别兜底，
// 没有任何测试会失败——这是本次改动最脆弱的一环。
// 这里把已登记的约束名与 migrations/*.up.sql 里的字面量对一遍，
// 让改名漂移在 CI 上立刻暴露。
func Test登记的约束名与迁移脚本一致(t *testing.T) {
	script := readMigrationScript(t)

	registered := map[string][]string{
		"memberships":      registeredConstraintNames(membershipWriteTargets(newTestMembership(t))),
		"membership_roles": registeredConstraintNames(membershipRoleWriteTargets(newTestMembershipRole(t))),
		"roles":            registeredConstraintNames(roleWriteTargets(newTestRoleEntity(t))),
		"tenants":          registeredConstraintNames(tenantWriteTargets(newTestTenant(t))),
		"users":            registeredConstraintNames(userWriteTargets(newTestUser(t))),
	}

	for table, names := range registered {
		for _, name := range names {
			if strings.Contains(script, name) {
				continue
			}
			// 主键约束在迁移里没有显式命名，由 PostgreSQL 按 <表名>_pkey 生成，
			// 脚本里查不到它的字面量。改为确认该表确实被声明——
			// 表在，主键约束名就成立。
			if name == table+"_pkey" &&
				strings.Contains(script, "CREATE TABLE IF NOT EXISTS "+table) {
				continue
			}
			t.Errorf("约束 %q 未出现在迁移脚本里，登记的名字已与数据库脱节", name)
		}
	}
}

// registeredConstraintNames 列出某个 writeTargets 登记的全部约束名。
func registeredConstraintNames(targets writeTargets) []string {
	names := make([]string, 0, len(targets.byConstraint))
	for name := range targets.byConstraint {
		names = append(names, name)
	}
	return names
}

// readMigrationScript 把 migrations 下所有 up 脚本拼成一段文本，
// 供约束名的一致性检查使用。
func readMigrationScript(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(repoRoot(t), "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取迁移目录失败：%v", err)
	}

	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读取迁移文件 %s 失败：%v", e.Name(), err)
		}
		sb.Write(content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// repoRoot 从当前目录向上找到 go.mod 所在目录，即仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取当前目录失败：%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到仓库根目录（向上没有 go.mod）")
		}
		dir = parent
	}
}
