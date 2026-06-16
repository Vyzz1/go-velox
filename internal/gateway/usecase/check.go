package usecase

import (
	"context"
	"fmt"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
)

// defaultRuleID is applied when a client omits rule_id; the engine requires a
// non-empty rule_id and StaticProvider returns the same default rule regardless.
const defaultRuleID = "default"

// CheckUseCase orchestrates a single rate-limit decision by forwarding to the
// limiter-engine. The gateway owns no limiter state of its own.
type CheckUseCase struct {
	limiter domain.Limiter
}

func NewCheck(l domain.Limiter) *CheckUseCase {
	return &CheckUseCase{limiter: l}
}

func (uc *CheckUseCase) Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	if in.RuleID == "" {
		in.RuleID = defaultRuleID
	}
	if in.Cost < 1 {
		in.Cost = 1
	}

	res, err := uc.limiter.Check(ctx, in)
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("gateway check: %w", err)
	}
	return res, nil
}
