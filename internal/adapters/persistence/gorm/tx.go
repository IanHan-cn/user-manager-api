package gorm

import (
	"context"

	"github.com/memories-cn/user-management-api/internal/ports"
	"gorm.io/gorm"
)

// 编译期断言：确保实现 ports.Transactor。
var _ ports.Transactor = (*Transactor)(nil)

// txKey 是事务句柄在上下文中的键，使用私有类型避免冲突。
type txKey struct{}

// Transactor 用 GORM 事务实现 ports.Transactor。
//
// 事务句柄通过 context 传递给各个 Repository，因此 service 层
// 完全不需要知道 GORM 的存在。
type Transactor struct {
	db *gorm.DB
}

// NewTransactor 创建事务管理器。
func NewTransactor(db *gorm.DB) *Transactor {
	return &Transactor{db: db}
}

// WithinTransaction 在单个事务中执行 fn。fn 返回错误则回滚。
func (t *Transactor) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if fn == nil {
		return nil
	}

	return t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(withDB(ctx, tx))
	})
}

// withDB 把 *gorm.DB（通常是事务句柄）放进 context。
func withDB(ctx context.Context, db *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, db)
}

// dBFromContext 取出 context 中的 *gorm.DB，不存在时返回 fallback。
func dBFromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if ctx == nil {
		return fallback
	}
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}

	return fallback
}
