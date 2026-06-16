// Package usecase orchestrates config-service rule operations: it writes the
// durable copy to the store (Postgres) first, then mirrors the change to the
// publisher (etcd) so the engine hot-reloads.
package usecase

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/configsvc/domain"
)

// RuleUseCase coordinates the store (source of truth) and publisher (notify).
type RuleUseCase struct {
	store domain.RuleStore
	pub   domain.RulePublisher
	log   *zap.Logger
}

func NewRule(store domain.RuleStore, pub domain.RulePublisher, log *zap.Logger) *RuleUseCase {
	return &RuleUseCase{store: store, pub: pub, log: log}
}

// ValidationError signals a bad input the caller should map to HTTP 400.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

func validate(r domain.Rule) error {
	if r.TenantID == "" {
		return ValidationError{"tenant_id is required"}
	}
	if r.RuleID == "" {
		return ValidationError{"rule_id is required"}
	}
	switch r.Algorithm {
	case domain.AlgorithmGCRA, domain.AlgorithmSlidingWindow:
	case "":
		return ValidationError{"algorithm is required"}
	default:
		return ValidationError{fmt.Sprintf("unknown algorithm %q", r.Algorithm)}
	}
	if r.Limit == 0 {
		return ValidationError{"limit must be > 0"}
	}
	if r.PeriodSecs == 0 {
		return ValidationError{"period_secs must be > 0"}
	}
	return nil
}

// Upsert persists the rule then mirrors it to etcd. The rule is durable once
// the store call returns; a publish failure is surfaced so the caller knows
// the engine has not yet hot-reloaded (startup reconcile heals any drift).
func (uc *RuleUseCase) Upsert(ctx context.Context, r domain.Rule) (domain.Rule, error) {
	if err := validate(r); err != nil {
		return domain.Rule{}, err
	}
	saved, err := uc.store.Upsert(ctx, r)
	if err != nil {
		return domain.Rule{}, err
	}
	if err := uc.pub.Publish(ctx, saved); err != nil {
		uc.log.Error("rule persisted but etcd publish failed",
			zap.String("tenant", saved.TenantID), zap.String("rule", saved.RuleID), zap.Error(err))
		return saved, fmt.Errorf("publish rule: %w", err)
	}
	return saved, nil
}

func (uc *RuleUseCase) Get(ctx context.Context, tenantID, ruleID string) (domain.Rule, error) {
	return uc.store.Get(ctx, tenantID, ruleID)
}

func (uc *RuleUseCase) ListByTenant(ctx context.Context, tenantID string) ([]domain.Rule, error) {
	return uc.store.ListByTenant(ctx, tenantID)
}

// Delete removes the rule from the store and etcd. Returns false if absent.
func (uc *RuleUseCase) Delete(ctx context.Context, tenantID, ruleID string) (bool, error) {
	removed, err := uc.store.Delete(ctx, tenantID, ruleID)
	if err != nil {
		return false, err
	}
	if !removed {
		return false, nil
	}
	if err := uc.pub.Remove(ctx, tenantID, ruleID); err != nil {
		uc.log.Error("rule deleted but etcd remove failed",
			zap.String("tenant", tenantID), zap.String("rule", ruleID), zap.Error(err))
		return true, fmt.Errorf("remove rule from etcd: %w", err)
	}
	return true, nil
}

// Reconcile republishes every stored rule to etcd. Run at startup so etcd
// reflects the durable store even after an etcd data loss or drift.
func (uc *RuleUseCase) Reconcile(ctx context.Context) error {
	rules, err := uc.store.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if err := uc.pub.Publish(ctx, r); err != nil {
			return fmt.Errorf("reconcile %s/%s: %w", r.TenantID, r.RuleID, err)
		}
	}
	uc.log.Info("reconciled rules to etcd", zap.Int("count", len(rules)))
	return nil
}
