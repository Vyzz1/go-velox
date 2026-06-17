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
