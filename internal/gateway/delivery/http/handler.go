package httpdelivery

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
	"github.com/Vyzz1/go-velox.git/pkg/middleware"
)

// checker is the gateway use case the handler depends on.
type checker interface {
	Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error)
}

type checkRequest struct {
	TenantID string            `json:"tenant_id"`
	Subject  string            `json:"subject"`
	Resource string            `json:"resource"`
	Action   string            `json:"action"`
	RuleID   string            `json:"rule_id"`
	Cost     uint32            `json:"cost"`
	Metadata map[string]string `json:"metadata"`
}

type checkResponse struct {
	Allowed       bool   `json:"allowed"`
	Limit         uint64 `json:"limit"`
	Remaining     uint64 `json:"remaining"`
	ResetAtUnixMs int64  `json:"reset_at_unix_ms"`
	RetryAfterMs  int64  `json:"retry_after_ms"`
	Reason        string `json:"reason"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Router builds the gateway HTTP handler with request-ID and logging middleware.
func Router(uc checker, log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/check", checkHandler(uc, log))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var h http.Handler = mux
	h = middleware.HTTPLogging(log)(h)
	h = middleware.HTTPRequestID()(h)
	return h
}

func checkHandler(uc checker, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req checkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json body"})
			return
		}
		if req.TenantID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "tenant_id is required"})
			return
		}

		res, err := uc.Execute(r.Context(), domain.CheckInput{
			TenantID: req.TenantID,
			Subject:  req.Subject,
			Resource: req.Resource,
			Action:   req.Action,
			RuleID:   req.RuleID,
			Cost:     req.Cost,
			Metadata: req.Metadata,
		})
		if err != nil {
			log.Error("rate-limit check failed",
				zap.String("tenant_id", req.TenantID),
				zap.String("subject", req.Subject),
				zap.Error(err),
			)
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "rate-limit service unavailable"})
			return
		}

		// Standard rate-limit headers on every decision.
		w.Header().Set("X-RateLimit-Limit", strconv.FormatUint(res.Limit, 10))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatUint(res.Remaining, 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(res.ResetAtMs, 10))

		status := http.StatusOK
		if !res.Allowed {
			status = http.StatusTooManyRequests
			// Retry-After is expressed in seconds, rounded up (min 1).
			retryAfterSecs := max((res.RetryAfterMs+999)/1000, 1)
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSecs, 10))
		}

		writeJSON(w, status, checkResponse{
			Allowed:       res.Allowed,
			Limit:         res.Limit,
			Remaining:     res.Remaining,
			ResetAtUnixMs: res.ResetAtMs,
			RetryAfterMs:  res.RetryAfterMs,
			Reason:        res.Reason,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
