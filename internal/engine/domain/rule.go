package domain

import (
	"context"
	"time"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
)

// Rule is the rate-limit policy for a tenant + rule-id pair.
type Rule struct {
	Algorithm algorithm.AlgorithmType // defaults to GCRA
	Limit     uint64
	Period    time.Duration
	Burst     uint64 // GCRA only; 0 → defaults to Limit
}

// RuleProvider resolves a Rule from a (tenantID, ruleID) pair.
// Implementations: StaticProvider (now), etcd-backed hot-reload (config-service).
type RuleProvider interface {
	GetRule(ctx context.Context, tenantID, ruleID string) (Rule, error)
}

// StaticProvider returns the same default Rule for every request.
// Replaced by an etcd-backed provider once config-service is ready.
type StaticProvider struct {
	Default Rule
}

func (p *StaticProvider) GetRule(_ context.Context, _, _ string) (Rule, error) {
	return p.Default, nil
}
