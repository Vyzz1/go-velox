package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

type fakeProvider struct {
	rule domain.Rule
	err  error
}

func (f fakeProvider) GetRule(context.Context, string, string) (domain.Rule, error) {
	return f.rule, f.err
}

type fakeStore struct {
	result    algorithm.Result
	err       error
	gotParams algorithm.Params
}

func (f *fakeStore) Check(_ context.Context, _ domain.CheckInput, p algorithm.Params) (algorithm.Result, error) {
	f.gotParams = p
	return f.result, f.err
}
func (f *fakeStore) Ping(context.Context) error { return nil }

func sampleRule() domain.Rule {
	return domain.Rule{
		Algorithm: algorithm.GCRA,
		Limit:     100,
		Period:    time.Minute,
		Burst:     10,
	}
}

func TestExecute_ResolveRuleError(t *testing.T) {
	uc := NewCheckLimit(&fakeStore{}, fakeProvider{err: errors.New("no such rule")}, FailOpen)

	_, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme", RuleID: "api"})
	if err == nil {
		t.Fatal("expected error when rule resolution fails")
	}
}

func TestExecute_AllowedMapsResultAndParams(t *testing.T) {
	store := &fakeStore{result: algorithm.Result{Allowed: true, Remaining: 7, ResetAtMs: 123}}
	uc := NewCheckLimit(store, fakeProvider{rule: sampleRule()}, FailOpen)

	got, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme", RuleID: "api", Cost: 3})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !got.Allowed || got.Reason != "allowed" {
		t.Errorf("got %+v, want allowed/\"allowed\"", got)
	}
	if got.Limit != 100 || got.Remaining != 7 {
		t.Errorf("limit/remaining = %d/%d, want 100/7", got.Limit, got.Remaining)
	}
	// Params are derived from the resolved rule + input cost.
	if store.gotParams.Limit != 100 || store.gotParams.Burst != 10 || store.gotParams.Cost != 3 {
		t.Errorf("params = %+v, want Limit100/Burst10/Cost3", store.gotParams)
	}
}

func TestExecute_DeniedReason(t *testing.T) {
	store := &fakeStore{result: algorithm.Result{Allowed: false, RetryAfterMs: 1000}}
	uc := NewCheckLimit(store, fakeProvider{rule: sampleRule()}, FailOpen)

	got, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme", RuleID: "api"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got.Allowed || got.Reason != "rate_limit_exceeded" {
		t.Errorf("got %+v, want denied/\"rate_limit_exceeded\"", got)
	}
}

// When the store is unreachable, the use case must NOT surface an error (which
// would become a 5xx); it applies the fail-mode and returns a clean decision.
func TestExecute_StoreUnreachable_FailOpen(t *testing.T) {
	store := &fakeStore{err: errors.New("redis down")}
	uc := NewCheckLimit(store, fakeProvider{rule: sampleRule()}, FailOpen)

	got, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme", RuleID: "api"})
	if err != nil {
		t.Fatalf("fail-open must not return an error, got %v", err)
	}
	if !got.Allowed || got.Reason != "degraded_fail_open" {
		t.Errorf("got %+v, want allowed/\"degraded_fail_open\"", got)
	}
	if got.Limit != 100 {
		t.Errorf("limit = %d, want 100", got.Limit)
	}
}

func TestExecute_StoreUnreachable_FailClosed(t *testing.T) {
	store := &fakeStore{err: errors.New("redis down")}
	uc := NewCheckLimit(store, fakeProvider{rule: sampleRule()}, FailClosed)

	got, err := uc.Execute(context.Background(), domain.CheckInput{TenantID: "acme", RuleID: "api"})
	if err != nil {
		t.Fatalf("fail-closed must not return an error, got %v", err)
	}
	if got.Allowed || got.Reason != "degraded_fail_closed" {
		t.Errorf("got %+v, want denied/\"degraded_fail_closed\"", got)
	}
	if got.RetryAfterMs != degradedRetryAfterMs {
		t.Errorf("retryAfterMs = %d, want %d", got.RetryAfterMs, degradedRetryAfterMs)
	}
}

func TestParseFailMode(t *testing.T) {
	cases := map[string]FailMode{"open": FailOpen, "closed": FailClosed, "CLOSED": FailClosed, "": FailOpen, "weird": FailOpen}
	for in, want := range cases {
		if got := ParseFailMode(in); got != want {
			t.Errorf("ParseFailMode(%q) = %v, want %v", in, got, want)
		}
	}
}
