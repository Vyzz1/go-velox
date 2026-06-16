package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	httpdelivery "github.com/Vyzz1/go-velox.git/internal/configsvc/delivery/http"
	"github.com/Vyzz1/go-velox.git/internal/configsvc/publisher"
	"github.com/Vyzz1/go-velox.git/internal/configsvc/repo"
	"github.com/Vyzz1/go-velox.git/internal/configsvc/usecase"
	"github.com/Vyzz1/go-velox.git/pkg/config"
	"github.com/Vyzz1/go-velox.git/pkg/logger"
	"github.com/Vyzz1/go-velox.git/pkg/metrics"
	"github.com/Vyzz1/go-velox.git/pkg/tracer"
)

type Config struct {
	config.Base
	HTTPAddr      string `env:"HTTP_ADDR"      envDefault:":8081"`
	MetricsAddr   string `env:"METRICS_ADDR"   envDefault:":8082"`
	OTLPEndpoint  string `env:"OTLP_ENDPOINT"  envDefault:"localhost:4317"`
	DatabaseURL   string `env:"DATABASE_URL"   envDefault:"postgres://velox:velox@localhost:5432/velox?sslmode=disable"`
	EtcdEndpoints string `env:"ETCD_ENDPOINTS" envDefault:"localhost:2379"`
	EtcdPrefix    string `env:"ETCD_PREFIX"    envDefault:"/velox/rules/"`
}

func main() {
	var cfg Config
	if err := config.Load(&cfg, "cmd/config-service/.env"); err != nil {
		panic("config: " + err.Error())
	}

	log := logger.Must(cfg.LogLevel, cfg.LogFormat)
	defer log.Sync() //nolint:errcheck

	log.Info("starting config-service",
		zap.String("service", cfg.ServiceName),
		zap.String("env", cfg.Environment),
		zap.String("http", cfg.HTTPAddr),
		zap.String("metrics", cfg.MetricsAddr),
		zap.String("etcd_prefix", cfg.EtcdPrefix),
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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("postgres connection failed", zap.Error(err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal("postgres ping failed", zap.Error(err))
	}

	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   splitTrim(cfg.EtcdEndpoints),
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatal("etcd connection failed", zap.Error(err))
	}
	defer etcdClient.Close() //nolint:errcheck

	store := repo.NewPostgres(pool)
	pub := publisher.NewEtcd(etcdClient, cfg.EtcdPrefix)
	ruleUC := usecase.NewRule(store, pub, log)

	// Heal any drift between the durable store and etcd at startup.
	reconcileCtx, cancelReconcile := context.WithTimeout(ctx, 10*time.Second)
	if err := ruleUC.Reconcile(reconcileCtx); err != nil {
		log.Warn("startup reconcile failed", zap.Error(err))
	}
	cancelReconcile()

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpdelivery.Router(ruleUC, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("config-service ready", zap.String("addr", cfg.HTTPAddr))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http serve error", zap.Error(err))
		}
	}()

	<-quit
	log.Info("shutting down config-service...")

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

	log.Info("config-service stopped")
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
