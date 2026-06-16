package domain

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a rule does not exist in the store.
var ErrNotFound = errors.New("rule not found")

// Algorithm identifiers shared with the engine over the etcd wire contract.
const (
	AlgorithmGCRA          = "gcra"
	AlgorithmSlidingWindow = "sliding_window"
)

// Rule is a tenant-scoped rate-limit policy. config-service owns the durable
// copy (Postgres) and mirrors it to etcd for the engine to hot-reload.
type Rule struct {
	TenantID   string
	RuleID     string
	Algorithm  string // "gcra" | "sliding_window"
	Limit      uint64 // max requests per period
	PeriodSecs uint64 // window length in seconds
	Burst      uint64 // GCRA only; 0 → defaults to Limit
}

// RuleStore is the durable source of truth, backed by Postgres.
type RuleStore interface {
	Upsert(ctx context.Context, r Rule) (Rule, error)
	Get(ctx context.Context, tenantID, ruleID string) (Rule, error)
	ListByTenant(ctx context.Context, tenantID string) ([]Rule, error)
	ListAll(ctx context.Context) ([]Rule, error)
	// Delete reports whether a row was removed.
	Delete(ctx context.Context, tenantID, ruleID string) (bool, error)
}

// RulePublisher mirrors rule changes to etcd so the engine hot-reloads.
type RulePublisher interface {
	Publish(ctx context.Context, r Rule) error
	Remove(ctx context.Context, tenantID, ruleID string) error
}
