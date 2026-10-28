package gorm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	domainrole "github.com/memories-cn/user-management-api/internal/domain/role"
	"gorm.io/gorm"
)

// 本文件与 user_repository_test.go 同一手法：刻意不连数据库，
// 只选"不触碰 *gorm.DB 就能判定"的分支——空实体、入参校验、空条件短路、模型转换。
// 一旦哪天这些分支改成先查库，用例会以 panic 的形式立刻失败，
// 而不是悄悄退化成依赖环境的集成测试。

// TestNewRoleRepository缺少数据库句柄返回错误
func TestNewRoleRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewRoleRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// TestCreate空角色返回ErrNilRole
//
// 空实体必须在触碰数据库之前就被拦下，
// 且不得混入业务分类（见 domainrole.ErrNilRole 的注释）。
func TestCreate空角色返回ErrNilRole(t *testing.T) {
	err := (&RoleRepository{}).Create(context.Background(), nil)
	if !errors.Is(err, domainrole.ErrNilRole) {
		t.Fatalf("错误 = %v，期望 domainrole.ErrNilRole", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("空实体不得被归类到 ErrNotFound：那是业务缺失，不是调用方 bug")
	}
}

// TestRequireUUID只放行合法且非零的UUID
func TestRequireUUID只放行合法且非零的UUID(t *testing.T) {
	valid := uuid.New()

	got, err := requireUUID("  "+valid.String()+"  ", domain.ErrInvalidTenantID)
	if err != nil {
		t.Fatalf("合法 UUID 被拒绝：%v", err)
	}
	if got != valid.String() {
		t.Fatalf("返回 %q，期望规范形态 %q", got, valid.String())
	}

	cases := map[string]string{
		"空串":      "",
		"纯空白":     "   ",
		"非 UUID":  "not-a-uuid",
		"零值 UUID": uuid.Nil().String(),
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := requireUUID(input, domain.ErrInvalidRoleID); !errors.Is(err, domain.ErrInvalidRoleID) {
				t.Fatalf("错误 = %v，期望归入 domain.ErrInvalidRoleID", err)
			}
		})
	}
}

// TestReplacePermissions入参校验早于任何数据库访问
//
// 这些分支都跑在零值 &RoleRepository{} 上：只要实现把校验放到查库之后，
// nil 句柄会立刻 panic，用例随即失败。
// 顺序本身也有意义——先归一化权限、再校验租户与角色，
// 任一步失败都不该让数据库产生任何写入。
func TestReplacePermissions入参校验早于任何数据库访问(t *testing.T) {
	ctx := context.Background()
	r := &RoleRepository{}
	tenantID := uuid.New().String()
	roleID := uuid.New().String()

	cases := map[string]func() error{
		"非法权限标识": func() error {
			return r.ReplacePermissions(ctx, tenantID, roleID, []string{"not-a-uuid"})
		},
		"非法租户标识": func() error {
			return r.ReplacePermissions(ctx, "not-a-uuid", roleID, []string{uuid.New().String()})
		},
		"非法角色标识": func() error {
			return r.ReplacePermissions(ctx, tenantID, "not-a-uuid", []string{uuid.New().String()})
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

// Test查询方法遇到空条件短路返回
//
// 缺少租户或角色条件意味着"这次查询没有依据"，不是"有一条待查的数据"：
// 查库只会白白消耗一次往返，更糟的是把空字符串打进 UUID 列。
func Test查询方法遇到空条件短路返回(t *testing.T) {
	ctx := context.Background()
	r := &RoleRepository{}
	tenantID := uuid.New().String()
	roleID := uuid.New().String()

	// 返回值刻意保持 *Role 这个具体类型：一旦改成 any，
	// 类型化的 nil 指针塞进接口后就不再等于 nil，断言会失效。
	cases := map[string]func() (*domainrole.Role, error){
		"FindByID 缺租户":  func() (*domainrole.Role, error) { return r.FindByID(ctx, "", roleID) },
		"FindByID 缺角色":  func() (*domainrole.Role, error) { return r.FindByID(ctx, tenantID, "") },
		"FindByKey 缺租户": func() (*domainrole.Role, error) { return r.FindByKey(ctx, "", "viewer") },
		"FindByKey 缺标识": func() (*domainrole.Role, error) { return r.FindByKey(ctx, tenantID, "  ") },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if err != nil {
				t.Fatalf("空条件不是错误：%v", err)
			}
			if got != nil {
				t.Fatalf("返回 %v，期望 nil", got)
			}
		})
	}
}

// Test列出类方法遇到空条件返回空切片而非nil
//
// 两个方向都必须在缺少条件时短路：
// 空条件意味着"这次查询没有依据"，查库只会白白消耗一次往返。
func Test列出类方法遇到空条件返回空切片而非nil(t *testing.T) {
	ctx := context.Background()
	r := &RoleRepository{}
	tenantID := uuid.New().String()

	cases := map[string]func() ([]string, error){
		"ListPermissionIDs 缺租户": func() ([]string, error) {
			return r.ListPermissionIDs(ctx, "", uuid.New().String())
		},
		"ListPermissionIDs 缺角色": func() ([]string, error) {
			return r.ListPermissionIDs(ctx, tenantID, "")
		},
		"按权限反查缺租户": func() ([]string, error) {
			return r.ListRoleIDsByPermissionID(ctx, "", uuid.New().String())
		},
		"按权限反查缺权限": func() ([]string, error) {
			return r.ListRoleIDsByPermissionID(ctx, tenantID, "")
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if err != nil {
				t.Fatalf("空条件不是错误：%v", err)
			}
			if got == nil {
				t.Fatal("应返回空切片，而不是 nil")
			}
			if len(got) != 0 {
				t.Fatalf("返回 %v，期望为空", got)
			}
		})
	}
}

