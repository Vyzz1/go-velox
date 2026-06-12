package domain

import (
	"context"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
)

// Store is the rate-limit counter backend.
// Implementations must be atomic (Lua script or transaction).
type Store interface {
	// Check runs a GCRA evaluation. Key construction is an implementation detail.
	Check(ctx context.Context, in CheckInput, p algorithm.Params) (algorithm.Result, error)
	Ping(ctx context.Context) error
}
