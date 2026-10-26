// internal/ports/user_repository.go
package ports

import (
	"context"

	"github.com/memories-cn/user-management-api/internal/domain"
)

type UserRepository interface {
	FindByID(ctx context.Context, id string) (*domain.User, error)
}

// RoleRepository 提供租户角色与角色权限的持久化能力。
//
// 所有方法都必须带 tenant_id 条件：角色不得跨租户使用。
type RoleRepository interface {
	// Create 创建角色。
	Create(ctx context.Context, r *domain.Role) error
	// FindByID 查询指定租户下的角色，未命中返回 (nil, nil)。
	FindByID(ctx context.Context, tenantID, roleID string) (*domain.Role, error)
	// FindByKey 查询指定租户下指定标识的角色，未命中返回 (nil, nil)。
	FindByKey(ctx context.Context, tenantID, key string) (*domain.Role, error)
	// ReplacePermissions 用给定权限集合整体替换角色的权限关联。
	ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error
}

// PermissionRepository 提供系统权限目录的只读访问。
//
// 权限集合由系统迁移维护，因此本接口不提供写入能力：租户只能在既有集合内组合角色。
type PermissionRepository interface {
	// ListAll 返回系统权限目录。
	ListAll(ctx context.Context) ([]*domain.Permission, error)
	// FindByCode 依据权限代码查询，未命中返回 (nil, nil)。
	FindByCode(ctx context.Context, code string) (*domain.Permission, error)
	// FindByCodes 批量查询权限，返回顺序与入参一致；任一代码不存在即返回错误，
	// 避免把不存在的权限静默写入角色。
	FindByCodes(ctx context.Context, codes []string) ([]*domain.Permission, error)
}

// PolicyPermission 是生成 Casbin p 规则所需的权限二元组。
type PolicyPermission struct {
	// Resource 是受控资源，例如 member。
	Resource string
	// Action 是资源上的操作，例如 invite。
	Action string
}

// PolicyRepository 维护 Casbin 策略在数据库中的投影（casbin_rule 表）与全局版本号。
//
// 关键约定：本接口的写入**只能**发生在业务事务内（实现必须走 dbFrom(ctx)），
// 从而保证"业务关系表 + 策略投影 + 版本号"的原子性（见 docs/TDD.md 8.3）。
// 绝不能在事务外调用 enforcer 的写入接口，否则事务回滚后策略会残留。
type PolicyRepository interface {
	// ReplaceRoleRules 用给定权限集合整体替换某角色在本租户的 p 规则。
	ReplaceRoleRules(ctx context.Context, tenantID, roleID string, perms []PolicyPermission) error
}
