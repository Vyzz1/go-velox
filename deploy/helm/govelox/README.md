# govelox Helm chart

Umbrella chart for the govelox distributed rate limiter: **api-gateway**,
**limiter-engine** (with a co-located **sync-agent** gossip sidecar), and
**config-service**. It templates the topology proven by the raw manifests in
[`../../k8s`](../../k8s) and layers on production concerns behind toggles.

## Topology

```
Client ─▶ Ingress ─▶ api-gateway ─┬─▶ limiter-engine (StatefulSet, N replicas) ─▶ Redis Cluster
                                  │        └─ sync-agent sidecar (gossip, SWIM)
                                  └─ polls /v1/members (membership Service) to build the hash ring
config-service ─▶ Postgres (source of truth) ─▶ etcd ─▶ limiter-engine hot-reload
```

- **engine + sync-agent** run as **two containers in one Pod**, deployed as a
  **StatefulSet** for stable gossip identity. A **headless Service** gives per-Pod
  DNS (gossip seeds + direct gRPC); a **ClusterIP membership Service**
  load-balances the gateway's `/v1/members` poll across all sidecars.
- The sidecar's `ENGINE_ADDR` is its Pod's routable DNS (not `localhost`) because
  it is both probed **and** gossiped to the gateway. Gossip seeds off the headless
  Service FQDN so any live Pod bootstraps a joiner.

## Quick start (demo)

Ships with lightweight **devInfra** (a single Redis, etcd, and Postgres) so a
default install runs with no external dependencies. Build/push the four images
first (or `kind load` them for a local cluster).

```bash
helm install velox ./deploy/helm/govelox -n velox --create-namespace
kubectl -n velox rollout status statefulset/velox-engine
```

Then drive it (see [`../../k8s/TESTING.md`](../../k8s/TESTING.md) for the full
runbook: gossip convergence, 200→429, failover, hot-reload).

## Production

Use the [`values-prod.yaml`](values-prod.yaml) overlay — it disables devInfra,
enables the real Bitnami data stores with persistence + HA sizing, and turns on
every hardening toggle:

```bash
helm dependency build ./deploy/helm/govelox        # fetch the Bitnami subcharts
helm install velox ./deploy/helm/govelox -n velox --create-namespace \
  -f ./deploy/helm/govelox/values-prod.yaml
```

## Feature toggles

Every production feature is off by default and enabled via values. Each needs the
listed cluster capability.

| Feature | Values | Requires |
|---|---|---|
| Real **Redis Cluster** | `redis-cluster.enabled=true` | `helm dependency build` |
| Real **Postgres** | `postgresql.enabled=true` | `helm dependency build` |
| Real **etcd** | `etcd.enabled=true` | `helm dependency build` |
| **DATABASE_URL Secret** | always on; `config.existingSecret` to bring your own | — |
| **Resources + PDB** | resource requests are defaults; `engine/gateway.pdb.enabled` | — |
| **NetworkPolicy** | `networkPolicy.enabled=true` | a CNI that enforces NP (Calico/Cilium) |
| **Ingress** | `ingress.enabled=true` | an ingress controller (nginx/Traefik) |
| **HPA autoscaling** | `engine/gateway.autoscaling.enabled=true` | metrics-server + CPU requests |
| **ServiceMonitors** | `serviceMonitor.enabled=true` | Prometheus Operator CRDs |

When a real backend is enabled, its devInfra counterpart is skipped automatically
and the app is pointed at it (`fullnameOverride` keeps the Service names stable,
so the connection strings need no change).

## Data stores: devInfra vs Bitnami

- **devInfra** (default): one throwaway Pod each (no PVC, public images). The
  engine's go-redis `UniversalClient` treats a single address as a standalone
  client, so a real cluster is unnecessary just to exercise the app.
- **Bitnami subcharts** (prod): a genuine 6-node Redis Cluster, Postgres (schema
  loaded via `primary.initdb`), and etcd. When Redis Cluster is enabled,
  `REDIS_ADDRS` is rendered as multiple seed nodes so the client selects a
  **ClusterClient**.

> **⚠️ bitnamilegacy caveat.** Bitnami moved its free images to the
> `bitnamilegacy` repo in 2025 (frozen, no security updates), so the chart
> overrides `image.repository` to `bitnamilegacy/*` and sets
> `global.security.allowInsecureImages`. For real production, migrate to a Redis
> operator / CloudNativePG / Bitnami Secure Images.

## Observability

The chart only declares itself a **scrape target** (ServiceMonitors) and emits
OTLP traces to `otlp.endpoint`. The observability **backends** (Prometheus,
Grafana, Jaeger) are platform-level and installed separately (e.g.
kube-prometheus-stack) — deliberately not bundled here.

## Values reference

See [`values.yaml`](values.yaml) for the full, commented set. Key groups:
`image`, per-service (`engine`, `gateway`, `config`) with `resources` / `pdb` /
`autoscaling`, backend endpoints (`redis`, `etcd`, `postgres`, `otlp`), and the
feature blocks (`networkPolicy`, `ingress`, `serviceMonitor`, `devInfra`,
`redis-cluster`, `postgresql`).

## Verification

Every feature was verified live (mostly on throwaway `kind` clusters): gossip
convergence + failover, Secret-sourced DB access, PDB allowed-disruptions,
NetworkPolicy enforcement under Calico, real Redis Cluster / Postgres / etcd
end-to-end (incl. hot-reload), Ingress routing, Prometheus scraping, and HPA
scale-up under CPU load. Notes are in the project's build log.
