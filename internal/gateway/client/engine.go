package client

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

// EngineClient adapts the limiter-engine gRPC client to the gateway's
// domain.Limiter port, translating between domain types and the engine wire
// contract.
type EngineClient struct {
	rpc enginev1.LimiterEngineServiceClient
}

// New wraps an existing gRPC connection to the limiter-engine.
func New(conn *grpc.ClientConn) *EngineClient {
	return &EngineClient{rpc: enginev1.NewLimiterEngineServiceClient(conn)}
}

func (c *EngineClient) Check(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	resp, err := c.rpc.CheckLimit(ctx, &enginev1.CheckLimitRequest{
		TenantId:   in.TenantID,
		Subject:    in.Subject,
		Resource:   in.Resource,
		Action:     in.Action,
		RuleId:     in.RuleID,
		Cost:       in.Cost,
		Attributes: in.Metadata,
	})
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("engine CheckLimit: %w", err)
	}

	return domain.CheckResult{
		Allowed:      resp.Allowed,
		Limit:        resp.Limit,
		Remaining:    resp.Remaining,
		ResetAtMs:    resp.ResetAtUnixMs,
		RetryAfterMs: resp.RetryAfterMs,
		Reason:       resp.Reason,
	}, nil
}
