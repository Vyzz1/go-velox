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
	"github.com/Vyzz1/go-velox.git/internal/syncagent/health"
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
	// Sidecar identity advertised to peers via gossip metadata. When this agent
	// is co-located with a limiter-engine, set Role=engine and EngineAddr to that
	// engine's gRPC address so the gateway can discover and route to it.
	Role       string `env:"ROLE"        envDefault:""`
	EngineAddr string `env:"ENGINE_ADDR" envDefault:""`

	// Engine health-check knobs (only used when Role=engine). Durations are in
	// milliseconds because the config loader treats time.Duration as a raw int.
	// The sidecar advertises healthy=false until the engine passes; it then
	// flips unhealthy after UnhealthyThreshold consecutive failures so the
	// gateway stops routing to a dead engine, and back after HealthyThreshold.
	HealthIntervalMs   int `env:"ENGINE_HEALTH_INTERVAL_MS"  envDefault:"2000"`
	HealthTimeoutMs    int `env:"ENGINE_HEALTH_TIMEOUT_MS"   envDefault:"1000"`
	UnhealthyThreshold int `env:"ENGINE_UNHEALTHY_THRESHOLD" envDefault:"3"`
	HealthyThreshold   int `env:"ENGINE_HEALTHY_THRESHOLD"   envDefault:"1"`
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
		zap.String("role", cfg.Role),
		zap.String("engine_addr", cfg.EngineAddr),
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
		Role:          cfg.Role,
		EngineAddr:    cfg.EngineAddr,
	}, log)
	if err != nil {
		log.Fatal("gossip init failed", zap.Error(err))
	}

	// Root context for background loops (join retry + health check); cancelled on
	// shutdown so both stop cleanly.
	ctx, cancelHealth := context.WithCancel(context.Background())
	defer cancelHealth()

	// Join the cluster in the background, retrying until a seed is reachable.
	// On k8s the seed's DNS is frequently not resolvable the instant we boot,
	// and a one-shot Join would leave this node isolated forever (see
	// docs/bugfix-syncagent-gossip-join-dns.md).
	go joinWithRetry(ctx, gossip, seeds, log)

	// When this agent is an engine sidecar, probe its local engine and tie the
	// result to the health we gossip — so the gateway drops a dead engine from
	// its hash ring without the sidecar itself having to leave the cluster.

	var engineProbe *health.EngineProbe
	if cfg.Role == "engine" && cfg.EngineAddr != "" {
		engineProbe, err = health.NewEngineProbe(cfg.EngineAddr)
		if err != nil {
			log.Fatal("engine probe init failed", zap.Error(err))
		}
		healthUC := usecase.NewHealthCheck(engineProbe, gossip, usecase.HealthCheckConfig{
			Interval:           time.Duration(cfg.HealthIntervalMs) * time.Millisecond,
			Timeout:            time.Duration(cfg.HealthTimeoutMs) * time.Millisecond,
			UnhealthyThreshold: cfg.UnhealthyThreshold,
			HealthyThreshold:   cfg.HealthyThreshold,
		}, log)
		go healthUC.Run(ctx)
		log.Info("engine health-check started",
			zap.String("engine_addr", cfg.EngineAddr),
			zap.Int("interval_ms", cfg.HealthIntervalMs),
		)
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

	// Stop probing the engine and release its connection before leaving gossip.
	cancelHealth()
	if engineProbe != nil {
		if err := engineProbe.Close(); err != nil {
			log.Warn("engine probe close error", zap.Error(err))
		}
	}

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

// joinWithRetry attempts to join the gossip cluster via the seeds, retrying with
// exponential backoff until at least one seed is contacted or ctx is cancelled.
// With no seeds this node is the founder and there is nothing to join. This makes
// convergence independent of seed-DNS readiness at boot and of Pod start order.
func joinWithRetry(ctx context.Context, gossip *cluster.Memberlist, seeds []string, log *zap.Logger) {
	if len(seeds) == 0 {
		return // founder node: no seeds to contact
	}
	const maxBackoff = 15 * time.Second
	backoff := time.Second
	for {
		n, err := gossip.Join(seeds)
		if err == nil && n > 0 {
			log.Info("joined cluster", zap.Int("contacted", n))
			return
		}
		log.Warn("join cluster failed, retrying",
			zap.Error(err), zap.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
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
