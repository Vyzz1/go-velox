package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// CheckLimitUseCase evaluates one rate-limit decision.
type CheckLimitUseCase struct {
	store domain.Store
	rules domain.RuleProvider
}

func NewCheckLimit(s domain.Store, r domain.RuleProvider) *CheckLimitUseCase {
	return &CheckLimitUseCase{store: s, rules: r}
}

func (uc *CheckLimitUseCase) Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	rule, err := uc.rules.GetRule(ctx, in.TenantID, in.RuleID)
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("check_limit: resolve rule %q: %w", in.RuleID, err)
	}

	params := algorithm.Params{
		Algorithm: rule.Algorithm,
		Limit:     rule.Limit,
		Period:    rule.Period,
		Burst:     rule.Burst,
		Cost:      uint64(in.Cost),
	}

	start := time.Now()
	res, err := uc.store.Check(ctx, in, params)
	if err != nil {
		redisErrors.Inc()
		return domain.CheckResult{}, fmt.Errorf("check_limit: gcra: %w", err)
	}
	recordCheck(in.RuleID, res.Allowed, time.Since(start))

	reason := "allowed"
	if !res.Allowed {
		reason = "rate_limit_exceeded"
	}

	return domain.CheckResult{
		Allowed:      res.Allowed,
		Limit:        rule.Limit,
		Remaining:    res.Remaining,
		ResetAtMs:    res.ResetAtMs,
		RetryAfterMs: res.RetryAfterMs,
		Reason:       reason,
	}, nil
}
