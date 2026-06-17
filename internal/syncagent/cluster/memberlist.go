// Package cluster provides a gossip-backed domain.Cluster adapter built on
// hashicorp/memberlist (a production SWIM implementation). It owns peer
// discovery and failure detection; it stays off the rate-limit request path.
package cluster

import (
	"sort"
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
}

// Memberlist implements domain.Cluster and memberlist.EventDelegate. The
// embedded *memberlist.Memberlist runs the SWIM gossip loops; we translate its
// view into domain.Member and mirror membership changes into logs and metrics.
type Memberlist struct {
	ml  *memberlist.Memberlist
	log *zap.Logger
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

	m := &Memberlist{log: log}
	mlCfg.Events = m // receive NotifyJoin/Leave/Update

	ml, err := memberlist.Create(mlCfg)
	if err != nil {
		return nil, err
	}
	m.ml = ml
	m.refresh()
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
		out = append(out, domain.Member{
			ID:    n.Name,
			Addr:  n.Address(),
			State: mapState(n.State),
			Local: n.Name == local,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Local returns the node we are running on.
func (m *Memberlist) Local() domain.Member {
	n := m.ml.LocalNode()
	return domain.Member{ID: n.Name, Addr: n.Address(), State: domain.StateAlive, Local: true}
}

// Leave broadcasts an intentional departure so peers mark us "left" rather than
// waiting for failure detection. Call before Shutdown for a graceful exit.
func (m *Memberlist) Leave(timeout time.Duration) error {
	return m.ml.Leave(timeout)
}

// Shutdown stops the gossip listeners and background loops.
func (m *Memberlist) Shutdown() error {
	return m.ml.Shutdown()
}

// --- memberlist.EventDelegate ---

func (m *Memberlist) NotifyJoin(n *memberlist.Node) {
	m.log.Info("member joined", zap.String("node", n.Name), zap.String("addr", n.Address()))
	m.refresh()
}

func (m *Memberlist) NotifyLeave(n *memberlist.Node) {
	m.log.Info("member left", zap.String("node", n.Name), zap.String("addr", n.Address()))
	m.refresh()
}

func (m *Memberlist) NotifyUpdate(n *memberlist.Node) {
	m.log.Debug("member updated", zap.String("node", n.Name), zap.String("addr", n.Address()))
	m.refresh()
}

// refresh syncs the alive-member gauge with the current cluster view.
func (m *Memberlist) refresh() {
	if m.ml != nil {
		membersGauge.Set(float64(m.ml.NumMembers()))
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
