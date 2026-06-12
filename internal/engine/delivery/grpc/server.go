package grpchandler

import (
	"context"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

type checkLimiter interface {
	Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error)
}

type healthChecker interface {
	Execute(ctx context.Context) bool
}

// Server implements the gRPC LimiterEngineService.
type Server struct {
	enginev1.UnimplementedLimiterEngineServiceServer
	checkLimit checkLimiter
	health     healthChecker
	log        *zap.Logger
}

func NewServer(cl checkLimiter, hc healthChecker, log *zap.Logger) *Server {
	return &Server{checkLimit: cl, health: hc, log: log}
}

func (s *Server) CheckLimit(ctx context.Context, req *enginev1.CheckLimitRequest) (*enginev1.CheckLimitResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.RuleId == "" {
		return nil, status.Error(codes.InvalidArgument, "rule_id is required")
	}

	result, err := s.checkLimit.Execute(ctx, domain.CheckInput{
		TenantID:   req.TenantId,
		RuleID:     req.RuleId,
		Subject:    req.Subject,
		Resource:   req.Resource,
		Action:     req.Action,
		Cost:       req.Cost,
		Attributes: req.Attributes,
	})
	if err != nil {
		s.log.Error("check limit failed",
			zap.String("tenant_id", req.TenantId),
			zap.String("rule_id", req.RuleId),
			zap.String("subject", req.Subject),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "rate limit check failed")
	}

	return &enginev1.CheckLimitResponse{
		Allowed:       result.Allowed,
		Limit:         result.Limit,
		Remaining:     result.Remaining,
		ResetAtUnixMs: result.ResetAtMs,
		RetryAfterMs:  result.RetryAfterMs,
		Reason:        result.Reason,
	}, nil
}

func (s *Server) HealthCheck(ctx context.Context, _ *enginev1.HealthCheckRequest) (*enginev1.HealthCheckResponse, error) {
	if !s.health.Execute(ctx) {
		return &enginev1.HealthCheckResponse{Status: "degraded"}, nil
	}
	return &enginev1.HealthCheckResponse{Status: "ok"}, nil
}
