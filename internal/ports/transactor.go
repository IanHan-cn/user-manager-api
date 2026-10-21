// internal/ports/transactor.go
package ports

import "context"

// Transactor 事务边界。
//
// Login / Refresh / Register 都涉及多次写（创建 Session、更新轮换摘要、写凭证），
// 必须落在同一个事务里。
//
// 关键约束：Core 绝不依赖 *gorm.DB。由 adapters/gorm 提供实现，
// 并通过 context 把事务句柄传给各 Repository（见 adapters/gorm.dBFromContext）。
type Transactor interface {
	WithinTransaction(ctx context.Context, fn func(context.Context) error) error
}
