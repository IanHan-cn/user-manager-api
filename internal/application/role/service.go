package role

import (
	"github.com/memories-cn/user-management-api/internal/ports"
)

// 静态校验
// var _ ports.RoleRepository = (*RoleService)(nil)

type RoleService struct {
	roleRepo ports.RoleRepository
}

func NewRoleService(roleRepo ports.RoleRepository) *RoleService {
	return &RoleService{
		roleRepo: roleRepo,
	}
}

// func (s *RoleService) Create(ctx context.Context, r *domain.Role) error
// func (s *RoleService) FindByID(ctx context.Context, tenantID, roleID string) (*domain.Role, error)
// func (s *RoleService) FindByKey(ctx context.Context, tenantID, key string) (*domain.Role, error)
// func (s *RoleService) ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error
