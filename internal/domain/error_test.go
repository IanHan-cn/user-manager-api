package domain_test

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membership"
	"github.com/memories-cn/user-management-api/internal/domain/permission"
	"github.com/memories-cn/user-management-api/internal/domain/role"
	"github.com/memories-cn/user-management-api/internal/domain/tenant"
	"github.com/memories-cn/user-management-api/internal/domain/user"
)

// 本文件守护错误分类的契约：
//   - 各聚合的领域错误都必须挂到 domain 的分类树上；
//   - 跨聚合共用的概念必须指向同一个错误值；
//   - 状态类错误与参数类错误互不串类。

// Test字段类错误统一归入 ErrInvalidKey
func Test字段类错误统一归入ErrInvalidKey(t *testing.T) {
	cases := map[string]error{
		"domain.ErrInvalidTenantID":    domain.ErrInvalidTenantID,
		"domain.ErrInvalidUserID":      domain.ErrInvalidUserID,
		"domain.ErrInvalidActor":       domain.ErrInvalidActor,
		"domain.ErrInvalidPlanCode":    domain.ErrInvalidPlanCode,
		"domain.ErrInvalidSlug":        domain.ErrInvalidSlug,
		"domain.ErrInvalidName":        domain.ErrInvalidName,
		"role.ErrInvalidRoleKey":       role.ErrInvalidRoleKey,
		"role.ErrNotSystemKey":         role.ErrNotSystemKey,
		"role.ErrReservedSystemKey":    role.ErrReservedSystemKey,
		"user.ErrInvalidZitadelUserID": user.ErrInvalidZitadelUserID,
		"user.ErrInvalidUsername":      user.ErrInvalidUsername,
		"user.ErrInvalidEmail":         user.ErrInvalidEmail,
		"user.ErrInvalidPhone":         user.ErrInvalidPhone,
		"user.ErrInvalidAvatarURL":     user.ErrInvalidAvatarURL,
		"user.ErrInvalidUserStatus":    user.ErrInvalidUserStatus,
		"user.ErrInvalidUserRecord":    user.ErrInvalidUserRecord,
		"permission.ErrInvalidCode":    permission.ErrInvalidCode,
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("%s 未归类到 domain.ErrInvalidKey", name)
			}
		})
	}
}

// Test状态类错误与参数类错误互不串类
func Test状态类错误与参数类错误互不串类(t *testing.T) {
	if !errors.Is(membership.ErrInvalidStatusTransition, domain.ErrInvalidState) {
		t.Fatal("membership.ErrInvalidStatusTransition 未归类到 domain.ErrInvalidState")
	}
	if errors.Is(membership.ErrInvalidStatusTransition, domain.ErrInvalidKey) {
		t.Fatal("状态类错误不应同时归类到 ErrInvalidKey")
	}
}

// Test租户标识错误跨包共用同一个值
func Test租户标识错误跨包共用同一个值(t *testing.T) {
	if _, err := role.NewCustomRole(uuid.Nil(), "reader", "只读", ""); !errors.Is(err, domain.ErrInvalidTenantID) {
		t.Fatalf("role 的租户标识错误 = %v，期望 domain.ErrInvalidTenantID", err)
	}
	if _, err := membership.New(uuid.Nil(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrInvalidTenantID) {
		t.Fatalf("membership 的租户标识错误 = %v，期望 domain.ErrInvalidTenantID", err)
	}
}

// Test操作者错误跨包共用同一个值
//
// tenant 的创建者与 membership 的邀请人是同一个"操作者"概念。
func Test操作者错误跨包共用同一个值(t *testing.T) {
	if _, err := tenant.NewTenant("acme", "Acme", "   "); !errors.Is(err, domain.ErrInvalidActor) {
		t.Fatalf("tenant 创建者错误 = %v，期望 domain.ErrInvalidActor", err)
	}
	if _, err := membership.New(uuid.New(), uuid.New(), uuid.Nil()); !errors.Is(err, domain.ErrInvalidActor) {
		t.Fatalf("membership 邀请人错误 = %v，期望 domain.ErrInvalidActor", err)
	}
}

// Test用户标识错误可跨包复用
func Test用户标识错误可跨包复用(t *testing.T) {
	if _, err := membership.New(uuid.New(), uuid.Nil(), uuid.New()); !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("membership 用户标识错误 = %v，期望 domain.ErrInvalidUserID", err)
	}
}

