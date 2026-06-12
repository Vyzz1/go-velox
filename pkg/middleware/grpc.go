package middleware

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryLogging logs every gRPC unary call with method, duration, request ID,
// and — when a span is active — trace_id and span_id for log/trace correlation.
// Run this after UnaryRequestID so request_id is available in ctx.
func UnaryLogging(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)

		fields := []zap.Field{
			zap.String("method", info.FullMethod),
			zap.Duration("duration", time.Since(start)),
		}
		if id := RequestIDFromContext(ctx); id != "" {
			fields = append(fields, zap.String("request_id", id))
		}
		if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
			fields = append(fields,
				zap.String("trace_id", sc.TraceID().String()),
				zap.String("span_id", sc.SpanID().String()),
			)
		}
		if err != nil {
			fields = append(fields, zap.Error(err))
			log.Warn("grpc call failed", fields...)
		} else {
			log.Info("grpc call", fields...)
		}
		return resp, err
	}
}

// UnaryRequestID injects a request ID into ctx, sourced from incoming metadata
// (x-request-id) or freshly generated.
func UnaryRequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := uuid.NewString()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if vals := md.Get("x-request-id"); len(vals) > 0 {
				id = vals[0]
			}
		}
		return handler(context.WithValue(ctx, requestIDKey, id), req)
	}
}

// UnaryRecovery catches panics inside handlers and converts them to an
// Internal gRPC status error.
func UnaryRecovery(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in grpc handler",
					zap.Any("panic", r),
					zap.String("method", info.FullMethod),
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
