# Testing Phase 1 locally with kind

Self-contained: no external registry, no Redis Cluster. Uses a 3-node kind
cluster + throwaway `dev-infra.yaml` (one standalone Redis + one etcd). Run from
the repo root. Commands are Git-Bash / PowerShell friendly.

## 0. Install kind (Go is already in this repo's toolchain)

```bash
go install sigs.k8s.io/kind@latest      # puts kind in $(go env GOPATH)/bin — ensure it's on PATH
kind version
```

## 1. Create the cluster

```bash
kind create cluster --name govelox --config deploy/k8s/kind-config.yaml
kubectl config use-context kind-govelox
kubectl get nodes                        # expect 1 control-plane + 2 workers, Ready
```

## 2. Build the 3 images and load them into kind

kind nodes can't see your local Docker images until you load them. `IfNotPresent`
in the manifests then uses them without a registry.

```bash
docker build -t govelox/limiter-engine:latest -f cmd/limiter-engine/Dockerfile .
docker build -t govelox/sync-agent:latest     -f cmd/sync-agent/Dockerfile .
docker build -t govelox/api-gateway:latest    -f cmd/api-gateway/Dockerfile .
docker build -t govelox/config-service:latest -f cmd/config-service/Dockerfile .

kind load docker-image govelox/limiter-engine:latest --name govelox
kind load docker-image govelox/sync-agent:latest     --name govelox
kind load docker-image govelox/api-gateway:latest    --name govelox
kind load docker-image govelox/config-service:latest --name govelox
```

## 3. Apply — dependencies first, then the app

```bash
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/dev-infra.yaml
kubectl -n velox rollout status deploy/redis
kubectl -n velox rollout status deploy/etcd
kubectl -n velox rollout status deploy/postgres

kubectl apply -f deploy/k8s/engine.yaml
kubectl apply -f deploy/k8s/gateway.yaml
kubectl apply -f deploy/k8s/config-service.yaml
kubectl -n velox rollout status statefulset/velox-engine   # waits for all 3 Pods
kubectl -n velox rollout status deploy/velox-config
kubectl -n velox get pods -o wide                          # 3 engine Pods, ideally on different nodes
```

Each `velox-engine-N` Pod should show `2/2` ready (engine + sync-agent).

## 4. Verify the hard part

### 4a. Gossip formed a 3-node cluster; all engines healthy

```bash
kubectl -n velox port-forward svc/velox-membership 7072:7072 &
curl -s localhost:7072/v1/members | jq
```
Expect 3 members, each `role":"engine"`, `"healthy":true`, and a **distinct**
`engine_addr` (`velox-engine-0.velox-engine:9090`, `-1`, `-2`). If health is
false, the sidecar can't reach its engine — check the engine container logs.

### 4b. Gateway discovered the ring and enforces the limit (200 → 429)

```bash
kubectl -n velox port-forward svc/velox-gateway 8080:8080 &
for i in $(seq 1 130); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d '{"tenant_id":"acme","subject":"u1","resource":"/orders","action":"POST"}'
done | sort | uniq -c
```
Expect ~110×`200` then ~20×`429` (default limit 100 + burst 10).

### 4c. Failover — kill an engine Pod, confirm no 502s

```bash
# Hammer the gateway in one shell...
while true; do
  curl -s -o /dev/null -w "%{http_code} " -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d '{"tenant_id":"t'$RANDOM'","subject":"u1"}'
done
# ...and in another, kill a Pod:
kubectl -n velox delete pod velox-engine-1
```
You should see `200`/`429` throughout and **no `502`**: the sidecar dies with the
Pod, the other sidecars drop it from `/v1/members` via SWIM, and the gateway
stops routing there within one poll (~5s). The StatefulSet recreates
`velox-engine-1` under the same name/DNS and it rejoins.

### 4d. Hot-reload — change a rule with no engine restart (config-service)

```bash
kubectl -n velox port-forward svc/velox-config 8081:8081 &

# Tighten acme's default rule to a tiny limit:
curl -s -X PUT localhost:8081/v1/tenants/acme/rules/default \
  -H 'Content-Type: application/json' \
  -d '{"algorithm":"gcra","limit":5,"period_secs":60,"burst":0}'

# Within ~1s the engine's etcd watch picks it up (no restart). Re-run 4b:
for i in $(seq 1 20); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d '{"tenant_id":"acme","subject":"u1"}'
done | sort | uniq -c
```
Now `429` appears after ~5 requests instead of ~110 — the new limit took effect
with zero engine restarts. Confirm no engine Pod restarted:
`kubectl -n velox get pods` (RESTARTS still 0).

## 5. Tear down

```bash
kind delete cluster --name govelox
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| engine Pod `CrashLoopBackOff` | Redis/etcd not ready — engine `Ping`s Redis and `Get`s etcd at boot (both fatal). Ensure step 3's rollouts finished first. |
| `/v1/members` shows `healthy:false` | Sidecar probing wrong address. Confirm `ENGINE_ADDR=$(POD_NAME).velox-engine:9090` resolved (it needs the headless Service). |
| gateway returns `502` steadily | No engines in the ring — check `velox-membership` has endpoints (`kubectl -n velox get endpoints velox-membership`). |
| only 1 gossip member seen | Seed DNS wrong. All nodes must seed `velox-engine-0.velox-engine:7070` and the headless Service must exist. |
