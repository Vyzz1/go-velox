package client

import (
	"context"
	"fmt"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

// EngineClient adapts the limiter-engine gRPC client to the gateway's
// domain.Limiter port, using a Consistent Hashing router to dynamically select connections.
type EngineClient struct {
	router *Router
}

// New wraps an existing consistent hash router.
func New(router *Router) *EngineClient {
	return &EngineClient{router: router}
}

func (c *EngineClient) Check(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	rpc, err := c.router.GetClient(in.TenantID)
	if err != nil {
		return domain.CheckResult{}, fmt.Errorf("router GetClient: %w", err)
	}

	resp, err := rpc.CheckLimit(ctx, &enginev1.CheckLimitRequest{
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
