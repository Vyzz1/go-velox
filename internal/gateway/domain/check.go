package domain

import "context"

// CheckInput is the gateway-level rate-limit request, decoupled from any
// transport (REST/gRPC) and from the engine's wire contract.
type CheckInput struct {
	TenantID string
	Subject  string
	Resource string
	Action   string
	RuleID   string
	Cost     uint32
	Metadata map[string]string
}

// CheckResult is the gateway-level decision returned to clients.
type CheckResult struct {
	Allowed      bool
	Limit        uint64
	Remaining    uint64
	ResetAtMs    int64
	RetryAfterMs int64
	Reason       string
}

// Limiter is the gateway's port to the rate-limit authority (limiter-engine).
// Defined here, at the point of use; implemented by an engine gRPC client
// adapter in the client package.
type Limiter interface {
	Check(ctx context.Context, in CheckInput) (CheckResult, error)
}
