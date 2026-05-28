# AGENTS.md

## Project Identity

- Name: `govelox`
- Type: microservice system
- Domain: `Distributed Rate Limiter as a Service`
- Primary transport: `gRPC`
- Secondary transport: `REST`
- Language: `Go`
- Module path currently in `go.mod`: `github.com/Vyzz1/go-velox.git`

## System Overview

This repository is intended to host a distributed rate-limiting platform composed of four services plus shared infrastructure.

Core design goals:

- low-latency rate-limit decisions
- distributed coordination across nodes
- tenant-scoped configuration
- hot-reloadable rules
- operability via metrics and tracing

## Microservices

### `api-gateway` on `:8080`

- Entry point for external clients and peer services
- Exposes `gRPC` and may also expose `REST`
- Handles request validation, auth/middleware, request shaping, and forwarding to internal services
- Should remain thin; business logic belongs in downstream services

### `limiter-engine` on `:9090`

- Internal `gRPC` service
- Core decision engine for rate-limiting
- Executes Redis-backed logic, including Lua scripts for atomic limit evaluation
- Owns algorithms such as token bucket, sliding window, fixed window, or leaky bucket if implemented

### `config-service` on `:8081`

- `REST` service for rule management
- Stores and serves tenant-specific configuration
- Publishes or reacts to config changes through `etcd`
- Supports hot reload without requiring full service restarts

### `sync-agent` on `:7070` UDP

- Handles gossip-based peer discovery
- Used when scaling multiple nodes of the system
- Keeps cluster topology and peer awareness synchronized
- Should stay focused on membership and node-state propagation, not request-path logic

## Infrastructure From `docker-compose.yml`

The current compose file provisions supporting infrastructure, not the four app services yet.

### Redis Cluster

- `6` Redis containers
- Topology: `3 masters + 3 replicas`
- Internal Redis port: `6379`
- Host ports: `6371` to `6376`
- A one-shot `redis-cluster-init` container creates the cluster

Use Redis Cluster for:

- distributed counters
- atomic limit evaluation
- Lua script execution
- high availability across limiter nodes

### Etcd

- Client port: `2379`
- Peer port: `2380`
- Used for dynamic configuration and hot reload signaling

### Jaeger

- UI port: `16686`
- OTLP gRPC: `4317`
- OTLP HTTP: `4318`
- Used for tracing across gateway, engine, config service, and sync flows

### Prometheus

- Port: `9090`
- Config file mounted from `infra/prometheus/prometheus.yml`

### Grafana

- Port: `3000`
- Provisioning mounted from `infra/grafana/provisioning`

## Intended Repository Layout

Keep new code aligned with this structure:

```text
go-velox/
├── go.mod
├── go.sum
├── Makefile
├── docker-compose.yml
├── .gitignore
├── AGENTS.md
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

## Architecture Rules

- Keep transport handlers thin; put business logic under `internal/`.
- Shared reusable packages go in `pkg/`; avoid putting service-specific logic there.
- `api-gateway` should orchestrate, not own limiter state.
- `limiter-engine` is the authority for rate-limit decisions.
- Redis access patterns must preserve atomicity; prefer Lua or carefully designed transactions.
- Config changes should be tenant-aware and propagate safely through `etcd`.
- Gossip concerns belong to `sync-agent`; do not mix them into gateway or engine request handlers.
- Prefer explicit interfaces around storage, peer discovery, and config watchers.

## gRPC And API Guidance

- Define service contracts in `proto/` first.
- Generate stubs from `.proto` files rather than hand-writing transport DTOs.
- Keep public API messages stable and versionable.
- Treat internal engine RPCs as separate from external gateway-facing contracts when semantics differ.

## Observability Guidance

- Every service should emit structured logs.
- Every service should expose Prometheus metrics.
- gRPC and HTTP handlers should be trace-instrumented.
- Propagate trace and request IDs across service boundaries.

## Local Development Expectations

Before considering work complete, run the relevant local quality checks when the code exists:

- `gofmt` on touched Go files
- `go test ./...`
- project lint target if/when configured
- relevant `make` targets if the Makefile is populated

Suggested future `Makefile` targets:

- `make fmt`
- `make lint`
- `make test`
- `make proto`
- `make run-gateway`
- `make run-engine`
- `make run-config`
- `make run-sync`

## Agent Working Rules

- Read `docker-compose.yml` before changing infra assumptions.
- Preserve service boundaries; do not collapse multiple services into one package for convenience.
- Prefer incremental scaffolding that compiles over speculative large code drops.
- When adding a service, create its `cmd/<service>/main.go` entry point and corresponding `internal/...` package together.
- Keep config centralized and environment-driven.
- Document new ports, protocols, env vars, and dependencies in this file when architecture changes.

## Current State Notes

- The repository already contains infra for Redis Cluster, Etcd, Jaeger, Prometheus, and Grafana.
- The application service directories described above may still need to be scaffolded.
- `Makefile` is currently empty, so any automation targets still need to be added.
