package client

import (
	"fmt"
	"sync"

	"github.com/buraksezer/consistent"
	"github.com/cespare/xxhash/v2"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	enginev1 "github.com/Vyzz1/go-velox.git/proto/gen/engine/v1"
)

// member implements consistent.Member interface
type member string

func (m member) String() string {
	return string(m)
}

// hasher implements consistent.Hasher interface using xxhash
type hasher struct{}

func (h hasher) Sum64(data []byte) uint64 {
	return xxhash.Sum64(data)
}

// Router manages a consistent hash ring of Limiter Engine nodes and their gRPC connections.
type Router struct {
	mu    sync.RWMutex
	ring  *consistent.Consistent
	conns map[string]*grpc.ClientConn
}

// NewRouter initializes an empty consistent hash router.
func NewRouter() *Router {
	cfg := consistent.Config{
		PartitionCount:    271,  // A prime number is good for distribution
		ReplicationFactor: 20,   // Number of virtual nodes per actual node
		Load:              1.25, // Bounded load feature (1.25 means a node can handle up to 25% above average load)
		Hasher:            hasher{},
	}

	return &Router{
		ring:  consistent.New(nil, cfg),
		conns: make(map[string]*grpc.ClientConn),
	}
}

// UpdateMembers reconciles the active list of engine node addresses.
// It opens new gRPC connections for new nodes and closes connections for removed nodes.
func (r *Router) UpdateMembers(addrs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	currentAddrs := make(map[string]bool)
	for _, addr := range addrs {
		currentAddrs[addr] = true
	}

	// Remove nodes that are no longer active
	for existingAddr, conn := range r.conns {
		if !currentAddrs[existingAddr] {
			r.ring.Remove(existingAddr)
			_ = conn.Close()
			delete(r.conns, existingAddr)
		}
	}

	// Add new nodes
	for _, addr := range addrs {
		if _, exists := r.conns[addr]; !exists {
			// In production, you would add retry, TLS, timeout, etc.
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
			)
			if err == nil {
				r.conns[addr] = conn
				r.ring.Add(member(addr))
			}
		}
	}
}

// GetClient locates the appropriate engine node for the given tenantID and returns its gRPC client.
func (r *Router) GetClient(tenantID string) (enginev1.LimiterEngineServiceClient, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.conns) == 0 {
		return nil, fmt.Errorf("no active limiter-engine nodes available")
	}

	// Find the node responsible for this tenant
	node := r.ring.LocateKey([]byte(tenantID))
	if node == nil {
		return nil, fmt.Errorf("failed to locate node for tenant: %s", tenantID)
	}

	addr := node.String()
	conn, exists := r.conns[addr]
	if !exists {
		return nil, fmt.Errorf("located node %s but connection not found", addr)
	}

	return enginev1.NewLimiterEngineServiceClient(conn), nil
}
