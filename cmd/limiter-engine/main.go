package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	grpchandler "github.com/Vyzz1/go-velox.git/internal/engine/delivery/grpc"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
	"github.com/Vyzz1/go-velox.git/internal/engine/store"
	"github.com/Vyzz1/go-velox.git/internal/engine/usecase"
	"github.com/Vyzz1/go-velox.git/pkg/config"
	"github.com/Vyzz1/go-velox.git/pkg/logger"
	"github.com/Vyzz1/go-velox.git/pkg/metrics"
	"github.com/Vyzz1/go-velox.git/pkg/middleware"
	"github.com/Vyzz1/go-velox.git/pkg/tracer"
	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
	"google.golang.org/grpc/reflection"
)

type Config struct {
	config.Base
	GRPCAddr          string `env:"GRPC_ADDR"                    envDefault:":9090"`
	MetricsAddr       string `env:"METRICS_ADDR"                 envDefault:":9091"`
	OTLPEndpoint      string `env:"OTLP_ENDPOINT"                envDefault:"localhost:4317"`
	RedisAddrs        string `env:"REDIS_ADDRS"                  envDefault:"localhost:6379"`
	DefaultLimit      uint64 `env:"LIMITER_DEFAULT_LIMIT"        envDefault:"100"`
	DefaultPeriodSecs int64  `env:"LIMITER_DEFAULT_PERIOD_SECS"  envDefault:"60"`
	DefaultBurst      uint64 `env:"LIMITER_DEFAULT_BURST"        envDefault:"10"`
}

func main() {
	var cfg Config
	if err := config.Load(&cfg, "cmd/limiter-engine/.env"); err != nil {
		panic("config: " + err.Error())
	}

	log := logger.Must(cfg.LogLevel, cfg.LogFormat)
	defer log.Sync() //nolint:errcheck

	log.Info("starting limiter-engine",
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
		zap.String("grpc", cfg.GRPCAddr),
		zap.String("metrics", cfg.MetricsAddr),
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

	redisStore, err := store.New(splitTrim(cfg.RedisAddrs))
	if err != nil {
		log.Fatal("redis connection failed", zap.Error(err))
	}
	defer redisStore.Close() //nolint:errcheck

	rules := &domain.StaticProvider{Default: domain.Rule{
		Limit:  cfg.DefaultLimit,
		Period: time.Duration(cfg.DefaultPeriodSecs) * time.Second,
		Burst:  cfg.DefaultBurst,
	}}

	checkLimitUC := usecase.NewCheckLimit(redisStore, rules)
	healthUC := usecase.NewHealth(redisStore)
	srv := grpchandler.NewServer(checkLimitUC, healthUC, log)

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			middleware.UnaryRequestID(),
			middleware.UnaryLogging(log),
			middleware.UnaryRecovery(log),
		),
	)
	enginev1.RegisterLimiterEngineServiceServer(grpcServer, srv)
	reflection.Register(grpcServer)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatal("listen failed", zap.Error(err))
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("limiter-engine ready", zap.String("addr", cfg.GRPCAddr))
		if err := grpcServer.Serve(lis); err != nil {
			log.Error("grpc serve error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutting down limiter-engine...")

	grpcServer.GracefulStop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("metrics shutdown error", zap.Error(err))
	}
	if err := tracerShutdown(shutdownCtx); err != nil {
		log.Warn("tracer shutdown error", zap.Error(err))
	}

	log.Info("limiter-engine stopped")
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
