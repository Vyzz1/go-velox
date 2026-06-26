package httpdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
)

type stubChecker struct {
	result domain.CheckResult
	err    error
}

func (s stubChecker) Execute(context.Context, domain.CheckInput) (domain.CheckResult, error) {
	return s.result, s.err
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCheck_Allowed(t *testing.T) {
	h := Router(stubChecker{result: domain.CheckResult{
		Allowed: true, Limit: 100, Remaining: 99, ResetAtMs: 123, Reason: "allowed",
	}}, zap.NewNop())

	rec := post(h, `{"tenant_id":"acme"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "100" {
		t.Errorf("X-RateLimit-Limit = %q, want 100", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "99" {
		t.Errorf("X-RateLimit-Remaining = %q, want 99", got)
	}
	var body checkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.Allowed {
		t.Errorf("body.Allowed = false, want true")
	}
}

func TestCheck_Denied(t *testing.T) {
	h := Router(stubChecker{result: domain.CheckResult{
		Allowed: false, Limit: 100, Remaining: 0, RetryAfterMs: 1500, Reason: "rate_limit_exceeded",
	}}, zap.NewNop())

	rec := post(h, `{"tenant_id":"acme"}`)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	// 1500ms rounds up to 2 seconds.
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
}

func TestCheck_MissingTenantID(t *testing.T) {
	h := Router(stubChecker{result: domain.CheckResult{Allowed: true}}, zap.NewNop())

	rec := post(h, `{"subject":"user-1"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCheck_InvalidJSON(t *testing.T) {
	h := Router(stubChecker{}, zap.NewNop())

	rec := post(h, `{not json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCheck_EngineError(t *testing.T) {
	h := Router(stubChecker{err: errors.New("engine unreachable")}, zap.NewNop())

	rec := post(h, `{"tenant_id":"acme"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
