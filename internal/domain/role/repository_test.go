package role

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// stubRepository 是 Repository 端口的最小实现，承担两件事：
//
//   - 锁定端口签名：签名一旦变动，本文件会立即编译失败，
//     而不是等到适配层实现时才发现（与 membership/repository_test.go 同一手法）；
//   - 让 ListPermissionIDs 与 ReplacePermissions 这对契约有东西可跑。
//     真正的 GORM 实现落地之前，这里是唯一能验证"写入即读出""整体替换""租户隔离"
//     这三个语义的地方，因此它按设想的语义实现，而不是一律返回零值。
//
// 边界声明：本文件只验证领域语义与端口形状，
// **不验证 GORM 适配层的差分写入与 SQL 租户条件**——
// 后者由 internal/adapters/persistence/gorm 包的用例覆盖。
// 因此 stub 必须与真实实现保持一致（错误类型、校验顺序、排序保证、标识语义），
// 否则这里会给出一个"契约已被验证"的错觉，而真实适配层可能整体不符合端口。
//
// 一条例外：端口还要求"待写入的权限标识必须真实存在于系统权限目录"，
// 而目录是数据库里的一张表（permissions），stub 无从建模，
// 因此这一条只在适配层验证——stub 只校验权限标识的形态（合法 UUID）。
//
// 刻意只用 map 而不引入 mock 框架：本仓不依赖 testify/mock（见 go.mod），
// 标准库足够表达"按租户+角色存一份权限集合"。
type stubRepository struct {
	roles map[string]*Role
	perms map[string][]string
}

var _ Repository = (*stubRepository)(nil)

func newStubRepository() *stubRepository {
	return &stubRepository{
		roles: make(map[string]*Role),
		perms: make(map[string][]string),
	}
}

// stubKey 拼接 map 的键。
// UUID 字符串里不含竖线，用它分隔即可，不必担心 "ab"+"c" 与 "a"+"bc" 撞键。
func stubKey(tenantID, roleID string) string {
	return tenantID + "|" + roleID
}

func (s *stubRepository) Create(ctx context.Context, r *Role) error {
	if r == nil {
		return errors.New("实体为空")
	}
	copied := *r
	s.roles[stubKey(r.TenantID, r.ID)] = &copied
	return nil
}

func (s *stubRepository) FindByID(ctx context.Context, tenantID, roleID string) (*Role, error) {
	r, ok := s.roles[stubKey(tenantID, roleID)]
	if !ok {
		return nil, nil
	}
	copied := *r
	return &copied, nil
}

func (s *stubRepository) FindByKey(ctx context.Context, tenantID, key string) (*Role, error) {
	for _, r := range s.roles {
		if r.TenantID == tenantID && r.Key == key {
			copied := *r
			return &copied, nil
		}
	}
	return nil, nil
}

// ReplacePermissions 模拟真实实现必须具备的租户校验：
// 角色不属于本租户时不写入任何数据，直接失败。
//
// 错误类型与真实实现一致（domainrole.ErrRoleNotFound）：
// 若这里返回别的错误，领域层这套用例会给出一个"错误码归属已被验证"的错觉，
// 而真实适配层换成别的错误时测试依然全绿。
//
// 校验顺序也与真实实现一致：先归一化权限标识，再校验租户归属。
// 两者同时不合法时先报哪一个，是契约的一部分。
func (s *stubRepository) ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error {
	ids, err := NormalizePermissionIDs(permissionIDs)
	if err != nil {
		return err
	}
	if _, ok := s.roles[stubKey(tenantID, roleID)]; !ok {
		return fmt.Errorf("%w: tenant_id=%s role_id=%s", ErrRoleNotFound, tenantID, roleID)
	}
	s.perms[stubKey(tenantID, roleID)] = ids
	return nil
}

