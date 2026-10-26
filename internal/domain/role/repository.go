// internal/domain/role/repository.go
package role

import (
	"context"
)

// Repository 提供租户角色与角色权限的持久化能力，由适配层实现。
//
// 所有方法都必须带 tenant_id 条件：角色不得跨租户使用。
type Repository interface {
	// Create 创建角色。
	Create(ctx context.Context, r *Role) error
	// FindByID 查询指定租户下的角色，未命中返回 (nil, nil)。
	FindByID(ctx context.Context, tenantID, roleID string) (*Role, error)
	// FindByKey 查询指定租户下指定标识的角色，未命中返回 (nil, nil)。
	FindByKey(ctx context.Context, tenantID, key string) (*Role, error)
	// ReplacePermissions 用给定权限集合整体替换角色的权限关联。
	ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error
}
