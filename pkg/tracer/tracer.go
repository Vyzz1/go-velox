package tracer

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds tracing initialisation parameters.
type Config struct {
	ServiceName  string
	Environment  string
	OTLPEndpoint string // gRPC endpoint, e.g. "localhost:4317"; empty string disables tracing
}

// ShutdownFunc flushes and closes the TracerProvider.
type ShutdownFunc func(context.Context) error

// Init initialises the global OpenTelemetry TracerProvider and wires the OTLP
// gRPC exporter to the configured collector (Jaeger in this project).
//
// Call the returned ShutdownFunc on service exit to flush pending spans.
// If OTLPEndpoint is empty, Init is a no-op and returns a no-op shutdown.
func Init(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if cfg.OTLPEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	conn, err := grpc.NewClient(cfg.OTLPEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("tracer: dial %s: %w", cfg.OTLPEndpoint, err)
	}

	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("tracer: otlp exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("tracer: resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		// AlwaysSample is fine for development; tune to TraceIDRatioBased in production.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	// W3C TraceContext + Baggage propagation so spans stitch across service boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
