# Clean Architecture in go-velox

## Overview

`go-velox` is a distributed rate-limiter platform built as several small
services (`api-gateway`, `limiter-engine`, `config-service`, and the planned
`sync-agent`). **Every service** follows the same Clean Architecture layering —
a layered design where business logic is isolated from infrastructure concerns.
Dependencies always point inward: outer layers depend on inner layers, never the
reverse.

```
┌──────────────────────────────────────────────┐
│               Delivery Layer                 │  gRPC / HTTP handlers, middleware
├──────────────────────────────────────────────┤
│              Use Case Layer                  │  Business workflows
├──────────────────────────────────────────────┤
│              Domain Layer                    │  Entities + port interfaces
├──────────────────────────────────────────────┤
│            Adapter / Infra Layer             │  Redis, Postgres, etcd, gRPC client
└──────────────────────────────────────────────┘
```

The same shape repeats per service, so the rest of this document describes the
layers once and pulls concrete examples from whichever service illustrates each
idea best.

---

## Layers

### 1. Domain (`internal/<svc>/domain/`)

The innermost layer. Contains pure business entities and the **port interfaces**
that outer layers must implement. Has **no dependencies** on transport, drivers,
or third-party infrastructure packages.

| File | Contents |
|---|---|
| `engine/domain/rule.go` | `Rule` entity; `RuleProvider` port (resolves a rule for a tenant+rule-id) |
| `engine/domain/store.go` | `Store` port — the atomic rate-limit counter backend |
| `engine/domain/check.go` | `CheckInput` / decision entities |
| `configsvc/domain/rule.go` | `Rule` entity; `RuleStore` + `RulePublisher` ports |
| `gateway/domain/check.go` | `CheckInput` / `CheckResult`; `Limiter` port to the engine |

**Example — a domain port (config-service):**

```go
// internal/configsvc/domain/rule.go

type RuleStore interface {
    Upsert(ctx context.Context, r Rule) (Rule, error)
    Get(ctx context.Context, tenantID, ruleID string) (Rule, error)
    ListByTenant(ctx context.Context, tenantID string) ([]Rule, error)
    ListAll(ctx context.Context) ([]Rule, error)
    Delete(ctx context.Context, tenantID, ruleID string) (bool, error)
}

// RulePublisher propagates rule changes to subscribers (etcd) for hot-reload.
type RulePublisher interface {
    Publish(ctx context.Context, r Rule) error
    Remove(ctx context.Context, tenantID, ruleID string) error
}
```

The domain layer defines **what** must exist. It never cares **how** it is
implemented — `RuleStore` could be Postgres, an in-memory map, or a test stub.

---

### 2. Use Case (`internal/<svc>/usecase/`)

Orchestrates domain entities to fulfill application workflows. Each file is one
discrete operation, depending only on **domain ports** — never on concrete
implementations.

| Service | Use Case | File | Description |
|---|---|---|---|
| limiter-engine | Check limit | `check_limit.go` | Resolve rule via `RuleProvider`, evaluate against `Store` |
| limiter-engine | Health | `health.go` | Ping the store backend |
| config-service | Rule CRUD | `rule.go` | Validate, persist via `RuleStore`, mirror via `RulePublisher`, `Reconcile` on startup |
| api-gateway | Check | `check.go` | Default the rule-id / cost, then call the `Limiter` port |

```go
// internal/configsvc/usecase/rule.go

type RuleUseCase struct {
    store domain.RuleStore       // port
    pub   domain.RulePublisher   // port
    log   *zap.Logger
}
```

Because it only knows ports, a use case is fully testable with stubs — no
database, Redis, or etcd required.

---

### 3. Adapters / Infrastructure (`internal/<svc>/{store,repo,publisher,rules,client}/`)

Concrete implementations of domain ports. This is the only layer allowed to
import external packages (Redis, pgx, etcd clientv3, gRPC).