// ListPermissionIDs 与 ReplacePermissions 取值口径一致：
// 都是 permissions.id 的 UUID 字符串，因此"读出来再写回去"是一次幂等往返。
//
// 标识语义与真实实现一致：
//   - 标识为空 → 空切片，"没有依据可查"不是错误；
//   - 标识非空但非法 → 字段级错误，调用方 bug 不得伪装成"查不到"。
func (s *stubRepository) ListPermissionIDs(ctx context.Context, tenantID, roleID string) ([]string, error) {
	if err := validateLookupIDs(tenantID, roleID); err != nil {
		return nil, err
	}

	ids := s.perms[stubKey(tenantID, roleID)]
	if len(ids) == 0 {
		return []string{}, nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out, nil
}

// validateLookupIDs 校验读路径上的标识：空则放行（交给调用方返回空结果），
// 非空但不是合法 UUID 则返回字段级错误。
//
// 与适配层的 normalizeLookupID 同口径：stub 必须与真实实现保持一致，
// 否则这里会给出一个"契约已被验证"的错觉。
func validateLookupIDs(values ...string) error {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		parsed, err := uuid.Parse(strings.TrimSpace(v))
		if err != nil || parsed == uuid.Nil() {
			return fmt.Errorf("%w: %q", domain.ErrInvalidKey, v)
		}
	}
	return nil
}

// newTestRole 构造一个自定义角色。刻意避开系统预置 key：
// NewCustomRole 会拒绝占用系统标识，用它们构造必然失败（见 role.go）。
func newTestRole(t *testing.T, tenantID uuid.UUID) *Role {
	t.Helper()
	r, err := NewCustomRole(tenantID, "auditor", "审计员", "")
	if err != nil {
		t.Fatalf("构造角色失败：%v", err)
	}
	return r
}

// ListRoleIDsByPermissionID 是 ListPermissionIDs 的反方向：
// 同一个关联的两个视角，共用同一份存储。
//
// 结果按 role_id 升序返回，与端口契约一致：
// 遍历 map 的顺序是随机的，不排序就会让顺序相关的断言变成假绿。
//
// 标识语义与真实实现一致，同 ListPermissionIDs。
func (s *stubRepository) ListRoleIDsByPermissionID(ctx context.Context, tenantID, permissionID string) ([]string, error) {
	if err := validateLookupIDs(tenantID, permissionID); err != nil {
		return nil, err
	}

	roleIDs := make([]string, 0)
	for _, role := range s.roles {
		if role.TenantID != tenantID {
			continue
		}
		if hasPermission(s.perms[stubKey(role.TenantID, role.ID)], permissionID) {
			roleIDs = append(roleIDs, role.ID)
		}
	}
	sort.Strings(roleIDs)
	return roleIDs, nil
}

// hasPermission 判断某个角色的权限集合里是否包含给定权限。
func hasPermission(perms []string, permissionID string) bool {
	for _, id := range perms {
		if id == permissionID {
			return true
		}
	}
	return false
}

// TestRepository_写入的权限可读回
//
// 这条用例存在的理由就是 ListPermissionIDs 本身存在的理由：
// 只有写口没有读口时，调用方既无法校验替换是否生效，
// 也无法判断一次失败的替换留下了什么中间状态。
func TestRepository_写入的权限可读回(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	role := newTestRole(t, tenantID)

	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}

	want := []string{uuid.New().String(), uuid.New().String()}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, want); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}

	got, err := repo.ListPermissionIDs(ctx, tenantID.String(), role.ID)
	if err != nil {
		t.Fatalf("读取权限失败：%v", err)
	}
	if !equalIDs(got, want) {
		t.Fatalf("读回 %v，期望 %v", got, want)
	}
}

// TestRepository_替换是全量覆盖而不是追加
//
// "整体替换"是这个端口唯一的写入方式：传进来的集合就是最终结果，
// 不在集合里的旧关联必须全部消失。若实现写成追加，
// 管理员撤销权限的结果就是权限越攒越多。
func TestRepository_替换是全量覆盖而不是追加(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	role := newTestRole(t, tenantID)
	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}

	keeper := uuid.New().String()
	dropped := uuid.New().String()
	fresh := uuid.New().String()

	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, []string{keeper, dropped}); err != nil {
		t.Fatalf("首次替换失败：%v", err)
	}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, []string{keeper, fresh}); err != nil {
		t.Fatalf("二次替换失败：%v", err)
	}

	got, err := repo.ListPermissionIDs(ctx, tenantID.String(), role.ID)
	if err != nil {
		t.Fatalf("读取权限失败：%v", err)
	}
	if !equalIDs(got, []string{keeper, fresh}) {
		t.Fatalf("读回 %v，期望 %v（dropped 必须消失）", got, []string{keeper, fresh})
	}
}

