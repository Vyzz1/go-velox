package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:"localhost:4317"`
	EngineAddr   string `env:"ENGINE_ADDR"   envDefault:"localhost:9090"`
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
		zap.String("engine", cfg.EngineAddr),
		zap.String("otlp", cfg.OTLPEndpoint),
	)

	ctx := context.Background()
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

	// gRPC connection to limiter-engine; otelgrpc stats handler stitches the
	// gateway→engine call into a single distributed trace.
	engineConn, err := grpc.NewClient(cfg.EngineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		log.Fatal("engine dial failed", zap.Error(err))
	}
	defer engineConn.Close() //nolint:errcheck

	engine := client.New(engineConn)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
