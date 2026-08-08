package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vyzz1/go-velox.git/internal/gateway/domain"
	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

// RouteMode selects the consistent-hash key used to pick an engine. Because the
// rate-limit counters live in Redis (engines are stateless), any engine returns
// the same decision, so routing only affects which engine does the work.
type RouteMode int

const (
	// RouteByTenant hashes on tenant_id: a tenant is sticky to one engine (the
	// default). Simple, but a single hot tenant concentrates load on one engine.
	RouteByTenant RouteMode = iota
	// RouteByTenantSubject hashes on tenant_id|subject, spreading a hot tenant's
	// traffic across the fleet. Note: it spreads engine load, not the Redis
	// hot-key (the counter is still one key on one master).
	RouteByTenantSubject
)

// ParseRouteMode reads the ROUTE_KEY env value; anything but "tenant_subject"
// (case-insensitive) is treated as tenant-sticky routing.
func ParseRouteMode(s string) RouteMode {
	if strings.EqualFold(strings.TrimSpace(s), "tenant_subject") {
		return RouteByTenantSubject
	}
	return RouteByTenant
}

// routingKey builds the consistent-hash key for a request under the given mode.
// It falls back to tenant-only when the subject is empty so an unset subject
// does not collapse every request onto a single ring point.
func routingKey(mode RouteMode, in domain.CheckInput) string {
	if mode == RouteByTenantSubject && in.Subject != "" {
		return in.TenantID + "|" + in.Subject
	}
	return in.TenantID
}

// EngineClient adapts the limiter-engine gRPC client to the gateway's
// domain.Limiter port, using a Consistent Hashing router to dynamically select connections.
type EngineClient struct {
	router *Router
	mode   RouteMode
}

// New wraps an existing consistent hash router with the given routing mode.
func New(router *Router, mode RouteMode) *EngineClient {
	return &EngineClient{router: router, mode: mode}
}

func (c *EngineClient) Check(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error) {
	rpc, err := c.router.GetClient(routingKey(c.mode, in))
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
