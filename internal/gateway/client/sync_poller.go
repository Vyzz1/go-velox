package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type memberResponse struct {
	ID         string `json:"id"`
	Addr       string `json:"addr"`
	State      string `json:"state"`
	Local      bool   `json:"local"`
	Role       string `json:"role"`
	EngineAddr string `json:"engine_addr"`
	Healthy    bool   `json:"healthy"`
}

type membersResponse struct {
	Local   string           `json:"local"`
	Count   int              `json:"count"`
	Members []memberResponse `json:"members"`
}

// SyncPoller periodically fetches the active limiter-engine nodes from the sync-agent.
type SyncPoller struct {
	router       *Router
	syncAgentURL string
	interval     time.Duration
	log          *zap.Logger
}

// NewSyncPoller creates a new poller.
func NewSyncPoller(router *Router, url string, interval time.Duration, log *zap.Logger) *SyncPoller {
	return &SyncPoller{
		router:       router,
		syncAgentURL: url,
		interval:     interval,
		log:          log,
	}
}

// Start begins the polling loop in the background. It blocks until ctx is canceled.
func (p *SyncPoller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Initial poll
	p.poll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

func (p *SyncPoller) poll() {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(p.syncAgentURL + "/v1/members")
	if err != nil {
		p.log.Error("failed to fetch members from sync-agent", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.log.Error("sync-agent returned non-200 status", zap.Int("status", resp.StatusCode))
		return
	}

	var data membersResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		p.log.Error("failed to decode sync-agent response", zap.Error(err))
		return
	}

	// Each engine sidecar advertises its limiter-engine gRPC address via gossip
	// metadata (role=engine, engine_addr=host:port). We route only to those, and
	// keep both alive and suspect nodes to avoid ring churn on transient flaps.
	// We additionally require healthy=true: the sidecar sets this from probing
	// its local engine, so an unreachable/degraded engine is dropped from the
	// ring even while its sidecar is still gossiping.
	seen := make(map[string]bool)
	var activeAddrs []string
	for _, m := range data.Members {
		if m.Role != "engine" || m.EngineAddr == "" {
			continue
		}
		if !m.Healthy {
			continue
		}
		if m.State != "alive" && m.State != "suspect" {
			continue
		}
		if seen[m.EngineAddr] {
			continue
		}
		seen[m.EngineAddr] = true
		activeAddrs = append(activeAddrs, m.EngineAddr)
	}

	if len(activeAddrs) > 0 {
		p.router.UpdateMembers(activeAddrs)
		p.log.Debug("updated hash ring", zap.Strings("members", activeAddrs))
	} else {
		p.log.Warn("no active limiter-engine nodes advertised by sync-agent")
	}
}
