package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/gateway/client"
	httpdelivery "github.com/Vyzz1/go-velox.git/internal/gateway/delivery/http"
	"github.com/Vyzz1/go-velox.git/internal/gateway/usecase"
	"github.com/Vyzz1/go-velox.git/pkg/config"
	"github.com/Vyzz1/go-velox.git/pkg/logger"
	"github.com/Vyzz1/go-velox.git/pkg/metrics"
	"github.com/Vyzz1/go-velox.git/pkg/tracer"
)

type Config struct {
	config.Base
	HTTPAddr     string `env:"HTTP_ADDR"     envDefault:":8080"`
	MetricsAddr  string `env:"METRICS_ADDR"  envDefault:":8090"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT"   envDefault:"localhost:4317"`
	SyncAgentURL string `env:"SYNC_AGENT_URL"  envDefault:"http://localhost:7072"`
	EnginePort   string `env:"ENGINE_PORT"     envDefault:"9090"`
}

func main() {
	var cfg Config
	if err := config.Load(&cfg, "cmd/api-gateway/.env"); err != nil {
		panic("config: " + err.Error())
	}

	log := logger.Must(cfg.LogLevel, cfg.LogFormat)
	defer log.Sync() //nolint:errcheck

	log.Info("starting api-gateway",
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
		zap.String("http", cfg.HTTPAddr),
		zap.String("metrics", cfg.MetricsAddr),
		zap.String("sync_agent", cfg.SyncAgentURL),
		zap.String("engine_port", cfg.EnginePort),
		zap.String("otlp", cfg.OTLPEndpoint),
	)

	ctx, cancelPoller := context.WithCancel(context.Background())
	defer cancelPoller()
	tracerShutdown, err := tracer.Init(ctx, tracer.Config{
		ServiceName:  cfg.ServiceName,
		Environment:  cfg.Environment,
		OTLPEndpoint: cfg.OTLPEndpoint,
	})
	if err != nil {
		log.Fatal("tracer init failed", zap.Error(err))
	}

	metricsSrv := metrics.New(cfg.MetricsAddr, log)
	metricsSrv.Start()

	// Initialize consistent hash router
	router := client.NewRouter()

	// Initial fallback (in case sync-agent is slow/down, we at least try localhost)
	router.UpdateMembers([]string{"localhost:" + cfg.EnginePort})

	// Start sync poller to periodically fetch engine nodes
	poller := client.NewSyncPoller(router, cfg.SyncAgentURL, 5*time.Second, log)
	go poller.Start(ctx)

	engine := client.New(router)
	checkUC := usecase.NewCheck(engine)
	handler := httpdelivery.Router(checkUC, log)

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("api-gateway ready", zap.String("addr", cfg.HTTPAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http serve error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutting down api-gateway...")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()

	cancelPoller() // Stop the sync poller

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown error", zap.Error(err))
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("metrics shutdown error", zap.Error(err))
	}
	if err := tracerShutdown(shutdownCtx); err != nil {
		log.Warn("tracer shutdown error", zap.Error(err))
	}

	log.Info("api-gateway stopped")
}
