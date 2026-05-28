# go-velox

`go-velox` is a Go-based microservice system for building a `Distributed Rate Limiter as a Service`.

The platform is designed around four services:

- `api-gateway` on `:8080` for external gRPC and REST entry
- `limiter-engine` on `:9090` for internal gRPC rate-limit decisions
- `config-service` on `:8081` for tenant rule management and hot reload
- `sync-agent` on `:7070/udp` for gossip-based peer discovery

## Architecture

### `api-gateway`

- entry point for client traffic
- accepts gRPC and optionally REST
- forwards requests to internal services
- should stay thin and avoid owning limiter state

### `limiter-engine`

- core rate-limiting brain
- evaluates limits using Redis
- intended to run atomic Lua scripts against Redis Cluster
- owns rate-limit algorithms and decision logic

### `config-service`

- manages per-tenant rules
- exposes REST endpoints for configuration
- uses `etcd` for config propagation and hot reload

### `sync-agent`

- handles UDP gossip and peer discovery
- supports multi-node deployments
- should focus on membership and topology sync

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
└── infra/
    ├── prometheus/
    └── grafana/
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

## Current Status

The repository currently has infra and project conventions in place, but application services may still need to be scaffolded.

What is already present:

- `docker-compose.yml`
- Prometheus config
- Grafana provisioning
- `Makefile`
- `.golangci.yml`
- `AGENTS.md`

What likely comes next:

- scaffold `cmd/` for the four services
- add shared `pkg/` and internal business packages
- define `.proto` contracts
- wire Redis, Etcd, observability, and config flow into service implementations

## Notes

- `docker-compose.yml` still includes `version: "3.8"`, which modern `docker compose` ignores with a warning.
- The module path in `go.mod` is currently `github.com/Vyzz1/go-velox.git`.
- This project is intended for service boundaries first, not a monolith split later.
