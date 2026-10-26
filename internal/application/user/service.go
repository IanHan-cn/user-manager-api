// internal/application/user/service.go
package user

import (
	"errors"

	domainuser "github.com/memories-cn/user-management-api/internal/domain/user"
	"github.com/memories-cn/user-management-api/internal/ports"
)

var ErrMissingRepository = errors.New("user repository is required")

type Service struct {
	repo domainuser.Repository
	tx   ports.Transactor
}

func NewService(repo domainuser.Repository, tx ports.Transactor) (*Service, error) {
	if repo == nil {
		return nil, ErrMissingRepository
	}
	if tx == nil {
		return nil, errors.New("transactor is required")
	}

	return &Service{repo: repo, tx: tx}, nil
}