// TestRepository_权限集合为空时可读回空切片
//
// 清空是合法操作，且必须与"从未设置过"返回同一种形态，
// 让调用方统一用 len() 判断。
func TestRepository_权限集合为空时可读回空切片(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	role := newTestRole(t, tenantID)
	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}

	// 场景一：清空一份已有权限。
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, []string{uuid.New().String()}); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, nil); err != nil {
		t.Fatalf("清空权限失败：%v", err)
	}
	got, err := repo.ListPermissionIDs(ctx, tenantID.String(), role.ID)
	if err != nil {
		t.Fatalf("读取权限失败：%v", err)
	}
	if got == nil {
		t.Fatal("清空后应返回空切片，而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("清空后读回 %v，期望为空", got)
	}

	// 场景二：从未设置过权限，返回值形态与场景一相同。
	never := newTestRole(t, tenantID)
	never.Key = "reviewer"
	if err := repo.Create(ctx, never); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}
	got, err = repo.ListPermissionIDs(ctx, tenantID.String(), never.ID)
	if err != nil {
		t.Fatalf("读取权限失败：%v", err)
	}
	if got == nil {
		t.Fatal("从未设置过权限时应返回空切片，而不是 nil")
	}
}

// TestRepository_跨租户不可触碰角色权限
//
// role_permissions 上冗余 tenant_id 正是为了这一条：
// 租户条件下推到索引之后，跨租户读取拿不到任何东西、跨租户写入直接失败，
// 而不必先 join roles 才能判断归属。
func TestRepository_跨租户不可触碰角色权限(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantA := uuid.New()
	tenantB := uuid.New()
	role := newTestRole(t, tenantA)

	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}
	if err := repo.ReplacePermissions(ctx, tenantA.String(), role.ID, []string{uuid.New().String()}); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}

	got, err := repo.ListPermissionIDs(ctx, tenantB.String(), role.ID)
	if err != nil {
		t.Fatalf("跨租户读取不应报错：%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("跨租户读取到 %d 条权限，期望 0 条", len(got))
	}

	err = repo.ReplacePermissions(ctx, tenantB.String(), role.ID, []string{uuid.New().String()})
	if err == nil {
		t.Fatal("跨租户写入应失败，实际通过")
	}
	// 错误类型必须与端口契约一致：跨租户写入是"要写却写不到"，
	// 归入 ErrRoleNotFound。只断言"非 nil"是不够的——
	// 适配层换成任何别的错误，这个用例都会全绿。
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("错误 = %v，期望 ErrRoleNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 %v 未归类到 domain.ErrNotFound", err)
	}
}

// TestRepository_非法权限标识在写入前被拒绝且不破坏已有数据
//
// 归一化发生在写之前，因此失败时库里的内容必须原封不动：
// 一次失败的替换不能让角色半途失去部分权限。
func TestRepository_非法权限标识在写入前被拒绝且不破坏已有数据(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	role := newTestRole(t, tenantID)
	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}

	kept := []string{uuid.New().String(), uuid.New().String()}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, kept); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}

	err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, []string{uuid.New().String(), "not-a-uuid"})
	if !errors.Is(err, domain.ErrInvalidPermissionID) {
		t.Fatalf("错误 = %v，期望归入 domain.ErrInvalidPermissionID", err)
	}

	got, listErr := repo.ListPermissionIDs(ctx, tenantID.String(), role.ID)
	if listErr != nil {
		t.Fatalf("读取权限失败：%v", listErr)
	}
	if !equalIDs(got, kept) {
		t.Fatalf("写入失败后读回 %v，期望保持 %v 不变", got, kept)
	}
}

