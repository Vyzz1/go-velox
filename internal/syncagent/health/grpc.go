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

// Probe calls the engine's HealthCheck RPC. It returns nil only when the engine
// answers with status "ok"; a transport error or any other status is a failure.
func (p *EngineProbe) Probe(ctx context.Context) error {
	resp, err := p.client.HealthCheck(ctx, &enginev1.HealthCheckRequest{})
	if err != nil {
		return fmt.Errorf("health rpc: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("engine reported status %q", resp.Status)
	}
	return nil
}

// Close releases the underlying gRPC connection.
func (p *EngineProbe) Close() error {
	return p.conn.Close()
}