// Test角色模型与实体往返一致
//
// 转换函数成对维护，漏改一侧会悄悄丢字段；
// 这里锁住"转出去再转回来等于原值"。
func Test角色模型与实体往返一致(t *testing.T) {
	r, err := domainrole.NewCustomRole(uuid.New(), "auditor", "审计员", "负责审计")
	if err != nil {
		t.Fatalf("构造角色失败：%v", err)
	}

	got := toRoleRecord(r).toEntity()
	if *got != *r {
		t.Fatalf("往返后 = %+v，期望 %+v", got, r)
	}
}

// Test关联表名与领域约定一致
//
// TableName 是白纸黑字固定下来的表名：
// 一旦有人重命名实体而忘了同步这里，GORM 的默认复数推断会静悄悄换一张表。
func Test关联表名与领域约定一致(t *testing.T) {
	// if 语句的表达式里不能直接写复合字面量（大括号会与语句块冲突），
	// 因此先取变量再断言。
	var roleRec roleRecord
	var permissionRec rolePermissionRecord

	if roleRec.TableName() != "roles" {
		t.Fatalf("roles 表名 = %q", roleRec.TableName())
	}
	if permissionRec.TableName() != "role_permissions" {
		t.Fatalf("role_permissions 表名 = %q", permissionRec.TableName())
	}
}

// Test差分权限_只删不插
//
// 差分算反了（删掉该保留的、留下该删的）是这类实现最容易犯的错，
// 而它藏在数据库往返之间，真出问题时只能靠线上数据才发现——
// 因此差分计算被抽成纯函数 diffPermissions，在这里逐条分支锁死。
func Test差分权限_只删不插(t *testing.T) {
	kept := uuid.New().String()
	dropped := uuid.New().String()

	toRemove, toInsert := diffPermissions([]string{kept, dropped}, []string{kept})

	if len(toRemove) != 1 || toRemove[0] != dropped {
		t.Fatalf("待删除 = %v，期望 [%s]", toRemove, dropped)
	}
	if len(toInsert) != 0 {
		t.Fatalf("待插入 = %v，期望为空", toInsert)
	}
}

