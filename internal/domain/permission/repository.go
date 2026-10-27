// internal/domain/permission/repository.go
package permission

import (
	"context"
)

// Repository 提供系统权限目录的只读访问，由适配层实现。
//
// 权限集合由系统维护，因此本接口刻意不提供写入能力：租户只能在既有集合内组合角色。
// 写入一端单独收敛在 Writer 端口，只有系统初始化的 Seeder 会用到；
// 两者分开是为了让"谁有权改权限"在类型层面就说得清，而不是只靠注释约束。
type Repository interface {
	// ListAll 返回系统权限目录。
	ListAll(ctx context.Context) ([]*Permission, error)
	// FindByCode 依据权限代码查询，未命中返回 (nil, nil)。
	FindByCode(ctx context.Context, code string) (*Permission, error)
	// FindByCodes 批量查询权限，返回顺序与入参一致；任一代码不存在即返回错误，
	// 避免把不存在的权限静默写入角色。
	FindByCodes(ctx context.Context, codes []string) ([]*Permission, error)
}
