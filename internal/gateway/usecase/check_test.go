package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
)

// stubLimiter records the input it received and returns canned results.
type stubLimiter struct {
	gotInput domain.CheckInput
	result   domain.CheckResult
	err      error
}

func (s *stubLimiter) Check(_ context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	s.gotInput = in
	return s.result, s.err
}

func TestExecute_DefaultsRuleIDAndCost(t *testing.T) {
	stub := &stubLimiter{result: domain.CheckResult{Allowed: true}}
	uc := NewCheck(stub)

	if _, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme"}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stub.gotInput.RuleID != defaultRuleID {
		t.Errorf("RuleID = %q, want %q", stub.gotInput.RuleID, defaultRuleID)
	}
	if stub.gotInput.Cost != 1 {
		t.Errorf("Cost = %d, want 1", stub.gotInput.Cost)
	}
}

func TestExecute_PreservesProvidedValues(t *testing.T) {
	stub := &stubLimiter{result: domain.CheckResult{Allowed: false, Reason: "rate_limit_exceeded"}}
	uc := NewCheck(stub)

	got, err := uc.Execute(context.Background(), domain.CheckInput{
		TenantID: "acme",
		RuleID:   "api",
		Cost:     5,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stub.gotInput.RuleID != "api" {
		t.Errorf("RuleID = %q, want unchanged %q", stub.gotInput.RuleID, "api")
	}
	if stub.gotInput.Cost != 5 {
		t.Errorf("Cost = %d, want unchanged 5", stub.gotInput.Cost)
	}
	if got.Allowed || got.Reason != "rate_limit_exceeded" {
		t.Errorf("result not passed through: %+v", got)
	}
}

func TestExecute_WrapsLimiterError(t *testing.T) {
	sentinel := errors.New("engine unreachable")
	uc := NewCheck(&stubLimiter{err: sentinel})

	_, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrap of %v", err, sentinel)
	}
}
