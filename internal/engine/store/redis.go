package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// Store executes rate-limit checks against a Redis instance.
// Scripts are loaded once and invoked via EVALSHA on every check.
type Store struct {
	client           redis.UniversalClient
	gcraScript       *redis.Script
	slidingWinScript *redis.Script
}

func New(addrs []string) (*Store, error) {
	client := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:        addrs,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolSize:     20,
		MinIdleConns: 4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("store: redis ping: %w", err)
	}

	return &Store{
		client:           client,
		gcraScript:       redis.NewScript(algorithm.Script),
		slidingWinScript: redis.NewScript(algorithm.SlidingWindowScript),
	}, nil
}

func (s *Store) Close() error {
	return s.client.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Check dispatches to the correct algorithm based on p.Algorithm.
func (s *Store) Check(ctx context.Context, in domain.CheckInput, p algorithm.Params) (algorithm.Result, error) {
	cost := p.Cost
	if cost < 1 {
		cost = 1
	}

	nowMs := time.Now().UnixMilli()

	switch p.Algorithm {
	case algorithm.SlidingWindow:
		return s.checkSlidingWindow(ctx, in, p, int64(cost), nowMs)
	default:
		return s.checkGCRA(ctx, in, p, int64(cost), nowMs)
	}
}

func (s *Store) checkGCRA(ctx context.Context, in domain.CheckInput, p algorithm.Params, cost, nowMs int64) (algorithm.Result, error) {
	burst := p.Burst
	if burst < 1 {
		burst = p.Limit
	}

	key := buildKey(in)
	raw, err := s.gcraScript.Run(ctx, s.client, []string{key},
		p.Limit,
		p.Period.Milliseconds(),
		cost,
		nowMs,
		burst,
	).Int64Slice()
	if err != nil {
		return algorithm.Result{}, fmt.Errorf("store: gcra script: %w", err)
	}
	return parseResult(raw), nil
}

func (s *Store) checkSlidingWindow(ctx context.Context, in domain.CheckInput, p algorithm.Params, cost, nowMs int64) (algorithm.Result, error) {
	// Hash tag ensures the two window sub-keys land on the same cluster slot.
	key := "{" + buildKey(in) + "}"
	raw, err := s.slidingWinScript.Run(ctx, s.client, []string{key},
		p.Limit,
		p.Period.Milliseconds(),
		cost,
		nowMs,
	).Int64Slice()
	if err != nil {
		return algorithm.Result{}, fmt.Errorf("store: sliding window script: %w", err)
	}
	return parseResult(raw), nil
}

func parseResult(raw []int64) algorithm.Result {
	return algorithm.Result{
		Allowed:      raw[0] == 1,
		Remaining:    uint64(raw[1]),
		ResetAtMs:    raw[2],
		RetryAfterMs: raw[3],
	}
}

// buildKey returns rl:{tenantID}:{ruleID}[:{resource}][:{action}][:{subject}]
// Empty segments are omitted so tenant-wide limits stay compact.
func buildKey(in domain.CheckInput) string {
	parts := []string{"rl", in.TenantID, in.RuleID}
	if in.Resource != "" {
		parts = append(parts, in.Resource)
	}
	if in.Action != "" {
		parts = append(parts, in.Action)
	}
	if in.Subject != "" {
		parts = append(parts, in.Subject)
	}
	return strings.Join(parts, ":")
}
