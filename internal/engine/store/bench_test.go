package store

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vyzz1/go-velox.git/internal/engine/algorithm"
	"github.com/Vyzz1/go-velox.git/internal/engine/domain"
)

// benchStore connects to a real Redis for benchmarking. Set REDIS_BENCH_ADDR
// (e.g. localhost:6379) to run; otherwise the benchmark is skipped, so a plain
// `go test` / CI without Redis stays unaffected.
func benchStore(b *testing.B) *Store {
	b.Helper()
	addr := os.Getenv("REDIS_BENCH_ADDR")
	if addr == "" {
		b.Skip("set REDIS_BENCH_ADDR to benchmark against a real Redis")
	}
	s, err := New([]string{addr}, os.Getenv("REDIS_BENCH_PASSWORD"))
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

// A limit large enough that the bucket never saturates over the run, so every
// call exercises the full allow path (the common case).
func benchParams(alg algorithm.AlgorithmType) algorithm.Params {
	return algorithm.Params{Algorithm: alg, Limit: 1_000_000_000, Period: time.Minute, Burst: 1_000_000_000}
}

func benchSerial(b *testing.B, alg algorithm.AlgorithmType) {
	s := benchStore(b)
	p := benchParams(alg)
	in := domain.CheckInput{TenantID: "bench", RuleID: "r", Subject: "u"}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.Check(ctx, in, p); err != nil {
			b.Fatal(err)
		}
	}
}

func benchParallel(b *testing.B, alg algorithm.AlgorithmType) {
	s := benchStore(b)
	p := benchParams(alg)
	ctx := context.Background()
	var gid atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		// A distinct key per goroutine so throughput isn't bottlenecked on a
		// single Redis key (which Redis serializes).
		in := domain.CheckInput{TenantID: "bench", RuleID: "r", Subject: strconv.FormatInt(gid.Add(1), 10)}
		for pb.Next() {
			if _, err := s.Check(ctx, in, p); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGCRA(b *testing.B)               { benchSerial(b, algorithm.GCRA) }
func BenchmarkSlidingWindow(b *testing.B)      { benchSerial(b, algorithm.SlidingWindow) }
func BenchmarkGCRAParallel(b *testing.B)       { benchParallel(b, algorithm.GCRA) }
func BenchmarkSlidingWinParallel(b *testing.B) { benchParallel(b, algorithm.SlidingWindow) }
