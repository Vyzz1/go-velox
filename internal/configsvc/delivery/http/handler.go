// Package httpdelivery exposes the config-service REST API for rule management.
package httpdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/configsvc/domain"
	"github.com/Vyzz1/go-velox.git/internal/configsvc/usecase"
	"github.com/Vyzz1/go-velox.git/pkg/middleware"
)

// rules is the use case the handler depends on.
type rules interface {
	Upsert(ctx context.Context, r domain.Rule) (domain.Rule, error)
	Get(ctx context.Context, tenantID, ruleID string) (domain.Rule, error)
	ListByTenant(ctx context.Context, tenantID string) ([]domain.Rule, error)
	Delete(ctx context.Context, tenantID, ruleID string) (bool, error)
}

type ruleBody struct {
	Algorithm  string `json:"algorithm"`
	Limit      uint64 `json:"limit"`
	PeriodSecs uint64 `json:"period_secs"`
	Burst      uint64 `json:"burst"`
}

type ruleResponse struct {
	TenantID   string `json:"tenant_id"`
	RuleID     string `json:"rule_id"`
	Algorithm  string `json:"algorithm"`
	Limit      uint64 `json:"limit"`
	PeriodSecs uint64 `json:"period_secs"`
	Burst      uint64 `json:"burst"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Router builds the config-service HTTP handler with request-ID and logging middleware.
func Router(uc rules, log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/tenants/{tenant}/rules/{rule}", upsertHandler(uc, log))
	mux.HandleFunc("GET /v1/tenants/{tenant}/rules/{rule}", getHandler(uc))
	mux.HandleFunc("GET /v1/tenants/{tenant}/rules", listHandler(uc))
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/rules/{rule}", deleteHandler(uc, log))

	var h http.Handler = mux
	h = middleware.HTTPLogging(log)(h)
	h = middleware.HTTPRequestID()(h)
	return h
}

func upsertHandler(uc rules, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body ruleBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json body"})
			return
		}
		rule := domain.Rule{
			TenantID:   r.PathValue("tenant"),
			RuleID:     r.PathValue("rule"),
			Algorithm:  body.Algorithm,
			Limit:      body.Limit,
			PeriodSecs: body.PeriodSecs,
			Burst:      body.Burst,
		}
		saved, err := uc.Upsert(r.Context(), rule)
		if err != nil {
			var verr usecase.ValidationError
			if errors.As(err, &verr) {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: verr.Msg})
				return
			}
			log.Error("upsert rule failed",
				zap.String("tenant", rule.TenantID), zap.String("rule", rule.RuleID), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save rule"})
			return
		}
		writeJSON(w, http.StatusOK, toResponse(saved))
	}
}

func getHandler(uc rules) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rule, err := uc.Get(r.Context(), r.PathValue("tenant"), r.PathValue("rule"))
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "rule not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to fetch rule"})
			return
		}
		writeJSON(w, http.StatusOK, toResponse(rule))
	}
}

func listHandler(uc rules) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := uc.ListByTenant(r.Context(), r.PathValue("tenant"))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list rules"})
			return
		}
		out := make([]ruleResponse, 0, len(list))
		for _, rule := range list {
			out = append(out, toResponse(rule))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func deleteHandler(uc rules, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, rule := r.PathValue("tenant"), r.PathValue("rule")
		removed, err := uc.Delete(r.Context(), tenant, rule)
		if err != nil {
			log.Error("delete rule failed",
				zap.String("tenant", tenant), zap.String("rule", rule), zap.Error(err))
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete rule"})
			return
		}
		if !removed {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "rule not found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func toResponse(r domain.Rule) ruleResponse {
	return ruleResponse{
		TenantID:   r.TenantID,
		RuleID:     r.RuleID,
		Algorithm:  r.Algorithm,
		Limit:      r.Limit,
		PeriodSecs: r.PeriodSecs,
		Burst:      r.Burst,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
