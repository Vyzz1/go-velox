// Package rules provides an etcd-backed domain.RuleProvider for the engine.
// It loads all rules under a key prefix at startup, then watches the prefix so
// rule changes published by config-service hot-reload without a restart.
package rules

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// wireRule mirrors config-service's publisher.WireRule JSON contract.
type wireRule struct {
	Algorithm  string `json:"algorithm"`
	Limit      uint64 `json:"limit"`
	PeriodSecs uint64 `json:"period_secs"`
	Burst      uint64 `json:"burst"`
}

// EtcdProvider implements domain.RuleProvider, backed by an in-memory snapshot
// of etcd kept fresh by a background watch. Rules not present fall back to def.
type EtcdProvider struct {
	client *clientv3.Client
	prefix string
	def    domain.Rule
	log    *zap.Logger

	mu    sync.RWMutex
	rules map[string]domain.Rule // key: "{tenant}/{rule}"

	cancel context.CancelFunc
	done   chan struct{}
}

// New builds the provider, performs the initial load, and starts watching.
func New(ctx context.Context, client *clientv3.Client, prefix string, def domain.Rule, log *zap.Logger) (*EtcdProvider, error) {
	p := &EtcdProvider{
		client: client,
		prefix: prefix,
		def:    def,
		log:    log,
		rules:  make(map[string]domain.Rule),
		done:   make(chan struct{}),
	}

	resp, err := client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	for _, kv := range resp.Kvs {
		p.set(string(kv.Key), kv.Value)
	}
	log.Info("loaded rules from etcd", zap.Int("count", len(resp.Kvs)), zap.String("prefix", prefix))

	watchCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.watch(watchCtx, resp.Header.Revision+1)

	return p, nil
}

// GetRule returns the configured rule for the pair, or the default fallback.
func (p *EtcdProvider) GetRule(_ context.Context, tenantID, ruleID string) (domain.Rule, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.rules[tenantID+"/"+ruleID]; ok {
		return r, nil
	}
	return p.def, nil
}

// Close stops the background watch.
func (p *EtcdProvider) Close() {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
}

func (p *EtcdProvider) watch(ctx context.Context, rev int64) {
	defer close(p.done)
	wch := p.client.Watch(ctx, p.prefix, clientv3.WithPrefix(), clientv3.WithRev(rev))
	for resp := range wch {
		if err := resp.Err(); err != nil {
			p.log.Warn("etcd watch error", zap.Error(err))
			continue
		}
		for _, ev := range resp.Events {
			switch ev.Type {
			case clientv3.EventTypePut:
				p.set(string(ev.Kv.Key), ev.Kv.Value)
				p.log.Info("rule hot-reloaded", zap.String("key", string(ev.Kv.Key)))
			case clientv3.EventTypeDelete:
				p.del(string(ev.Kv.Key))
				p.log.Info("rule removed", zap.String("key", string(ev.Kv.Key)))
			}
		}
	}
}

// set parses a value and stores it under the prefix-stripped key.
func (p *EtcdProvider) set(key string, value []byte) {
	var w wireRule
	if err := json.Unmarshal(value, &w); err != nil {
		p.log.Warn("skip malformed rule", zap.String("key", key), zap.Error(err))
		return
	}
	rule := domain.Rule{
		Algorithm: parseAlgorithm(w.Algorithm),
		Limit:     w.Limit,
		Period:    time.Duration(w.PeriodSecs) * time.Second,
		Burst:     w.Burst,
	}
	p.mu.Lock()
	p.rules[p.suffix(key)] = rule
	p.mu.Unlock()
}

func (p *EtcdProvider) del(key string) {
	p.mu.Lock()
	delete(p.rules, p.suffix(key))
	p.mu.Unlock()
}

// suffix strips the configured prefix, leaving "{tenant}/{rule}".
func (p *EtcdProvider) suffix(key string) string {
	return strings.TrimPrefix(key, p.prefix)
}

func parseAlgorithm(s string) algorithm.AlgorithmType {
	if s == "sliding_window" {
		return algorithm.SlidingWindow
	}
	return algorithm.GCRA
}
