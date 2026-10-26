package platformsetup

import "github.com/memories-cn/user-management-api/internal/domain"

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
