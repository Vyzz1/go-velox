// Package repo implements config-service storage ports against Postgres,
// wrapping the sqlc-generated queries.
package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vyzz1/go-velox.git/internal/configsvc/db"
	"github.com/Vyzz1/go-velox.git/internal/configsvc/domain"
)

// PostgresRuleStore implements domain.RuleStore over a pgx pool.
type PostgresRuleStore struct {
	q *db.Queries
}

func NewPostgres(pool *pgxpool.Pool) *PostgresRuleStore {
	return &PostgresRuleStore{q: db.New(pool)}
}

func (s *PostgresRuleStore) Upsert(ctx context.Context, r domain.Rule) (domain.Rule, error) {
	row, err := s.q.UpsertRule(ctx, db.UpsertRuleParams{
		TenantID:   r.TenantID,
		RuleID:     r.RuleID,
		Algorithm:  r.Algorithm,
		LimitCount: int64(r.Limit),
		PeriodSecs: int64(r.PeriodSecs),
		Burst:      int64(r.Burst),
	})
	if err != nil {
		return domain.Rule{}, fmt.Errorf("repo: upsert rule: %w", err)
	}
	return toDomain(row), nil
}

func (s *PostgresRuleStore) Get(ctx context.Context, tenantID, ruleID string) (domain.Rule, error) {
	row, err := s.q.GetRule(ctx, db.GetRuleParams{TenantID: tenantID, RuleID: ruleID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Rule{}, domain.ErrNotFound
		}
		return domain.Rule{}, fmt.Errorf("repo: get rule: %w", err)
	}
	return toDomain(row), nil
}

func (s *PostgresRuleStore) ListByTenant(ctx context.Context, tenantID string) ([]domain.Rule, error) {
	rows, err := s.q.ListRulesByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("repo: list rules by tenant: %w", err)
	}
	return toDomainSlice(rows), nil
}

func (s *PostgresRuleStore) ListAll(ctx context.Context) ([]domain.Rule, error) {
	rows, err := s.q.ListAllRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("repo: list all rules: %w", err)
	}
	return toDomainSlice(rows), nil
}

func (s *PostgresRuleStore) Delete(ctx context.Context, tenantID, ruleID string) (bool, error) {
	n, err := s.q.DeleteRule(ctx, db.DeleteRuleParams{TenantID: tenantID, RuleID: ruleID})
	if err != nil {
		return false, fmt.Errorf("repo: delete rule: %w", err)
	}
	return n > 0, nil
}

func toDomain(r db.Rule) domain.Rule {
	return domain.Rule{
		TenantID:   r.TenantID,
		RuleID:     r.RuleID,
		Algorithm:  r.Algorithm,
		Limit:      uint64(r.LimitCount),
		PeriodSecs: uint64(r.PeriodSecs),
		Burst:      uint64(r.Burst),
	}
}

func toDomainSlice(rows []db.Rule) []domain.Rule {
	out := make([]domain.Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDomain(r))
	}
	return out
}
