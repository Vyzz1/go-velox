// Package cluster provides a gossip-backed domain.Cluster adapter built on
// hashicorp/memberlist (a production SWIM implementation). It owns peer
// discovery and failure detection; it stays off the rate-limit request path.
package cluster

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/syncagent/domain"
)

// membersGauge tracks how many alive members this agent currently sees.
var membersGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "velox_cluster_members",
	Help: "Number of alive members currently known to this sync-agent.",
})

// Config is the gossip configuration injected from the composition root.
type Config struct {
	NodeID        string   // unique node name; must differ per node
	BindAddr      string   // address to bind the gossip listener (e.g. 0.0.0.0)
	BindPort      int      // TCP+UDP gossip port (e.g. 7070)
	AdvertiseAddr string   // address peers should use to reach us ("" → auto-detect)
	AdvertisePort int      // port peers should use ("" / 0 → BindPort)
	Seeds         []string // existing members to join on startup ("host:port")

	// Role + EngineAddr are advertised to peers via gossip metadata (NodeMeta).
	// When this agent runs as a sidecar of a limiter-engine, Role is "engine" and
	// EngineAddr is that engine's gRPC address — the gateway router locates engines
	// by reading these from any agent's membership view.
	Role       string // e.g. "engine"; empty for a plain membership node
	EngineAddr string // gRPC host:port of the co-located limiter-engine
}

// nodeMeta is the JSON payload each node advertises through memberlist's
// NodeMeta delegate. It must stay small (memberlist caps meta at 512 bytes).
// Healthy carries no omitempty: a false value must travel so the gateway can
// distinguish "engine reported unhealthy" from "field absent".
type nodeMeta struct {
	Role       string `json:"role,omitempty"`
	EngineAddr string `json:"engine_addr,omitempty"`
	Healthy    bool   `json:"healthy"`
}

// Memberlist implements domain.Cluster, memberlist.EventDelegate, and
// memberlist.Delegate. The embedded *memberlist.Memberlist runs the SWIM gossip
// loops; we translate its view into domain.Member, advertise node metadata
// (role + engine address), and mirror membership changes into logs and metrics.
type Memberlist struct {
	ml  *memberlist.Memberlist
	log *zap.Logger

	// mu guards the advertised metadata, which can change at runtime when the
	// engine sidecar toggles its health (see SetEngineHealthy). NodeMeta reads
	// it whenever memberlist broadcasts our state.
	mu         sync.RWMutex
	role       string
	engineAddr string
	healthy    bool

	// stop signals the background metrics loop to exit (closed by Shutdown).
	stop chan struct{}
}

// New creates the gossip node and starts its background loops. Call Join after
// construction to merge with an existing cluster.
func New(cfg Config, log *zap.Logger) (*Memberlist, error) {
	mlCfg := memberlist.DefaultLANConfig()
	mlCfg.Name = cfg.NodeID
	mlCfg.BindAddr = cfg.BindAddr
	mlCfg.BindPort = cfg.BindPort
	if cfg.AdvertiseAddr != "" {
		mlCfg.AdvertiseAddr = cfg.AdvertiseAddr
	}
	if cfg.AdvertisePort != 0 {
		mlCfg.AdvertisePort = cfg.AdvertisePort
	}
	// Route memberlist's stdlib logging through our structured logger.
	mlCfg.Logger = zap.NewStdLog(log.Named("memberlist"))

	// An engine sidecar starts unhealthy and only advertises healthy=true after
	// its first successful probe (see usecase.HealthCheck), so the gateway never
	// routes to an engine we have not yet confirmed is up.
	m := &Memberlist{
		log:        log,
		role:       cfg.Role,
		engineAddr: cfg.EngineAddr,
		healthy:    false,
		stop:       make(chan struct{}),
	}
	mlCfg.Events = m   // receive NotifyJoin/Leave/Update
	mlCfg.Delegate = m // advertise NodeMeta (role + engine address + health) to peers

	ml, err := memberlist.Create(mlCfg)
	if err != nil {
		return nil, err
	}
	m.ml = ml
	go m.gaugeLoop()
	return m, nil
}

// Join merges this node into an existing cluster via the seed addresses.
// With no seeds it is a no-op (this node forms a new single-member cluster).
func (m *Memberlist) Join(seeds []string) (int, error) {
	if len(seeds) == 0 {
		return 0, nil
	}
	return m.ml.Join(seeds)
}