```
internal/engine/
├── store/redis.go         ← Store: Redis Cluster + Lua (atomic GCRA / sliding window)
├── rules/etcd.go          ← RuleProvider: etcd watch + in-memory snapshot (hot-reload)
└── algorithm/             ← GCRA + sliding-window as Lua scripts (run in Redis) + Go Params/Result types

internal/configsvc/
├── repo/postgres.go       ← RuleStore: sqlc + pgx/v5 (source of truth)
├── publisher/etcd.go      ← RulePublisher: writes /velox/rules/{tenant}/{rule}
└── db/                    ← sqlc-generated, type-safe SQL (do not edit by hand)

internal/gateway/
└── client/engine.go       ← Limiter: gRPC client adapter to limiter-engine
```

**Rule propagation chain (the hot-reload path):**

```
config-service                         limiter-engine
   PUT rule                               EtcdProvider.watch
      │                                        ▲
      ▼                                        │
  repo/postgres (source of truth)              │ etcd PUT/DELETE event
      │                                        │
      └──► publisher/etcd ──► etcd ────────────┘
           /velox/rules/{tenant}/{rule}    (in-memory snapshot updated,
                                            no engine restart)
```

The `WireRule` JSON (`{algorithm, limit, period_secs, burst}`) is the shared
contract between `publisher/etcd` and the engine's `rules/etcd` — keep the two
in sync.

---

### 4. Delivery (`internal/<svc>/delivery/`)

The outermost layer. Translates transport requests into use-case calls and
results back into transport responses. No business logic of its own.

```
internal/engine/delivery/grpc/server.go    ← implements the LimiterEngineService gRPC server
internal/configsvc/delivery/http/handler.go ← REST: PUT/GET/DELETE /v1/tenants/{tenant}/rules/{rule}
internal/gateway/delivery/http/handler.go    ← REST: POST /v1/check → 200 / 429 + X-RateLimit-* headers
```

Transport-specific concerns live here: the gateway maps a denied decision to
`429 Too Many Requests` with a `Retry-After` header; the config-service maps a
`usecase.ValidationError` to `400` and `domain.ErrNotFound` to `404`.

---

### 5. Shared Platform (`pkg/`)

Cross-cutting infrastructure reusable across all services. Service-specific
logic must **not** leak in here.

| Package | Purpose |
|---|---|
| `pkg/config` | Env-driven config loader (struct tags + defaults) |
| `pkg/logger` | Structured logging (Uber Zap) |
| `pkg/metrics` | Prometheus `/metrics` + `/healthz` server |
| `pkg/tracer` | OTLP → Jaeger tracing init |
| `pkg/middleware` | HTTP + gRPC request-ID, logging, recovery; context helpers |

---

## Dependency Flow

```
delivery ──► usecase ──► domain ◄── adapters (store / repo / publisher / rules / client)
                                ◄── pkg (config, logger, metrics, tracer, middleware)
```

- `delivery` knows about `usecase` structs (or small local interfaces over them)
- `usecase` knows about `domain` ports only
- adapters implement `domain` ports
- the `domain` layer has **zero imports** from the rest of the codebase

---

## Key Patterns

### Interface Defined at Point of Use

Each consumer declares the **minimal** interface it needs, where it needs it —
rather than importing a fat interface from the provider. Go's implicit interface
satisfaction wires it up automatically.

```go
// internal/configsvc/delivery/http/handler.go
// The handler declares exactly the methods it calls — nothing more.

type rules interface {
    Upsert(ctx context.Context, r domain.Rule) (domain.Rule, error)
    Get(ctx context.Context, tenantID, ruleID string) (domain.Rule, error)
    ListByTenant(ctx context.Context, tenantID string) ([]domain.Rule, error)
    Delete(ctx context.Context, tenantID, ruleID string) (bool, error)
}
```

`usecase.RuleUseCase` never declares that it implements `rules` — the method
set matches, so it just works.

