package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// newTestStore backs the Store with an in-process miniredis so the actual Lua
// scripts run against a real Redis command set — no Docker required.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := New([]string{mr.Addr()}, "")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var input = domain.CheckInput{TenantID: "acme", RuleID: "default"}

// gcra evaluates one GCRA request at a fixed logical time (ms).
func gcra(t *testing.T, s *Store, p algorithm.Params, cost, nowMs int64) algorithm.Result {
	t.Helper()
	res, err := s.checkGCRA(context.Background(), input, p, cost, nowMs)
	if err != nil {
		t.Fatalf("checkGCRA: %v", err)
	}
	return res
}

func TestGCRA_BurstThenDeny(t *testing.T) {
	s := newTestStore(t)
	p := algorithm.Params{Algorithm: algorithm.GCRA, Limit: 10, Period: time.Second, Burst: 10}
	const now = int64(1_700_000_000_000)

	allowed := 0
	for range 12 {
		if gcra(t, s, p, 1, now).Allowed {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("burst allowed = %d, want 10 (limit+burst at one instant)", allowed)
	}
	// The next request at the same instant is denied with a positive backoff.
	res := gcra(t, s, p, 1, now)
	if res.Allowed || res.RetryAfterMs <= 0 {
		t.Fatalf("saturated request = %+v, want denied with RetryAfterMs>0", res)
	}
}

func TestGCRA_RefillAfterTime(t *testing.T) {
	s := newTestStore(t)
	p := algorithm.Params{Algorithm: algorithm.GCRA, Limit: 10, Period: time.Second, Burst: 10}
	const now = int64(1_700_000_000_000)

	for range 10 {
		gcra(t, s, p, 1, now) // saturate the bucket
	}
	if gcra(t, s, p, 1, now).Allowed {
		t.Fatal("expected denial once the bucket is saturated")
	}
	// One emission interval later (period/limit = 100ms) a token has refilled.
	if !gcra(t, s, p, 1, now+100).Allowed {
		t.Fatal("expected allow after a 100ms refill")
	}
}

func TestGCRA_CostConsumesMultiple(t *testing.T) {
	s := newTestStore(t)
	p := algorithm.Params{Algorithm: algorithm.GCRA, Limit: 10, Period: time.Second, Burst: 10}
	const now = int64(1_700_000_000_000)

	// A cost-5 request consumes 5 tokens: 5 remain of the 10-token bucket.
	res := gcra(t, s, p, 5, now)
	if !res.Allowed || res.Remaining != 5 {
		t.Fatalf("cost-5 request = %+v, want allowed with Remaining=5", res)
	}
	// A second cost-5 drains it; a further cost-1 is denied.
	if !gcra(t, s, p, 5, now).Allowed {
		t.Fatal("second cost-5 should still fit")
	}
	if gcra(t, s, p, 1, now).Allowed {
		t.Fatal("bucket drained — further request must be denied")
	}
}

// sw evaluates one sliding-window request at a fixed logical time (ms).
func sw(t *testing.T, s *Store, p algorithm.Params, nowMs int64) algorithm.Result {
	t.Helper()
	res, err := s.checkSlidingWindow(context.Background(), input, p, 1, nowMs)
	if err != nil {
		t.Fatalf("checkSlidingWindow: %v", err)
	}
	return res
}

func TestSlidingWindow_LimitWithinWindow(t *testing.T) {
	s := newTestStore(t)
	p := algorithm.Params{Algorithm: algorithm.SlidingWindow, Limit: 5, Period: time.Second}
	const now = int64(1_700_000_000_000) // divisible by 1000 → window start (elapsed 0)

	allowed := 0
	for range 7 {
		if sw(t, s, p, now).Allowed {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("sliding-window allowed = %d, want 5 within one window", allowed)
	}
}

func TestSlidingWindow_ResetsAfterTwoWindows(t *testing.T) {
	s := newTestStore(t)
	p := algorithm.Params{Algorithm: algorithm.SlidingWindow, Limit: 5, Period: time.Second}
	const now = int64(1_700_000_000_000)

	for range 5 {
		sw(t, s, p, now) // fill the window
	}
	if sw(t, s, p, now).Allowed {
		t.Fatal("window is full — request must be denied")
	}
	// Two full windows later the previous-window weight is gone → fresh allowance.
	if !sw(t, s, p, now+2*time.Second.Milliseconds()).Allowed {
		t.Fatal("expected allow two windows later")
	}
}
