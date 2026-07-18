# govelox on Kubernetes — Phase 1 (raw manifests)

Hand-written manifests that prove the tricky part of the topology on k8s:
the **engine + sync-agent sidecar** pairing, gossip membership, and gateway
discovery. Once this runs, Phase 2 wraps it in a Helm chart. Do the raw
manifests first — Helm only packages what already works; it does not decide the
design below for you.

## The three design decisions (and why)

| Choice | Why not the default |
|---|---|
| **StatefulSet** for engine+sidecar | Deployment gives random Pod names/IPs on restart → gossip seed address breaks and a rejoining node is stranded. StatefulSet gives stable `velox-engine-{0,1,2}` names + DNS. |
| **Headless Service** (`clusterIP: None`) | A normal Service hides Pods behind one load-balanced VIP. Gossip must seed off a *specific* peer and the gateway must dial a *specific* engine — both need per-Pod DNS, which only a headless Service publishes. |
| **Two containers, one Pod** | The sidecar must probe *its* engine and share its lifecycle. Co-locating them makes the 1:1 pairing automatic — scale `replicas` and you get matched pairs for free. |

Plus one trap docker-compose hid: `ENGINE_ADDR` is used for **both** probing the
local engine and for the address gossiped to the gateway. It therefore **cannot
be `localhost`** (the gateway would dial itself) — it must be the Pod's routable
DNS `$(POD_NAME).velox-engine:9090`. See the comment in `engine.yaml`.

Note the two Services over the same StatefulSet, with opposite intents:
`velox-engine` (headless, for gossip + direct gRPC) and `velox-membership`
(ClusterIP, load-balances the gateway's poll across all 3 sidecars — removing the
single-sidecar SPOF compose had by hardcoding `sync-agent-1`).

## Prerequisites (not in these files)

The engine needs Redis Cluster + etcd; config-service needs Postgres; traces go
to Jaeger. Do **not** hand-write these — install charts/operators and point the
env at their Service DNS (`redis-cluster:6379`, `etcd:2379`, `jaeger:4317`):

- Redis Cluster — Bitnami `redis-cluster` chart or a Redis operator
- Postgres — CloudNativePG operator or Bitnami `postgresql`
- etcd — Bitnami `etcd` chart
- Jaeger — jaeger-operator or the all-in-one manifest

Also build + push the images (`govelox/limiter-engine`, `govelox/sync-agent`,
`govelox/api-gateway`) to a registry your cluster can pull from.

## Apply

```bash
kubectl apply -f namespace.yaml
kubectl apply -f engine.yaml
kubectl apply -f gateway.yaml
kubectl -n velox rollout status statefulset/velox-engine
```

## Verify the hard part actually works

```bash
# 1. Gossip formed a 3-node cluster and every sidecar sees all engines healthy:
kubectl -n velox exec velox-engine-0 -c sync-agent -- \
  wget -qO- localhost:7072/v1/members
# → 3 members, role=engine, healthy=true, distinct engine_addr per Pod.

# 2. The gateway discovered the ring and routes 200→429:
kubectl -n velox port-forward svc/velox-gateway 8080:8080 &
for i in $(seq 1 130); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d '{"tenant_id":"acme","subject":"u1","resource":"/orders","action":"POST"}'
done | sort | uniq -c            # ~110×200, ~20×429

# 3. Failover: kill one engine Pod, confirm no 502s during the outage.
kubectl -n velox delete pod velox-engine-1
# The sidecar in that Pod dies with it; the other sidecars detect the loss via
# SWIM and drop it from /v1/members, so the gateway stops routing there. When the
# StatefulSet recreates velox-engine-1 it rejoins under the SAME name/DNS.
```

## Phase 2

Once the above passes, template these three files into one umbrella Helm chart:
`values.yaml` for the 4 app services + `Chart.yaml` `dependencies:` pulling the
Redis/Postgres/etcd charts above.
