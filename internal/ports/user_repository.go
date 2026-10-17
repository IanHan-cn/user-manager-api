// internal/ports/user_repository.go
package ports

import (
	"context"

	"github.com/memories-cn/user-management-api/internal/domain"
)

type UserRepository interface {
	FindByID(ctx context.Context, id string) (*domain.User, error)
}