// Test差分权限_只插不删
func Test差分权限_只插不删(t *testing.T) {
	kept := uuid.New().String()
	fresh := uuid.New().String()

	toRemove, toInsert := diffPermissions([]string{kept}, []string{kept, fresh})

	if len(toRemove) != 0 {
		t.Fatalf("待删除 = %v，期望为空", toRemove)
	}
	if len(toInsert) != 1 || toInsert[0] != fresh {
		t.Fatalf("待插入 = %v，期望 [%s]", toInsert, fresh)
	}
}

// Test差分权限_零变更不产生任何增删
//
// 零变更的替换必须退化成零写入：保留下来的那部分关联 created_at 才不会被动过，
// 而"没有实际变化的一次替换"也不该在库上留下任何痕迹。
func Test差分权限_零变更不产生任何增删(t *testing.T) {
	a := uuid.New().String()
	b := uuid.New().String()

	toRemove, toInsert := diffPermissions([]string{a, b}, []string{b, a})

	if len(toRemove) != 0 {
		t.Fatalf("待删除 = %v，期望为空", toRemove)
	}
	if len(toInsert) != 0 {
		t.Fatalf("待插入 = %v，期望为空", toInsert)
	}
}

// Test差分权限_目标为空时删除全部
//
// 清空是合法操作：整体替换的语义是"传进来的集合就是最终结果"，
// 空集合的结果就是一条关联都不留。
func Test差分权限_目标为空时删除全部(t *testing.T) {
	a := uuid.New().String()
	b := uuid.New().String()

	toRemove, toInsert := diffPermissions([]string{a, b}, nil)

	if len(toRemove) != 2 {
		t.Fatalf("待删除 = %v，期望 2 项", toRemove)
	}
	if len(toInsert) != 0 {
		t.Fatalf("待插入 = %v，期望为空", toInsert)
	}
}

// Test差分权限_目标包含库中已有项时不重复插入
//
// 重复插入会撞上 idx_role_permissions_role_permission 唯一索引，
// 把一次"没有实际变化"的替换变成一次写失败。
func Test差分权限_目标包含库中已有项时不重复插入(t *testing.T) {
	a := uuid.New().String()

	toRemove, toInsert := diffPermissions([]string{a}, []string{a})

	if len(toRemove) != 0 {
		t.Fatalf("待删除 = %v，期望为空", toRemove)
	}
	if len(toInsert) != 0 {
		t.Fatalf("待插入 = %v，期望为空", toInsert)
	}
}

// Test创建角色撞唯一约束映射为ErrRoleExists
//
// Create 的错误映射是端口明文契约："命中 (tenant_id, key) 唯一约束时返回
// ErrRoleExists，不得静默吞掉后返回 nil"。
// 映射逻辑抽成纯函数 translateRoleWriteError，因此不需要连库就能验证。
func Test创建角色撞唯一约束映射为ErrRoleExists(t *testing.T) {
	role := newTestRoleEntity(t)

	err := translateRoleWriteError(gorm.ErrDuplicatedKey, role)
	if !errors.Is(err, domainrole.ErrRoleExists) {
		t.Fatalf("错误 = %v，期望归入 domainrole.ErrRoleExists", err)
	}
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("错误 %v 未归类到 domain.ErrAlreadyExists", err)
	}
	// 错误信息必须带定位信息：多租户环境下只说"已存在"无法排障。
	if !strings.Contains(err.Error(), role.TenantID) || !strings.Contains(err.Error(), role.Key) {
		t.Fatalf("错误信息 %q 未带上 tenant_id 与 key", err.Error())
	}
}

