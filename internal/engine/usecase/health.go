package usecase

import "context"

type pinger interface {
	Ping(ctx context.Context) error
}

// HealthUseCase checks whether the backing store is reachable.
type HealthUseCase struct {
	store pinger
}

func NewHealth(s pinger) *HealthUseCase {
	return &HealthUseCase{store: s}
}

func (uc *HealthUseCase) Execute(ctx context.Context) bool {
	return uc.store.Ping(ctx) == nil
}
