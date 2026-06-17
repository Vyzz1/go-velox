// Command sync-agent runs a gossip membership node for the govelox cluster.
// It discovers peers and detects failures via SWIM (hashicorp/memberlist) over
// UDP/TCP, and exposes a read-only HTTP view of the membership. It deliberately
// stays off the rate-limit request path.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/syncagent/cluster"
	httpdelivery "github.com/Vyzz1/go-velox.git/internal/syncagent/delivery/http"
	"github.com/Vyzz1/go-velox.git/internal/syncagent/usecase"
	"github.com/Vyzz1/go-velox.git/pkg/config"
	"github.com/Vyzz1/go-velox.git/pkg/logger"
	"github.com/Vyzz1/go-velox.git/pkg/metrics"
)

type Config struct {
	config.Base
	NodeID        string `env:"NODE_ID"               envDefault:""`
	GossipBind    string `env:"GOSSIP_BIND_ADDR"      envDefault:"0.0.0.0"`
	GossipPort    int    `env:"GOSSIP_PORT"           envDefault:"7070"`
	AdvertiseAddr string `env:"GOSSIP_ADVERTISE_ADDR" envDefault:""`
	AdvertisePort int    `env:"GOSSIP_ADVERTISE_PORT" envDefault:"0"`
	Seeds         string `env:"SEEDS"                 envDefault:""`
	HTTPAddr      string `env:"HTTP_ADDR"             envDefault:":7072"`
	MetricsAddr   string `env:"METRICS_ADDR"          envDefault:":7071"`
}

func main() {
	var cfg Config
	if err := config.Load(&cfg, "cmd/sync-agent/.env"); err != nil {
		panic("config: " + err.Error())
	}

	log := logger.Must(cfg.LogLevel, cfg.LogFormat)
	defer log.Sync() //nolint:errcheck

	// Default the node ID to the hostname so each container is unique.
	if cfg.NodeID == "" {
		if hn, err := os.Hostname(); err == nil {
			cfg.NodeID = hn
		}
	}

	seeds := splitTrim(cfg.Seeds)

	log.Info("starting sync-agent",
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
		zap.String("node_id", cfg.NodeID),
		zap.String("gossip_bind", cfg.GossipBind),
		zap.Int("gossip_port", cfg.GossipPort),
		zap.String("http", cfg.HTTPAddr),
		zap.String("metrics", cfg.MetricsAddr),
		zap.Strings("seeds", seeds),
	)

	metricsSrv := metrics.New(cfg.MetricsAddr, log)
	metricsSrv.Start()

	gossip, err := cluster.New(cluster.Config{
		NodeID:        cfg.NodeID,
		BindAddr:      cfg.GossipBind,
		BindPort:      cfg.GossipPort,
		AdvertiseAddr: cfg.AdvertiseAddr,
		AdvertisePort: cfg.AdvertisePort,
		Seeds:         seeds,
	}, log)
	if err != nil {
		log.Fatal("gossip init failed", zap.Error(err))
	}

	if n, err := gossip.Join(seeds); err != nil {
		// Not fatal: the node still runs and peers can join it later.
		log.Warn("join cluster failed", zap.Error(err))
	} else if n > 0 {
		log.Info("joined cluster", zap.Int("contacted", n))
	}

	membershipUC := usecase.NewMembership(gossip)
	handler := httpdelivery.Router(membershipUC, log)

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("sync-agent ready", zap.String("addr", cfg.HTTPAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http serve error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutting down sync-agent...")

	// Broadcast an intentional leave so peers mark us "left" promptly.
	if err := gossip.Leave(5 * time.Second); err != nil {
		log.Warn("gossip leave error", zap.Error(err))
	}
	if err := gossip.Shutdown(); err != nil {
		log.Warn("gossip shutdown error", zap.Error(err))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown error", zap.Error(err))
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("metrics shutdown error", zap.Error(err))
	}

	log.Info("sync-agent stopped")
}

func splitTrim(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if v := strings.TrimSpace(r); v != "" {
			out = append(out, v)
		}
	}
	return out
}
