// Package domain holds the sync-agent's business entities and port interfaces.
// It has no dependency on transport, gossip drivers, or third-party packages.
package domain

// State is the membership state of a node as seen through the gossip protocol.
type State string

const (
	StateAlive   State = "alive"
	StateSuspect State = "suspect"
	StateDead    State = "dead"
	StateLeft    State = "left"
)

// Member is one node in the cluster's membership view.
type Member struct {
	ID    string // unique node name
	Addr  string // host:port the node gossips on
	State State
	Local bool // true when this is the node we are running on

	// Role identifies what this node represents in the cluster, e.g. "engine"
	// for a sync-agent running as a sidecar of a limiter-engine. Empty when the
	// node advertised no metadata.
	Role string
	// EngineAddr is the gRPC address (host:port) of the limiter-engine this node
	// is a sidecar for. Consumers (the gateway router) dial this address rather
	// than the gossip Addr. Empty for non-engine nodes.
	EngineAddr string
	// Healthy reports whether the sidecar's last health probe of its local
	// limiter-engine succeeded. The gateway routes only to healthy engines.
	// Always false for non-engine nodes (Role != "engine").
	Healthy bool
}

// Cluster is the port the use case depends on: a live view of cluster
// membership maintained by a gossip adapter. Implementations keep this fresh
// in the background; reads must be safe to call concurrently.
type Cluster interface {
	// Members returns the current membership snapshot (alive + suspect nodes).
	Members() []Member
	// Local returns the node we are running on.
	Local() Member
}