**This is applied in every delivery layer, not just config-service.** Each
handler/server defines its own tiny consumer interface over the use case it
drives:

```go
// internal/gateway/delivery/http/handler.go
type checker interface {
    Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error)
}

// internal/engine/delivery/grpc/server.go — one interface per dependency
type checkLimiter interface {
    Execute(ctx context.Context, in domain.CheckInput) (domain.CheckResult, error)
}
type healthChecker interface {
    Execute(ctx context.Context) bool
}
```

Notice the engine server splits its two dependencies (`checkLimiter`,
`healthChecker`) into **separate** single-method interfaces rather than one fat
interface — each consumer only sees what it actually calls.

**Why define the interface in the consumer, not the provider?**

- **One-directional coupling** — the dependency points one way; the use case
  package does not need to know who consumes it.
- **Easier testing** — a handler test only needs a tiny mock satisfying the
  interface; no Postgres, no etcd, no real engine.
- **Minimal surface** — if the use case grows new methods later, the consumer is
  unaffected.

Because the interface is small, a test double is trivial — no mocking framework
required:

```go
// A stub satisfying the gateway's `checker` interface for a handler test.
type stubChecker struct {
    result domain.CheckResult
    err    error
}

func (s stubChecker) Execute(context.Context, domain.CheckInput) (domain.CheckResult, error) {
    return s.result, s.err
}

// h := Router(stubChecker{result: domain.CheckResult{Allowed: false}}, log)
// → exercise the 429 path with no engine, no network.
```

This follows the Go proverb: **"Accept interfaces, return structs."** — the
use cases *return* concrete `*UseCase` structs; the consumers *accept* the
narrow interfaces declared above.

### Ports & Adapters (Hexagonal)

Every boundary the business logic crosses is a domain port with a swappable
adapter behind it:

| Port | Adapter(s) |
|---|---|
| `engine/domain.Store` | `engine/store` (Redis Cluster + Lua) |
| `engine/domain.RuleProvider` | `engine/rules` (etcd watch) — formerly `StaticProvider` |
| `configsvc/domain.RuleStore` | `configsvc/repo` (Postgres/sqlc) |
| `configsvc/domain.RulePublisher` | `configsvc/publisher` (etcd) |
| `gateway/domain.Limiter` | `gateway/client` (engine gRPC client) |

### Constructor-based Dependency Injection

All dependencies are injected at construction time — no global state, no service
locator. Wiring happens in `cmd/<service>/main.go`, the composition root.

```go
// cmd/config-service/main.go (abridged)
store  := repo.NewPostgres(pool)
pub    := publisher.NewEtcd(etcdClient, cfg.EtcdPrefix)
ruleUC := usecase.NewRule(store, pub, log)
handler := httpdelivery.Router(ruleUC, log)
```

### Atomic Counter Evaluation

Rate-limit decisions must be atomic across distributed nodes. The `Store`
adapter executes a Lua script on Redis Cluster (loaded once, invoked via
`EVALSHA`) so the read-modify-write of a counter is a single atomic operation.

### Hot-reload via etcd Watch

`config-service` is the source of truth (Postgres) and mirrors every change to
etcd. The engine's `RuleProvider` loads all rules under the prefix at startup,
then **watches** the prefix: create/update/delete events update an in-memory
snapshot with no restart. A missing rule falls back to `LIMITER_DEFAULT_*`.

---

## Entry Points

Each service has its own composition root under `cmd/<service>/main.go`,
responsible for:

1. Loading config from env (`pkg/config`)
2. Initializing tracing (`pkg/tracer`) and the metrics server (`pkg/metrics`)
3. Connecting to backends (Redis / Postgres / etcd) as the service requires
4. Instantiating adapters, use cases, and the delivery handler/server
5. Starting the transport (gRPC for the engine, HTTP for gateway/config) with
   graceful shutdown on SIGINT/SIGTERM

This is the only place that knows about every layer of its service.
```

