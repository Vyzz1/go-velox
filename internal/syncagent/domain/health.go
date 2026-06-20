package domain

import "context"

// EngineProbe checks the liveness of the limiter-engine co-located with this
// sync-agent sidecar. The gRPC health adapter implements it; the health-check
// use case depends on it. It has no knowledge of gossip or transport details.
type EngineProbe interface {
	// Probe returns nil when the engine is healthy, or an error describing why
	// the probe failed (unreachable, degraded, or timed out).
	Probe(ctx context.Context) error
}

// HealthAdvertiser propagates the locally-observed engine health into the
// cluster, so peers — and the gateway reading their membership view — stop
// routing to an engine whose sidecar reports it unhealthy. Implemented by the
// gossip cluster adapter.
type HealthAdvertiser interface {
	// SetEngineHealthy updates this node's advertised health. Implementations
	// should re-broadcast gossip metadata only when the value actually changes.
	SetEngineHealthy(healthy bool) error
}