// Test创建角色撞CHECK约束映射为ErrInvalidRoleKey
func Test创建角色撞CHECK约束映射为ErrInvalidRoleKey(t *testing.T) {
	role := newTestRoleEntity(t)

	err := translateRoleWriteError(gorm.ErrCheckConstraintViolated, role)
	if !errors.Is(err, domainrole.ErrInvalidRoleKey) {
		t.Fatalf("错误 = %v，期望归入 domainrole.ErrInvalidRoleKey", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("错误 %v 未归类到 domain.ErrInvalidKey", err)
	}
}

// Test创建角色未知数据库错误原样上抛
//
// 不认识的错误不得被归进任何业务分类：把它说成"角色已存在"
// 会把调用方引向"换个 key 重试"这种无效操作。
func Test创建角色未知数据库错误原样上抛(t *testing.T) {
	role := newTestRoleEntity(t)
	raw := errors.New("连接被重置")

	err := translateRoleWriteError(raw, role)
	if !errors.Is(err, raw) {
		t.Fatalf("错误 = %v，期望原样保留底层错误", err)
	}
	if errors.Is(err, domain.ErrAlreadyExists) || errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("错误 %v 被误归类到业务分类上", err)
	}
}

// TestRequireUUID接受大小写与花括号写法并归一为规范形态
//
// uuid.Parse 同时接受大写与花括号写法（RFC 4122），
// 它们指向同一条记录。归一到小写无括号的规范形态，
// 读写两侧的口径才一致，否则"判空用一种形态、查询用另一种形态"
// 会让一次合法查询静默返回空集。
func TestRequireUUID接受大小写与花括号写法并归一为规范形态(t *testing.T) {
	valid := uuid.New()

	got, err := requireUUID(strings.ToUpper(valid.String()), domain.ErrInvalidRoleID)
	if err != nil {
		t.Fatalf("大写 UUID 被拒绝：%v", err)
	}
	if got != valid.String() {
		t.Fatalf("归一结果 = %q，期望 %q", got, valid.String())
	}

	got, err = requireUUID("{"+valid.String()+"}", domain.ErrInvalidRoleID)
	if err != nil {
		t.Fatalf("花括号写法被拒绝：%v", err)
	}
	if got != valid.String() {
		t.Fatalf("归一结果 = %q，期望 %q", got, valid.String())
	}
}

// Test读路径标识归一化
//
// 读路径必须与写路径用同一把尺子：
// 归一化之后再去查询，否则"判空用去空白后的值、查询却用原始值"
// 会让一次合法查询静默地返回空集。
func Test读路径标识归一化(t *testing.T) {
	valid := uuid.New()

	normalized, empty, err := normalizeLookupID("  "+valid.String()+"  ", domain.ErrInvalidTenantID)
	if err != nil {
		t.Fatalf("带空白的合法标识被拒绝：%v", err)
	}
	if empty {
		t.Fatal("合法标识不应被当作空值")
	}
	if normalized != valid.String() {
		t.Fatalf("归一结果 = %q，期望 %q", normalized, valid.String())
	}
}

// Test读路径空标识视为没有依据可查
//
// 空条件是"没有可查的东西"，不是错误：查库只会白白消耗一次往返，
// 更糟的是会把空字符串打进 UUID 列。
func Test读路径空标识视为没有依据可查(t *testing.T) {
	for _, input := range []string{"", "   "} {
		normalized, empty, err := normalizeLookupID(input, domain.ErrInvalidTenantID)
		if err != nil {
			t.Fatalf("空标识不应报错：%v", err)
		}
		if !empty {
			t.Fatalf("标识 %q 应被识别为空", input)
		}
		if normalized != "" {
			t.Fatalf("归一结果 = %q，期望空串", normalized)
		}
	}
}

// Test读路径非法标识显式报错
//
// 标识非空但非法是调用方 bug（两个标识传反了、或 UUID 拼错了），
// 静默当成"查不到"会把一次程序缺陷伪装成正常的业务结果。
// 口径与写路径 requireUUID 一致。
func Test读路径非法标识显式报错(t *testing.T) {
	cases := map[string]error{
		"非 UUID 字符串": domain.ErrInvalidTenantID,
		"零值 UUID":    domain.ErrInvalidTenantID,
	}

	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			input := "not-a-uuid"
			if name == "零值 UUID" {
				input = uuid.Nil().String()
			}

			_, empty, err := normalizeLookupID(input, invalid)
			if err == nil {
				t.Fatal("非法标识应报错")
			}
			if empty {
				t.Fatal("非法标识不应被当作空值")
			}
			if !errors.Is(err, invalid) {
				t.Fatalf("错误 = %v，期望归入 %v", err, invalid)
			}
		})
	}
}

