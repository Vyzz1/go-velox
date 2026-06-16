// Package publisher mirrors rule changes into etcd so the limiter-engine can
// hot-reload them by watching the key prefix.
package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/Vyzz1/go-velox.git/internal/configsvc/domain"
)

// WireRule is the JSON contract stored at each etcd key. The engine's
// EtcdRuleProvider unmarshals the identical shape — keep the two in sync.
type WireRule struct {
	Algorithm  string `json:"algorithm"`
	Limit      uint64 `json:"limit"`
	PeriodSecs uint64 `json:"period_secs"`
	Burst      uint64 `json:"burst"`
}

// EtcdPublisher implements domain.RulePublisher.
type EtcdPublisher struct {
	client *clientv3.Client
	prefix string // e.g. "/velox/rules/"
}

func NewEtcd(client *clientv3.Client, prefix string) *EtcdPublisher {
	return &EtcdPublisher{client: client, prefix: prefix}
}

// key returns prefix + "{tenant}/{rule}".
func (p *EtcdPublisher) key(tenantID, ruleID string) string {
	return p.prefix + tenantID + "/" + ruleID
}

func (p *EtcdPublisher) Publish(ctx context.Context, r domain.Rule) error {
	payload, err := json.Marshal(WireRule{
		Algorithm:  r.Algorithm,
		Limit:      r.Limit,
		PeriodSecs: r.PeriodSecs,
		Burst:      r.Burst,
	})
	if err != nil {
		return fmt.Errorf("publisher: marshal rule: %w", err)
	}
	if _, err := p.client.Put(ctx, p.key(r.TenantID, r.RuleID), string(payload)); err != nil {
		return fmt.Errorf("publisher: etcd put: %w", err)
	}
	return nil
}

func (p *EtcdPublisher) Remove(ctx context.Context, tenantID, ruleID string) error {
	if _, err := p.client.Delete(ctx, p.key(tenantID, ruleID)); err != nil {
		return fmt.Errorf("publisher: etcd delete: %w", err)
	}
	return nil
}
