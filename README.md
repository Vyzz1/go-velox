# go-velox

[![CI](https://github.com/Vyzz1/go-velox/actions/workflows/ci.yml/badge.svg)](https://github.com/Vyzz1/go-velox/actions/workflows/ci.yml)

`go-velox` is a Go-based microservice system for building a `Distributed Rate Limiter as a Service`.

The platform is designed around four services:

- `api-gateway` on `:8080` for external REST entry
- `limiter-engine` on `:9090` for internal gRPC rate-limit decisions
- `config-service` on `:8081` for tenant rule management and hot reload
- `sync-agent` on `:7070/udp` for gossip-based peer discovery

## Architecture

Requests flow **client → api-gateway → limiter-engine → Redis Cluster**.
**sync-agent** gossip gives the gateway a live view of the engine fleet, and
**config-service** pushes rule changes to the engines through etcd (hot reload).

### `api-gateway`

- REST entry point: `POST /v1/check` → `200` / `429` with `Retry-After` and
  `X-RateLimit-*` headers
- routes each request to a limiter-engine replica over a **consistent-hash ring**
  built from live gossip membership; the routing key is configurable
  (`ROUTE_KEY=tenant | tenant_subject`)
- thin — owns no limiter state

### `limiter-engine`

- the rate-limit decision engine (internal gRPC)
- evaluates **GCRA** and **sliding-window** limits as **atomic Lua scripts** on
  **Redis Cluster** (counters live in Redis, so engines are stateless)
- **fail-open / fail-closed** policy when Redis is unreachable
  (`LIMITER_FAIL_MODE`) — a clean 200/429, never a 5xx
- rules hot-reload from an etcd watch with no restart

### `config-service`

- REST CRUD for per-tenant rules
- **Postgres** is the source of truth; every change mirrors to **etcd** for
  propagation, and a startup reconcile heals drift

### `sync-agent`

- gossip membership + failure detection via **SWIM** (hashicorp/memberlist)
- runs as a **sidecar** of each engine, advertising the engine's gRPC address and
  health so the gateway routes only to live engines

## Local Infra

The current `docker-compose.yml` provisions the shared infrastructure layer:

- Redis Cluster: `6` nodes, `3 masters + 3 replicas`
- Etcd: `2379`, `2380`
- Jaeger: `16686`, `4317`, `4318`
- Prometheus: `9090`
- Grafana: `3000`

Redis host ports:

- `6371` -> `redis-1:6379`
- `6372` -> `redis-2:6379`
- `6373` -> `redis-3:6379`
- `6374` -> `redis-4:6379`
- `6375` -> `redis-5:6379`
- `6376` -> `redis-6:6379`

## Repository Layout

Target layout for this project:

```text
go-velox/
├── go.mod
├── go.sum
├── Makefile
├── docker-compose.yml
├── .golangci.yml
├── AGENTS.md
├── README.md
├── cmd/
│   ├── api-gateway/main.go
│   ├── limiter-engine/main.go
│   ├── config-service/main.go
│   └── sync-agent/main.go
├── internal/
│   ├── gateway/
│   ├── engine/
│   │   ├── algorithm/
│   │   └── store/
│   ├── configsvc/
│   └── syncagent/
├── pkg/
│   ├── logger/
│   ├── config/
│   └── middleware/
├── proto/
│   ├── ratelimit.proto
│   └── engine.proto
├── infra/
│   ├── prometheus/
│   └── grafana/
└── deploy/
    ├── k8s/        # raw Kubernetes manifests (reference / learning)
    └── helm/       # umbrella Helm chart (production)
```

## Getting Started

### Prerequisites

- Go
- Docker
- Docker Compose
- GNU Make
- Git Bash or another Bash-compatible shell

Optional:

- `golangci-lint`
- `protoc`
- `protoc-gen-go`
- `protoc-gen-go-grpc`

### Start Infra

```bash
make compose-up
```

Validate compose config:

```bash
make compose-config
```

Show running containers:

```bash
make compose-ps
```

Stop infra:

```bash
make compose-down
```

### Development Commands

Show available commands:

```bash
make help
```

Run formatting:

```bash
make fmt
```

Run tests:

```bash
make test
```

Run lint:

```bash
make lint
```

Run all checks:

```bash
make check
```

Generate protobuf code when `proto/` exists:

```bash
make proto
```

Start infra and run all available services:

```bash
make dev
```

`make dev` currently:

- starts Docker infra first
- runs any service with an existing `cmd/<service>/main.go`
- skips missing services cleanly
- writes logs to `.tmp/dev/*.log`

## Deployment (Kubernetes)

Beyond `docker-compose` (local dev), the platform ships two Kubernetes paths under
[`deploy/`](deploy):

- **[`deploy/k8s`](deploy/k8s)** — hand-written manifests plus a runbook, kept as
  the reference for the tricky parts (engine + sync-agent sidecar StatefulSet,
  headless Service for gossip, membership Service, gossip-join DNS handling).
- **[`deploy/helm/govelox`](deploy/helm/govelox)** — the umbrella Helm chart for
  real deployments. Lightweight bundled infra by default; a
  [`values-prod.yaml`](deploy/helm/govelox/values-prod.yaml) overlay switches on
  real Bitnami Redis Cluster / Postgres / etcd plus Secret-sourced credentials,
  PodDisruptionBudgets, NetworkPolicies, Ingress, HPA autoscaling, and Prometheus
  ServiceMonitors — each behind a toggle. See the chart
  [README](deploy/helm/govelox/README.md).

```bash
# demo (bundled infra)
helm install velox ./deploy/helm/govelox -n velox --create-namespace
# production (real backends + hardening)
helm dependency build ./deploy/helm/govelox
helm install velox ./deploy/helm/govelox -n velox --create-namespace \
  -f ./deploy/helm/govelox/values-prod.yaml
```

## Status

All four services are implemented and wired end-to-end, with unit + integration
tests and green CI. Highlights:

- **Algorithms** — GCRA and sliding-window as atomic Lua on Redis Cluster,
  tested against an in-process miniredis.
- **Topology** — gateway consistent-hash routing over a gossip-discovered engine
  fleet (sync-agent sidecars); tenant-sticky or tenant+subject spreading.
- **Resilience** — automatic engine failover; fail-open/fail-closed on Redis loss.
- **Control plane** — config-service (Postgres → etcd) hot-reloads engine rules
  with no restart.
- **Security** — optional Redis password + etcd RBAC, credentials sourced from
  Kubernetes Secrets.
- **Observability** — structured logs (Zap), Prometheus metrics, OTLP tracing to
  Jaeger; a provisioned Grafana dashboard.
- **Deploy** — `docker-compose` for local; raw k8s manifests and an umbrella Helm
  chart (Secrets, PDBs, NetworkPolicies, Ingress, HPA, ServiceMonitors) for k8s.
- **CI** — GitHub Actions: build, `go test -race`, golangci-lint, helm lint/template.
- **Benchmarks** — hot-path decision latency / throughput against a local Redis;
  see [`BENCHMARKS.md`](BENCHMARKS.md).

## Notes

- `docker-compose.yml` still includes `version: "3.8"`, which modern `docker compose` ignores with a warning.
- The module path in `go.mod` is currently `github.com/Vyzz1/go-velox.git`.
- This project is intended for service boundaries first, not a monolith split later.
