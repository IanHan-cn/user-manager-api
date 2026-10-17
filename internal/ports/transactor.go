// internal/ports/transactor.go
package ports

import "context"

type Transactor interface {
	WithinTransaction(
		ctx context.Context,
		fn func(context.Context) error,
	) error
}