// TestRepository_按权限反查持有它的角色
//
// ListRoleIDsByPermissionID 回答的是 ListPermissionIDs 的对立面：
// "哪些角色能做这件事"。权限变更前的波及面确认依赖它。
func TestRepository_按权限反查持有它的角色(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	shared := uuid.New().String()
	exclusive := uuid.New().String()

	first := newTestRole(t, tenantID)
	second := newTestRole(t, tenantID)
	second.Key = "reviewer"
	third := newTestRole(t, tenantID)
	third.Key = "operator"

	for _, role := range []*Role{first, second, third} {
		if err := repo.Create(ctx, role); err != nil {
			t.Fatalf("创建角色失败：%v", err)
		}
	}

	if err := repo.ReplacePermissions(ctx, tenantID.String(), first.ID, []string{shared, exclusive}); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), second.ID, []string{shared}); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}
	// 第三个角色不持有任何权限，用于确认它不会被带入结果。
	if err := repo.ReplacePermissions(ctx, tenantID.String(), third.ID, nil); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}

	got, err := repo.ListRoleIDsByPermissionID(ctx, tenantID.String(), shared)
	if err != nil {
		t.Fatalf("反查失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("持有该权限的角色有 %d 个，期望 2 个：%v", len(got), got)
	}

	holders := make(map[string]struct{}, len(got))
	for _, id := range got {
		holders[id] = struct{}{}
	}
	for _, want := range []string{first.ID, second.ID} {
		if _, ok := holders[want]; !ok {
			t.Fatalf("结果 %v 缺少角色 %s", got, want)
		}
	}

	single, err := repo.ListRoleIDsByPermissionID(ctx, tenantID.String(), exclusive)
	if err != nil {
		t.Fatalf("反查失败：%v", err)
	}
	if len(single) != 1 || single[0] != first.ID {
		t.Fatalf("结果 = %v，期望只有 %s", single, first.ID)
	}
}

// TestRepository_反查同样受租户约束
//
// tenant_id 是冗余列、没有外键，
// 一旦查询漏掉这个条件就会把所有租户的角色一并返回。
func TestRepository_反查同样受租户约束(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	tenantID := uuid.New()
	permissionID := uuid.New().String()

	role := newTestRole(t, tenantID)
	if err := repo.Create(ctx, role); err != nil {
		t.Fatalf("创建角色失败：%v", err)
	}
	if err := repo.ReplacePermissions(ctx, tenantID.String(), role.ID, []string{permissionID}); err != nil {
		t.Fatalf("替换权限失败：%v", err)
	}

	got, err := repo.ListRoleIDsByPermissionID(ctx, uuid.New().String(), permissionID)
	if err != nil {
		t.Fatalf("跨租户反查不应报错：%v", err)
	}
	if got == nil {
		t.Fatal("应返回空切片，而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("跨租户反查得到 %d 个角色，期望 0 个", len(got))
	}
}

// TestRepository_读路径对非法标识返回错误
//
// "没有依据可查"与"查错了"是两回事：
// 标识为空是前者，返回空切片；标识非空但非法是后者——
// 那是调用方 bug（两个标识传反了、或 UUID 拼错了），
// 静默当成"查不到"会把程序缺陷伪装成正常的业务结果。
func TestRepository_读路径对非法标识返回错误(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	valid := uuid.New().String()

	cases := map[string]func() error{
		"ListPermissionIDs 非法租户": func() error {
			_, err := repo.ListPermissionIDs(ctx, "not-a-uuid", valid)
			return err
		},
		"ListPermissionIDs 非法角色": func() error {
			_, err := repo.ListPermissionIDs(ctx, valid, "not-a-uuid")
			return err
		},
		"按权限反查 非法租户": func() error {
			_, err := repo.ListRoleIDsByPermissionID(ctx, "not-a-uuid", valid)
			return err
		},
		"按权限反查 非法权限": func() error {
			_, err := repo.ListRoleIDsByPermissionID(ctx, valid, "not-a-uuid")
			return err
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("错误 = %v，期望归入 domain.ErrInvalidKey", err)
			}
		})
	}
}

// TestRepository_读路径对空标识返回空结果
//
// 空条件意味着"这次查询没有依据"，不是"有一条待查的数据"：
// 查库只会白白消耗一次往返，更糟的是会把空字符串打进 UUID 列。
func TestRepository_读路径对空标识返回空结果(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepository()
	valid := uuid.New().String()

	got, err := repo.ListPermissionIDs(ctx, "", valid)
	if err != nil {
		t.Fatalf("空租户不应报错：%v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("结果 = %v，期望空切片", got)
	}

	got, err = repo.ListRoleIDsByPermissionID(ctx, valid, "  ")
	if err != nil {
		t.Fatalf("空权限不应报错：%v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("结果 = %v，期望空切片", got)
	}
}