// Test套餐标识错误指向共用定义
func Test套餐标识错误指向共用定义(t *testing.T) {
	if _, err := tenant.NewTenant("acme", "Acme", "u-1", tenant.WithPlanCode("bad plan!")); !errors.Is(err, domain.ErrInvalidPlanCode) {
		t.Fatalf("套餐标识错误 = %v，期望 domain.ErrInvalidPlanCode", err)
	}
}

// Test写操作结局错误归入独立分类
//
// "重复"与"不存在"描述的是写操作的结局：入参本身合法，状态机也没有拒绝，
// 因此单独成类，上层才能把它们与 400 类的参数错误区分开。
func Test写操作结局错误归入独立分类(t *testing.T) {
	if !errors.Is(domain.ErrAlreadyExists, domain.ErrConflict) {
		t.Fatal("domain.ErrAlreadyExists 未归类到 domain.ErrConflict")
	}
	if !errors.Is(membership.ErrMembershipExists, domain.ErrAlreadyExists) {
		t.Fatal("membership.ErrMembershipExists 未归类到 domain.ErrAlreadyExists")
	}
	if !errors.Is(membership.ErrMembershipNotFound, domain.ErrNotFound) {
		t.Fatal("membership.ErrMembershipNotFound 未归类到 domain.ErrNotFound")
	}
	if !errors.Is(user.ErrUserExists, domain.ErrAlreadyExists) {
		t.Fatal("user.ErrUserExists 未归类到 domain.ErrAlreadyExists")
	}
	if !errors.Is(user.ErrUserNotFound, domain.ErrNotFound) {
		t.Fatal("user.ErrUserNotFound 未归类到 domain.ErrNotFound")
	}
}

// Test写操作结局错误不串到参数类与状态类
func Test写操作结局错误不串到参数类与状态类(t *testing.T) {
	cases := map[string]error{
		"domain.ErrAlreadyExists":          domain.ErrAlreadyExists,
		"membership.ErrMembershipExists":   membership.ErrMembershipExists,
		"membership.ErrMembershipNotFound": membership.ErrMembershipNotFound,
		"user.ErrUserExists":               user.ErrUserExists,
		"user.ErrUserNotFound":             user.ErrUserNotFound,
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("%s 不应归类到 domain.ErrInvalidKey", name)
			}
			if errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("%s 不应归类到 domain.ErrInvalidState", name)
			}
		})
	}
}

// Test空实体错误不混入业务分类
//
// 空实体是调用方 bug，不是业务规则拒绝了请求。
// 它只挂在 domain.ErrNilEntity 上，任何业务分类的 errors.Is 都必须为 false：
// 否则上层按业务分类返回 4xx，会把程序缺陷当成正常结果处理。
func Test空实体错误不混入业务分类(t *testing.T) {
	nilEntities := map[string]error{
		"membership.ErrNilMembership": membership.ErrNilMembership,
		"user.ErrNilUser":             user.ErrNilUser,
	}
	for name, err := range nilEntities {
		if !errors.Is(err, domain.ErrNilEntity) {
			t.Fatalf("%s 未归类到 domain.ErrNilEntity", name)
		}
	}

	business := map[string]error{
		"ErrInvalidKey":    domain.ErrInvalidKey,
		"ErrInvalidState":  domain.ErrInvalidState,
		"ErrConflict":      domain.ErrConflict,
		"ErrNotFound":      domain.ErrNotFound,
		"ErrAlreadyExists": domain.ErrAlreadyExists,
	}

	for name, base := range business {
		t.Run(name, func(t *testing.T) {
			for entityName, err := range nilEntities {
				if errors.Is(err, base) {
					t.Fatalf("%s 不应归类到 %s", entityName, name)
				}
			}
		})
	}
}

// Test名称上限只有一个来源
//
// 名称上限曾经在错误文案与 role、tenant 两个包里各写一份 255，
// 现在统一引用 domain.MaxNameLen：改一处即可，文案不再可能与校验脱节。
func Test名称上限只有一个来源(t *testing.T) {
	if domain.MaxNameLen != 255 {
		t.Fatalf("domain.MaxNameLen = %d，期望 255", domain.MaxNameLen)
	}
	if !strings.Contains(domain.ErrInvalidName.Error(), domain.ErrInvalidKey.Error()) {
		t.Fatalf("ErrInvalidName = %q，应以 ErrInvalidKey 为基类", domain.ErrInvalidName)
	}
	if strings.Contains(domain.ErrInvalidName.Error(), "255") {
		t.Fatalf("ErrInvalidName = %q，不应把长度上限写死在文案里", domain.ErrInvalidName)
	}
}