// Members returns the current membership snapshot, sorted by node ID for
// stable output. memberlist excludes dead/left nodes from this list.
func (m *Memberlist) Members() []domain.Member {
	local := m.ml.LocalNode().Name
	nodes := m.ml.Members()
	out := make([]domain.Member, 0, len(nodes))
	for _, n := range nodes {
		meta := decodeMeta(n.Meta)
		out = append(out, domain.Member{
			ID:         n.Name,
			Addr:       n.Address(),
			State:      mapState(n.State),
			Local:      n.Name == local,
			Role:       meta.Role,
			EngineAddr: meta.EngineAddr,
			Healthy:    meta.Healthy,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Local returns the node we are running on. It reads our advertised identity
// from the live in-memory state rather than the gossiped meta blob, so a health
// transition is reflected immediately (UpdateNode propagation to LocalNode().Meta
// is not instantaneous).
func (m *Memberlist) Local() domain.Member {
	n := m.ml.LocalNode()
	m.mu.RLock()
	role, engineAddr, healthy := m.role, m.engineAddr, m.healthy
	m.mu.RUnlock()
	return domain.Member{
		ID:         n.Name,
		Addr:       n.Address(),
		State:      domain.StateAlive,
		Local:      true,
		Role:       role,
		EngineAddr: engineAddr,
		Healthy:    healthy,
	}
}

// decodeMeta parses a node's advertised gossip metadata. An unset or malformed
// payload yields a zero-value nodeMeta (a plain membership node).
func decodeMeta(b []byte) nodeMeta {
	var meta nodeMeta
	if len(b) == 0 {
		return meta
	}
	_ = json.Unmarshal(b, &meta)
	return meta
}

// Leave broadcasts an intentional departure so peers mark us "left" rather than
// waiting for failure detection. Call before Shutdown for a graceful exit.
func (m *Memberlist) Leave(timeout time.Duration) error {
	return m.ml.Leave(timeout)
}

// Shutdown stops the gossip listeners and background loops.
func (m *Memberlist) Shutdown() error {
	close(m.stop)
	return m.ml.Shutdown()
}

// --- memberlist.Delegate ---
// We use the delegate only to piggyback small, static node metadata (role +
// engine address) onto the gossip protocol. We do not exchange user messages or
// custom state, so the remaining methods are deliberate no-ops.

// NodeMeta returns this node's metadata, broadcast to peers on join and on
// every UpdateNode. It is re-encoded on each call so a runtime health change
// (SetEngineHealthy) is reflected the next time memberlist gossips our state.
func (m *Memberlist) NodeMeta(limit int) []byte {
	m.mu.RLock()
	meta := nodeMeta{Role: m.role, EngineAddr: m.engineAddr, Healthy: m.healthy}
	m.mu.RUnlock()

	b, err := json.Marshal(meta)
	if err != nil || len(b) > limit {
		return nil // never exceed memberlist's meta size cap
	}
	return b
}

// SetEngineHealthy updates the locally-advertised engine health and, only when
// the value changes, re-broadcasts our metadata to peers via UpdateNode. It
// implements domain.HealthAdvertiser. Safe for concurrent use.
func (m *Memberlist) SetEngineHealthy(healthy bool) error {
	m.mu.Lock()
	changed := m.healthy != healthy
	m.healthy = healthy
	m.mu.Unlock()

	if !changed {
		return nil
	}
	// UpdateNode triggers memberlist to call NodeMeta and gossip the fresh meta.
	// It only runs on a health transition, so the modest block is acceptable.
	return m.ml.UpdateNode(5 * time.Second)
}

func (m *Memberlist) NotifyMsg([]byte)                {}
func (m *Memberlist) GetBroadcasts(_, _ int) [][]byte { return nil }
func (m *Memberlist) LocalState(_ bool) []byte        { return nil }
func (m *Memberlist) MergeRemoteState([]byte, bool)   {}

// --- memberlist.EventDelegate ---

// These callbacks run while memberlist holds its internal nodeLock, so they must
// not call back into memberlist (NumMembers, Members, ...): sync.RWMutex is not
// reentrant, so re-acquiring that lock here self-deadlocks. We only log; the
// member gauge is refreshed out-of-band by gaugeLoop.

func (m *Memberlist) NotifyJoin(n *memberlist.Node) {
	m.log.Info("member joined", zap.String("node", n.Name), zap.String("addr", n.Address()))
}

func (m *Memberlist) NotifyLeave(n *memberlist.Node) {
	m.log.Info("member left", zap.String("node", n.Name), zap.String("addr", n.Address()))
}

func (m *Memberlist) NotifyUpdate(n *memberlist.Node) {
	m.log.Debug("member updated", zap.String("node", n.Name), zap.String("addr", n.Address()))
}

// gaugeLoop keeps the alive-member gauge in sync with the cluster view. It calls
// NumMembers from its own goroutine — outside memberlist's delegate callbacks,
// which hold nodeLock — so it never triggers the reentrant-lock deadlock that
// calling NumMembers from Notify* would. It exits when Shutdown closes m.stop.
func (m *Memberlist) gaugeLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	membersGauge.Set(float64(m.ml.NumMembers()))
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			membersGauge.Set(float64(m.ml.NumMembers()))
		}
	}
}

func mapState(s memberlist.NodeStateType) domain.State {
	switch s {
	case memberlist.StateAlive:
		return domain.StateAlive
	case memberlist.StateSuspect:
		return domain.StateSuspect
	case memberlist.StateDead:
		return domain.StateDead
	case memberlist.StateLeft:
		return domain.StateLeft
	default:
		return domain.StateAlive
	}
}
