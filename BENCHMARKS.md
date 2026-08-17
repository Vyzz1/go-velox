# Benchmarks

Micro-benchmarks for the hot path — one rate-limit decision, i.e.
`store.Check`: an `EVALSHA` of the GCRA / sliding-window Lua script plus the
Redis round-trip. This is the latency that matters for a rate limiter, and it
needs only a local Redis, no cluster or deployment.

## Running

```bash
docker run -d --name r -p 6399:6379 redis:7-alpine
REDIS_BENCH_ADDR=localhost:6399 go test -run=^$ -bench=. -benchmem -benchtime=3s \
  ./internal/engine/store/
docker rm -f r
```

The benchmarks **skip** when `REDIS_BENCH_ADDR` is unset, so a plain `go test`
and CI (no Redis) are unaffected. Set `REDIS_BENCH_PASSWORD` if the Redis needs
auth.

## Results

Redis 7 (`redis:7-alpine`), Go 1.26, 16 logical cores. `-benchtime=3s`.

| Benchmark | ns/op | ≈ latency | B/op | allocs/op |
|---|--:|--:|--:|--:|
| `GCRA` (serial) | 306,721 | ~307 µs | 656 | 18 |
| `SlidingWindow` (serial) | 294,056 | ~294 µs | 632 | 18 |
| `GCRAParallel` | 66,302 | ~66 µs | 687 | 18 |
| `SlidingWinParallel` | 67,242 | ~67 µs | 665 | 18 |

Parallel `ns/op` is wall-time per op across all goroutines, so it is also the
inverse of throughput: **~15,000 decisions/sec** on this machine.

## Reading the numbers

- **Decision cost is network-bound, not CPU-bound.** Each decision does only
  ~18 allocations / ~650 B of Go work; the wall time is dominated by the Redis
  round-trip. That is exactly what you want — the engine adds negligible
  overhead on top of Redis.
- **Concurrency scales well.** Serial ~300 µs → parallel ~66 µs (~4.5×) as the
  go-redis connection pool keeps multiple round-trips in flight.
- **GCRA ≈ sliding-window.** Both are a single O(1) script; neither is
  meaningfully more expensive.

> ⚠️ **Environment caveat.** These were measured on **Docker Desktop for
> Windows**, where loopback to a container is proxied through a VM — this
> inflates the absolute latency substantially. On native Linux against a local
> Redis the per-decision latency is typically an order of magnitude lower
> (tens of µs). Treat the **relative** comparisons (GCRA vs sliding-window,
> serial vs parallel, allocations) as the signal; re-run on your target infra
> for absolute numbers.
