// internal/adapters/persistence/gorm/user_repository.go
package gorm

import (
	"context"
	"errors"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/ports"
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

var _ ports.UserRepository = (*UserRepository)(nil)

func (r *UserRepository) FindByID(ctx context.Context, id string) (*domain.User, error) {
	var user domain.User
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &user, nil
}
