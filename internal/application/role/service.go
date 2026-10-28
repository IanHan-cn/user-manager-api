// Package role 承载角色相关用例的骨架：当前只有服务结构与构造，方法尚未落地。
package role

import (
	domainrole "github.com/memories-cn/user-management-api/internal/domain/role"
)

// 静态校验
// var _ domainrole.Repository = (*RoleService)(nil)

type RoleService struct {
	roleRepo domainrole.Repository
}

func NewRoleService(roleRepo domainrole.Repository) *RoleService {
	return &RoleService{
		roleRepo: roleRepo,
	}
}

// func (s *RoleService) Create(ctx context.Context, r *domainrole.Role) error
// func (s *RoleService) FindByID(ctx context.Context, tenantID, roleID string) (*domainrole.Role, error)
// func (s *RoleService) FindByKey(ctx context.Context, tenantID, key string) (*domainrole.Role, error)
// func (s *RoleService) ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error
//
// 其中 domainrole 指向 internal/domain/role 包。