// Test列出类方法对非法标识返回错误
//
// 这条用例锁住"读路径不把调用方 bug 伪装成查不到"这条契约：
// 适配层若把非法标识当成空条件短路成空切片，测试会立刻失败。
func Test列出类方法对非法标识返回错误(t *testing.T) {
	ctx := context.Background()
	r := &RoleRepository{}
	tenantID := uuid.New().String()
	roleID := uuid.New().String()

	cases := map[string]func() error{
		"ListPermissionIDs 非法租户": func() error {
			_, err := r.ListPermissionIDs(ctx, "not-a-uuid", roleID)
			return err
		},
		"ListPermissionIDs 非法角色": func() error {
			_, err := r.ListPermissionIDs(ctx, tenantID, "not-a-uuid")
			return err
		},
		"按权限反查 非法租户": func() error {
			_, err := r.ListRoleIDsByPermissionID(ctx, "not-a-uuid", roleID)
			return err
		},
		"按权限反查 非法权限": func() error {
			_, err := r.ListRoleIDsByPermissionID(ctx, tenantID, "not-a-uuid")
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

// newTestRoleEntity 构造一个用于错误映射测试的角色实体。
//
// 刻意避开领域构造函数：这里要验证的是适配层的错误翻译，
// 实体本身只需要一个合法的租户与标识。
func newTestRoleEntity(t *testing.T) *domainrole.Role {
	t.Helper()

	return &domainrole.Role{
		ID:       uuid.New().String(),
		TenantID: uuid.New().String(),
		Key:      "auditor",
		Name:     "审计员",
	}
}

// Test权限存在性校验_数量不符即失败
//
// 这条判定是"不留下孤儿行"这道防线的唯一落点：
// role_permissions 无外键，写入不存在的权限会造出一行脏数据，
// 让角色"持有"一项无法鉴权的权限，而调用方从返回值上看不出来。
func Test权限存在性校验_数量不符即失败(t *testing.T) {
	if err := verifyPermissionCount(3, 3); err != nil {
		t.Fatalf("数量相符时不应报错：%v", err)
	}

	err := verifyPermissionCount(2, 3)
	if !errors.Is(err, domain.ErrInvalidPermissionID) {
		t.Fatalf("错误 = %v，期望归入 domain.ErrInvalidPermissionID", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("错误 %v 未归类到 domain.ErrInvalidKey", err)
	}
	// 错误信息必须说清缺了几项，否则调用方无从判断要补哪些权限。
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("错误信息 %q 未包含缺失项数", err.Error())
	}
}

// Test权限存在性校验_全部存在时通过
func Test权限存在性校验_全部存在时通过(t *testing.T) {
	if err := verifyPermissionCount(0, 0); err != nil {
		t.Fatalf("空集合不应报错：%v", err)
	}
}

// Test角色归属校验_跨租户返回ErrRoleNotFound
//
// role_permissions 无外键兜底，跨租户写入的唯一防线就在这里：
// 角色不属于本租户时必须失败，不能写出跨租户的脏行。
func Test角色归属校验_跨租户返回ErrRoleNotFound(t *testing.T) {
	tenantID := uuid.New().String()
	roleID := uuid.New().String()

	err := roleNotFoundError(tenantID, roleID)
	if !errors.Is(err, domainrole.ErrRoleNotFound) {
		t.Fatalf("错误 = %v，期望归入 domainrole.ErrRoleNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 %v 未归类到 domain.ErrNotFound", err)
	}
	// 错误信息必须带定位信息：多租户环境下只说"不存在"无法排障。
	if !strings.Contains(err.Error(), tenantID) || !strings.Contains(err.Error(), roleID) {
		t.Fatalf("错误信息 %q 未带上 tenant_id 与 role_id", err.Error())
	}
}
