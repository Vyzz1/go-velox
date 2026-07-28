// Package health provides a domain.EngineProbe adapter that checks the
// liveness of the limiter-engine co-located with this sync-agent sidecar by
// calling its gRPC HealthCheck RPC.
package health

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

// EngineProbe dials the co-located limiter-engine over gRPC and reports whether
// its HealthCheck RPC returns an "ok" status. The connection is lazy
// (grpc.NewClient), so construction never blocks on the engine being up.
type EngineProbe struct {
	conn   *grpc.ClientConn
	client enginev1.LimiterEngineServiceClient
}

// NewEngineProbe creates a probe targeting the engine's gRPC address (host:port).
func NewEngineProbe(engineAddr string) (*EngineProbe, error) {
	conn, err := grpc.NewClient(engineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial engine %s: %w", engineAddr, err)
	}
	return &EngineProbe{
		conn:   conn,
		client: enginev1.NewLimiterEngineServiceClient(conn),
	}, nil
}

// Probe calls the engine's HealthCheck RPC. Only a transport failure (a dead or
// hung engine that cannot answer) marks the engine unroutable. A "degraded"
// status — the engine's Redis is unreachable — is deliberately NOT a drop
// trigger: the engine still serves decisions via its configured fail-mode, and
// dropping every engine during a shared-Redis outage would turn a graceful
// degrade into a 502 storm. The degraded status remains observable in logs and
// the engine's own metrics.
func (p *EngineProbe) Probe(ctx context.Context) error {
	if _, err := p.client.HealthCheck(ctx, &enginev1.HealthCheckRequest{}); err != nil {
		return fmt.Errorf("health rpc: %w", err)
	}
	return nil
}

// Close releases the underlying gRPC connection.
func (p *EngineProbe) Close() error {
	return p.conn.Close()
}
