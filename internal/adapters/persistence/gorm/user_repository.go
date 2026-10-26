// internal/adapters/persistence/gorm/user_repository.go
package gorm

import (
	"context"
	"errors"

	domainuser "github.com/memories-cn/user-management-api/internal/domain/user"
	"gorm.io/gorm"
)

var ErrMissingDB = errors.New("gorm database is required")

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(
	db *gorm.DB,
) (*UserRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}

	return &UserRepository{db: db}, nil
}

var _ domainuser.Repository = (*UserRepository)(nil)

func (r *UserRepository) FindByID(ctx context.Context, id string) (*domainuser.User, error) {
	var user domainuser.User
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Where("id = ?", id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}
