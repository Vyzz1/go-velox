package metrics

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// Server is a minimal HTTP server that exposes /metrics for Prometheus scraping.
type Server struct {
	srv *http.Server
	log *zap.Logger
}

// New creates a metrics server bound to addr.
func New(addr string, log *zap.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return &Server{
		srv: &http.Server{Addr: addr, Handler: mux},
		log: log,
	}
}

// Start begins serving in a background goroutine.
func (s *Server) Start() {
	go func() {
		s.log.Info("metrics server listening", zap.String("addr", s.srv.Addr))
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("metrics server error", zap.Error(err))
		}
	}()
}

// Shutdown gracefully drains the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
