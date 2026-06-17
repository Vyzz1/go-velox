// Package httpdelivery exposes the sync-agent's read-only membership API.
package httpdelivery

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/syncagent/domain"
	"github.com/Vyzz1/go-velox.git/pkg/middleware"
)

// membership is the use case the handler depends on (interface at point of use).
type membership interface {
	List() []domain.Member
	Local() domain.Member
}

type memberResponse struct {
	ID    string `json:"id"`
	Addr  string `json:"addr"`
	State string `json:"state"`
	Local bool   `json:"local"`
}

type membersResponse struct {
	Local   string           `json:"local"`
	Count   int              `json:"count"`
	Members []memberResponse `json:"members"`
}

// Router builds the sync-agent HTTP handler with request-ID and logging middleware.
func Router(uc membership, log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/members", membersHandler(uc))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var h http.Handler = mux
	h = middleware.HTTPLogging(log)(h)
	h = middleware.HTTPRequestID()(h)
	return h
}

func membersHandler(uc membership) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		members := uc.List()
		out := make([]memberResponse, 0, len(members))
		for _, m := range members {
			out = append(out, memberResponse{
				ID:    m.ID,
				Addr:  m.Addr,
				State: string(m.State),
				Local: m.Local,
			})
		}
		writeJSON(w, http.StatusOK, membersResponse{
			Local:   uc.Local().ID,
			Count:   len(out),
			Members: out,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
