package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// FailMode is the policy applied when the counter backend cannot be reached for
// a request (Redis timeout, cluster failover window, CLUSTERDOWN, ...). A rate
// limiter sits in the request path, so how it fails matters more than that it
// fails — surface a clean allow/deny, never a 5xx.
type FailMode int

const (
	FailOpen   FailMode = iota // allow the request (availability-first; the default)
	FailClosed                 // deny the request (protection-first)
)

// degradedRetryAfterMs is a short backoff advertised on a fail-closed degrade —
// the outage is expected to be transient (e.g. a Redis failover), so clients
// should retry soon rather than wait a full rule period.
const degradedRetryAfterMs = 1000

// ParseFailMode reads the LIMITER_FAIL_MODE env value; anything but "closed"
// (case-insensitive) is treated as fail-open.
func ParseFailMode(s string) FailMode {
	if strings.EqualFold(strings.TrimSpace(s), "closed") {
		return FailClosed
	}
	return FailOpen
}

// CheckLimitUseCase evaluates one rate-limit decision.
type CheckLimitUseCase struct {
	store    domain.Store
	rules    domain.RuleProvider
	failMode FailMode
}

func NewCheckLimit(s domain.Store, r domain.RuleProvider, fm FailMode) *CheckLimitUseCase {
	return &CheckLimitUseCase{store: s, rules: r, failMode: fm}
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
		// The counter backend was unreachable for THIS request. Apply the
		// configured fail-mode so the caller gets a clean 200/429 instead of a
		// 5xx; degradations are counted separately so operators can alert on them.
		return uc.degraded(rule), nil
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

// degraded builds the decision returned when the store is unreachable, per the
// configured fail-mode. It records the degradation for alerting.
func (uc *CheckLimitUseCase) degraded(rule domain.Rule) domain.CheckResult {
	if uc.failMode == FailClosed {
		recordDegraded("closed")
		return domain.CheckResult{
			Allowed:      false,
			Limit:        rule.Limit,
			Remaining:    0,
			RetryAfterMs: degradedRetryAfterMs,
			Reason:       "degraded_fail_closed",
		}
	}
	recordDegraded("open")
	return domain.CheckResult{
		Allowed:   true,
		Limit:     rule.Limit,
		Remaining: rule.Limit,
		Reason:    "degraded_fail_open",
	}
}
