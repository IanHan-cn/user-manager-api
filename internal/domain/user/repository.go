// internal/domain/user/repository.go
package user

import (
	"context"
)

// Repository 是用户聚合的持久化端口，由适配层实现。
type Repository interface {
	// FindByID 按主键查询用户，未命中返回 (nil, nil)。
	FindByID(ctx context.Context, id string) (*User, error)
}
