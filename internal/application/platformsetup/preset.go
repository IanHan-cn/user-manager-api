package platformsetup

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/ports"
)

// systemRolePreset 描述一个系统预置角色的默认展示信息与默认权限集合。
type systemRolePreset struct {
	key         string
	name        string
	description string
	permissions []string
}

// systemRolePresets 是租户创建时预置的系统角色与权限矩阵。
//
// 权限只能取自系统权限目录（docs/TDD.md 3.5）；租户管理员后续可在该集合内调整，
// 但不能新建可执行权限。矩阵可在 Milestone 5 按产品反馈调整。
var systemRolePresets = []systemRolePreset{
	{
		key:         domain.KeyOwner,
		name:        "所有者",
		description: "租户所有者，可管理租户及成员",
		permissions: []string{
			"tenant:read", "tenant:update", "tenant:delete",
			"member:read", "member:invite", "member:update", "member:remove",
			"role:read", "role:create", "role:update", "role:delete", "role:assign",
			"permission:read", "permission:assign",
			"audit:read",
		},
	},
	{
		key:         domain.KeyAdmin,
		name:        "管理员",
		description: "租户管理员，可管理大部分租户资源",
		permissions: []string{
			"tenant:read", "tenant:update",
			"member:read", "member:invite", "member:update", "member:remove",
			"role:read", "role:create", "role:update", "role:delete", "role:assign",
			"permission:read",
			"audit:read",
		},
	},
	{
		key:         domain.KeyMember,
		name:        "成员",
		description: "普通成员，可执行日常操作",
		permissions: []string{"tenant:read", "member:read", "role:read", "permission:read"},
	},
	{
		key:         domain.KeyViewer,
		name:        "只读",
		description: "只读用户，仅可查看资源",
		permissions: []string{"tenant:read", "member:read", "role:read", "permission:read"},
	},
}

type platformsetup struct {
	roleRepo       ports.RoleRepository
	permissionRepo ports.PermissionRepository
	policiesRepo   ports.PolicyRepository
}

func NewPlatformSetup(roleRepo ports.RoleRepository, permissionRepo ports.PermissionRepository, policiesRepo ports.PolicyRepository) *platformsetup {
	return &platformsetup{
		roleRepo:       roleRepo,
		permissionRepo: permissionRepo,
		policiesRepo:   policiesRepo,
	}
}

// ErrPresetPermissionMissing 表示预置权限不在系统权限目录中。
var ErrPresetPermissionMissing = errors.New("预置权限缺失于系统权限目录")

// EnsureSystemRoles 为租户预置系统角色与默认权限矩阵。
//
// 该方法**不开启事务、不递增策略版本号**：调用方（创建租户用例）负责把它们
// 与租户创建纳入同一个事务，避免出现"有租户但无角色"的中间状态。
//
// 幂等：已存在的角色会被跳过，不会覆盖管理员后续调整过的权限。
func (s *platformsetup) EnsureSystemRoles(ctx context.Context, tenantID uuid.UUID) error {
	if tenantID == uuid.Nil() {
		return errors.New("预置系统角色缺少租户标识")
	}

	for _, preset := range systemRolePresets {
		existing, err := s.roleRepo.FindByKey(ctx, tenantID.String(), preset.key)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}

		rl, err := domain.NewSystemRole(tenantID, preset.key, preset.name, preset.description)
		if err != nil {
			return fmt.Errorf("构造预置角色 %s 失败：%w", preset.key, err)
		}
		if err := s.roleRepo.Create(ctx, rl); err != nil {
			return fmt.Errorf("创建预置角色 %s 失败：%w", preset.key, err)
		}

		perms, err := s.lookupPermissions(ctx, preset.permissions)
		if err != nil {
			return fmt.Errorf("解析预置角色 %s 的权限失败：%w", preset.key, err)
		}
		if err := s.applyPermissions(ctx, tenantID.String(), rl.ID, perms); err != nil {
			return fmt.Errorf("绑定预置角色 %s 的权限失败：%w", preset.key, err)
		}
	}
	return nil
}

// lookupPermissions 依据权限代码查询目录项，任一缺失即返回错误。
func (s *platformsetup) lookupPermissions(ctx context.Context, codes []string) ([]*domain.Permission, error) {
	if err := domain.ValidateCodes(codes); err != nil {
		return nil, err
	}

	normalized := normalizeCodes(codes)
	perms, err := s.permissionRepo.FindByCodes(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if len(perms) != len(normalized) {
		return nil, ErrPresetPermissionMissing
	}
	return perms, nil
}

// normalizeCodes 归一化并去重权限代码；非法代码被丢弃（前置的 ValidateCodes 已拦截）。
func normalizeCodes(codes []string) []string {
	seen := make(map[string]struct{}, len(codes))
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		resource, action, err := domain.ParseCode(code)
		if err != nil {
			continue
		}
		normalized := domain.BuildCode(resource, action)
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

// applyPermissions 写入角色权限关联与策略投影，供事务内复用。
func (s *platformsetup) applyPermissions(ctx context.Context, tenantID, roleID string, perms []*domain.Permission) error {
	ids := make([]string, 0, len(perms))
	policyPerms := make([]ports.PolicyPermission, 0, len(perms))
	for _, p := range perms {
		ids = append(ids, p.ID)
		policyPerms = append(policyPerms, ports.PolicyPermission{Resource: p.Resource, Action: p.Action})
	}

	if err := s.roleRepo.ReplacePermissions(ctx, tenantID, roleID, ids); err != nil {
		return err
	}
	return s.policiesRepo.ReplaceRoleRules(ctx, tenantID, roleID, policyPerms)
}
