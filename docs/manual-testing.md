# Hướng dẫn test tay (manual testing) — go-velox

Runbook test thủ công cho stack sidecar-topology: **api-gateway → consistent-hash ring → fleet limiter-engine**, membership qua **sync-agent gossip**, rule hot-reload qua **config-service → etcd**.

> Lệnh viết cho **Git Bash** trên Windows. Lưu ý cái bẫy MSYS path-conversion ở [Phụ lục C](#phụ-lục-c--bẫy-thường-gặp).

---

## 0. Chuẩn bị

### 0.1 Khởi động stack

```bash
docker compose --profile stack up -d --build
```

Stack gồm: 3× `limiter-engine`, 3× `sync-agent` (sidecar 1:1), `api-gateway`, `config-service`, `postgres`, `etcd`, Redis Cluster (6 node), jaeger, prometheus, grafana.

### 0.2 Đợi mọi thứ "ready"

Đợi cả 3 engine được sidecar báo `healthy:true` (sidecar khởi đầu là `unhealthy`, chỉ bật `healthy` sau lần probe thành công đầu tiên):

```bash
until [ "$(curl -s localhost:7072/v1/members | grep -o '"healthy":true' | wc -l)" = "3" ]; do
  echo "đang đợi 3 engine healthy..."; sleep 2;
done
echo "OK — 3 engine đã healthy"
```

Kiểm tra nhanh các cổng:

```bash
curl -s localhost:8080/healthz  && echo " <- gateway OK"
curl -s localhost:8081/v1/tenants/_/rules && echo " <- config-service OK"
curl -s localhost:7072/v1/members | python -m json.tool   # hoặc | jq
```

### 0.3 Bảng cổng (host)

| Service | Cổng host | Dùng để |
|---|---|---|
| api-gateway | `8080` | `POST /v1/check` (entry point) |
| config-service | `8081` | CRUD rule |
| sync-agent-1 / 2 / 3 | `7072 / 7073 / 7074` | `GET /v1/members` |
| limiter-engine-1 / 2 / 3 (gRPC) | `9090 / 9095 / 9097` | grpcurl trực tiếp (tùy chọn) |
| etcd | `2379` | xem rule đã publish |
| postgres | `5432` | source of truth |
| jaeger UI | `16686` | trace gateway→engine |

---

## 1. Test rate-limit cơ bản (smoke)

Rule mặc định khi tenant chưa có cấu hình: `limit=100 / period=60s / burst=10` (fallback `LIMITER_DEFAULT_*`).

### 1.1 Một request

```bash
curl -i -X POST localhost:8080/v1/check \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"acme","subject":"u1","resource":"/orders","action":"POST"}'
```

Kỳ vọng: `HTTP/1.1 200 OK`, có header `X-RateLimit-Limit: 100`, `X-RateLimit-Remaining: <số>`.

### 1.2 Bắn dồn để thấy 429

Bắn **song song** để vượt hẳn tốc độ refill (~1.67 req/s) → thấy burst rồi bị chặn:

```bash
seq 1 60 | xargs -P 20 -I{} curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST localhost:8080/v1/check \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"burst-test","subject":"u"}' | sort | uniq -c
```

Kỳ vọng: một nhúm `200` (≈ burst 10 + vài token refill) và phần lớn còn lại `429`.

> **Vì sao chạy `while true` thấy 200/429 xen kẽ chứ không 429 liên tục?**
> Sau khi cạn burst, GCRA chỉ nhả ~1.67 token/giây (`limit/period`). Vòng lặp tuần tự với `curl` chỉ ~2–3 req/s nên cứ vài cái lọt (200) lại 1 cái rớt (429). Đó là limiter đang ghìm đúng mức cho phép — **không phải bug**. Muốn 429 dày đặc thì bắn song song như 1.2.

---

## 2. Test hot-reload rule (config-service → etcd → engine)

Mục tiêu: chứng minh đổi rule qua config-service có hiệu lực ở engine **không cần restart**. Chuỗi: `PUT config-service` → Postgres (source of truth) + publish etcd → engine watch prefix → cập nhật snapshot in-memory.

Dùng 1 tenant mới mỗi lần để tránh dính state cũ:

```bash
TENANT="hot-$RANDOM"
echo "tenant test = $TENANT"
```

Hàm tiện ích đọc `limit` mà engine đang áp (qua gateway header):

```bash
showlimit() {
  curl -s -D - -o /dev/null -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d "{\"tenant_id\":\"$TENANT\",\"subject\":\"probe\"}" \
    | grep -i 'x-ratelimit-limit'
}
```

### [A] Baseline — chưa có rule → fallback 100

```bash
showlimit       # kỳ vọng: X-RateLimit-Limit: 100
```

### [B] PUT rule mới → engine nhận trong vài giây

```bash
curl -i -X PUT "localhost:8081/v1/tenants/$TENANT/rules/default" \
  -H 'Content-Type: application/json' \
  -d '{"algorithm":"gcra","limit":7,"period_secs":60,"burst":2}'
# kỳ vọng: HTTP 200

# đợi watch lan tới engine rồi đọc lại
until [ -n "$(showlimit | grep -i '7')" ]; do sleep 1; done
showlimit       # kỳ vọng: X-RateLimit-Limit: 7
```

### [C] UPDATE rule (PUT lại) → đổi live

```bash
curl -s -o /dev/null -X PUT "localhost:8081/v1/tenants/$TENANT/rules/default" \
  -H 'Content-Type: application/json' \
  -d '{"algorithm":"gcra","limit":42,"period_secs":60,"burst":5}'

until [ -n "$(showlimit | grep -i '42')" ]; do sleep 1; done
showlimit       # kỳ vọng: X-RateLimit-Limit: 42
```

### [D] DELETE rule → quay về fallback 100

```bash
curl -i -X DELETE "localhost:8081/v1/tenants/$TENANT/rules/default"
# kỳ vọng: HTTP 204

until [ -n "$(showlimit | grep -i '100')" ]; do sleep 1; done
showlimit       # kỳ vọng: X-RateLimit-Limit: 100 (đã rơi về default)
```

### [E] Kiểm tra source-of-truth (Postgres) đã sạch

```bash
docker exec velox-postgres psql -U velox -d velox -tAc \
  "SELECT count(*) FROM rules WHERE tenant_id='$TENANT';"
# kỳ vọng: 0
```

### (Tùy chọn) Xem rule đang nằm trong etcd

```bash
# LƯU Ý: phải có MSYS_NO_PATHCONV=1, nếu không Git Bash sẽ đổi /velox/... thành path Windows
MSYS_NO_PATHCONV=1 docker exec -e ETCDCTL_API=3 velox-etcd \
  etcdctl get --prefix /velox/rules/ -w fields
```

> **Tóm tắt hot-reload PASS:** `100 (default) → 7 → 42 → 100`, mọi bước có hiệu lực mà engine **không restart**; Postgres khớp với trạng thái cuối.

---

## 3. Test health & failover (giết 1 engine)

Mục tiêu: giết 1 engine → sidecar phát hiện → gỡ khỏi ring → traffic vẫn phục vụ (không 502), không mất counter (Redis Cluster dùng chung) → bật lại → engine rejoin ring.

**Tham số timing (mặc định):**
- Sidecar probe engine mỗi **2s**, sau **3 lần fail liên tiếp** → `healthy:false` (~6s).
- Gossip lan trạng thái sang sync-agent-1 (~1–2s).
- Gateway poll `/v1/members` mỗi **5s** rồi rebuild ring.
- ⇒ Tổng từ lúc giết tới khi ring bỏ engine: **~10–15s**.
- Khi bật lại: chỉ cần **1 lần probe thành công** → `healthy:true`. Nếu engine còn ấm (chỉ gossip/probe): **~5–8s**; nếu **cold restart** (`docker start` engine đã chết) thì phải cộng thời gian engine boot lại + nối Redis Cluster + nạp etcd watch → thực tế **~20–25s** (xem [Phụ lục E](#phụ-lục-e--nhật-ký-nghiệm-thu)).

### 3.1 Trạng thái ban đầu — 3 engine healthy

```bash
curl -s localhost:7072/v1/members \
  | python -c 'import sys,json;[print(m["id"],m["engine_addr"],"healthy="+str(m["healthy"]),m["state"]) for m in json.load(sys.stdin)["members"] if m["role"]=="engine"]'
```

Kỳ vọng: 3 dòng, tất cả `healthy=True state=alive`.

### 3.2 (Terminal phụ) Bắn traffic liên tục trong lúc giết engine

Mở **một Git Bash khác**, chạy vòng lặp quan sát — mục tiêu là **không xuất hiện `502`** suốt quá trình:

```bash
while true; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d '{"tenant_id":"acme","subject":"u1"}')
  echo "$(date +%T)  -> $code"
  sleep 0.3
done
```

`200`/`429` xen kẽ là bình thường (mục 1.2). Cái cần theo dõi: **không có `502`** (502 = gateway không gọi được engine nào).

### 3.3 Giết engine-2

```bash
docker stop velox-limiter-engine-2
```

> Lưu ý: chỉ giết **engine**, sidecar `sync-agent-2` vẫn sống và sẽ probe fail → tự hạ cờ `healthy`. Đây đúng là kịch bản failover thật.

### 3.4 Quan sát sidecar hạ cờ trong ~10–15s

```bash
# đợi đúng engine-2 chuyển healthy:false
until curl -s localhost:7072/v1/members \
  | python -c 'import sys,json;ms=json.load(sys.stdin)["members"];import sys as s;sys.exit(0 if any(m["engine_addr"]=="limiter-engine-2:9090" and not m["healthy"] for m in ms) else 1)'; do
  echo "đang đợi engine-2 -> unhealthy..."; sleep 2;
done
echo "engine-2 đã bị đánh dấu unhealthy"
```

Kiểm tra lại danh sách: engine-2 giờ `healthy=False` → gateway đã loại nó khỏi ring (poll ≤5s sau đó). Terminal traffic ở 3.2 vẫn `200/429`, **không 502**.

### 3.5 (Tùy chọn) Xác nhận traffic dồn về 2 engine còn lại

```bash
docker logs --since 20s velox-limiter-engine-1 2>&1 | grep -c CheckLimit
docker logs --since 20s velox-limiter-engine-3 2>&1 | grep -c CheckLimit
docker logs --since 20s velox-limiter-engine-2 2>&1 | grep -c CheckLimit   # ~0 vì đã chết
```

### 3.6 Bật lại engine-2 → rejoin ring

```bash
docker start velox-limiter-engine-2

until curl -s localhost:7072/v1/members \
  | python -c 'import sys,json;ms=json.load(sys.stdin)["members"];import sys as s;sys.exit(0 if any(m["engine_addr"]=="limiter-engine-2:9090" and m["healthy"] for m in ms) else 1)'; do
  echo "đang đợi engine-2 rejoin..."; sleep 2;
done
echo "engine-2 đã healthy trở lại — ring đủ 3 node"
```

Dừng vòng lặp traffic ở 3.2 (`Ctrl-C`).

> **Tóm tắt failover PASS:** giết engine-2 → trong ~10–15s sidecar hạ `healthy`, ring co còn 2 node, traffic **không gián đoạn (không 502)**; bật lại → rejoin trong ~5–8s. Counter không mất vì engine stateless trên Redis Cluster chung.

---

## 4. Kiểm chứng load balancing (traffic trải ra 3 engine)

> **Hiểu trước:** gateway route bằng **consistent hashing theo `tenant_id`** (`ring.LocateKey([]byte(tenantID))`), **không** round-robin từng request. ⇒ cùng 1 tenant **luôn** về cùng 1 engine (đúng thiết kế — counter ownership ổn định). Muốn thấy cả 3 engine nhận traffic, phải bắn **nhiều tenant khác nhau**.

Hai tín hiệu đếm được mỗi engine xử lý bao nhiêu request:
- **Log:** mỗi RPC engine ghi 1 dòng `grpc call`.
- **Metric:** `velox_limiter_checks_total` trên cổng metrics riêng từng engine (host `9091` / `9096` / `9098`).

### 4.1 Bắn nhiều tenant khác nhau

```bash
for i in $(seq 1 60); do
  curl -s -o /dev/null -X POST localhost:8080/v1/check \
    -H 'Content-Type: application/json' \
    -d "{\"tenant_id\":\"tenant-$i\",\"subject\":\"u\"}"
done
```

### 4.2 Đếm phân phối — cách A: qua log

```bash
for c in 1 2 3; do
  printf "engine-%s: " $c
  docker logs --since 90s velox-limiter-engine-$c 2>&1 | grep -c 'grpc call'
done
```

### 4.2 Đếm phân phối — cách B: qua metric (chính xác hơn)

```bash
for p in 9091 9096 9098; do
  printf "engine @%s: " $p
  curl -s localhost:$p/metrics | grep '^velox_limiter_checks_total' \
    | awk '{s+=$NF} END{print s+0}'
done
```

**Kỳ vọng:** cả 3 engine đều `> 0`, chia gần đều (60 tenant → mỗi engine ~15–25). Không cần bằng nhau tuyệt đối vì ring dùng bounded-load `1.25` + `271` partition. Cả 3 cùng `> 0` ⇒ **load đã trải ra fleet thành công**.

### 4.3 Đối chứng tính "sticky"

Bắn nhiều lần **cùng một** tenant → chỉ đúng 1 engine tăng counter:

```bash
for i in $(seq 1 20); do
  curl -s -o /dev/null -X POST localhost:8080/v1/check \
    -d '{"tenant_id":"tenant-7","subject":"u"}'
done
# rồi chạy lại 4.2 — chỉ 1 trong 3 engine tăng thêm 20
```

> Kết hợp với mục 3: kill engine-2 rồi bắn lại 60 tenant → chỉ engine-1 & 3 tăng, các tenant từng thuộc engine-2 **tự dạt** sang 2 node còn lại (ring rebalance) — minh chứng failover + rebalance trong một phép thử.

---

## Phụ lục A — tham chiếu API

### api-gateway `POST :8080/v1/check`
Body: `{tenant_id (bắt buộc), subject, resource, action, rule_id, cost, metadata}`
Đáp: `200` (allowed) / `429` (denied, kèm `Retry-After`) / `400` (thiếu tenant_id) / `502` (engine lỗi).
Header luôn có: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`.

### config-service (`:8081`)
- `PUT /v1/tenants/{tenant}/rules/{rule}` — body `{algorithm, limit, period_secs, burst}` → `200`
- `GET /v1/tenants/{tenant}/rules/{rule}` → `200` / `404`
- `GET /v1/tenants/{tenant}/rules` → mảng rule
- `DELETE /v1/tenants/{tenant}/rules/{rule}` → `204` / `404`

### sync-agent (`:7072` / `:7073` / `:7074`)
- `GET /v1/members` → `{local, count, members:[{id, addr, state, local, role, engine_addr, healthy}]}`

---

## Phụ lục B — GCRA (vì sao remaining có lúc > limit)

- `limit` = tốc độ refill bền vững (token/period).
- `burst` = sức chứa tối đa của xô.
- `remaining` báo số token còn lại trong xô, nên có thể **lớn hơn** `limit` khi `burst > limit`.
- Refill ≈ `limit/period` token mỗi giây; xô đầy cho phép bắn dồn tới `burst` request tức thì.

---

## Phụ lục C — bẫy thường gặp

| Triệu chứng | Nguyên nhân | Cách xử lý |
|---|---|---|
| `etcdctl get /velox/...` trả rỗng trong Git Bash | MSYS đổi `/velox/...` thành path Windows | thêm `MSYS_NO_PATHCONV=1` trước `docker exec` |
| `etcdctl` báo no `sh` | container etcd là distroless | gọi thẳng `etcdctl`, đừng `sh -c` |
| Gateway log `failed to fetch members ... context deadline exceeded` | sync-agent chưa sẵn / treo | kiểm tra `curl :7072/v1/members`; xem `docker logs velox-sync-agent-1` |
| `/v1/check` trả `502` | gateway không có engine healthy nào trong ring | xem `/v1/members`, kiểm tra engine còn sống + `healthy:true` |
| Đổi rule mà engine không nhận | watch chưa kịp / publish lỗi | đợi vài giây; xác nhận key trong etcd (Phụ lục mục 2 tùy chọn) |
| `sleep 25` bị block bởi harness | — | dùng vòng `until <điều kiện>; do sleep 2; done` |

---

## Phụ lục D — dọn dẹp

```bash
docker compose --profile stack down          # giữ volume (postgres/etcd data)
docker compose --profile stack down -v       # xóa luôn volume — reset sạch
```

---

## Phụ lục E — Nhật ký nghiệm thu

Kết quả một lần chạy thực tế (`make infra-stack`, ngày **2026-06-26**) để đối chiếu kỳ vọng ở các mục trên.

### Khởi động
- 8 container app `Up`: 3× engine, 3× sync-agent, gateway, config-service (+ infra: Redis Cluster, postgres, etcd, jaeger, prometheus, grafana).
- `/v1/members`: cả 3 engine `healthy=True state=alive`; gateway smoke `POST /v1/check` → `200`.

### Mục 3 — Health & failover (kill `velox-limiter-engine-2`)
| Mốc | Đo được | Kỳ vọng |
|---|---|---|
| kill engine → sidecar hạ `healthy:false` | **6s** | ~6s (3×2s probe) ✓ |
| restart (cold) → rejoin `healthy:true` | **23s** | ~20–25s cold boot ✓ |
| traffic suốt failover (400 request, `acme`, 0.3s/lần) | **262× `200`, 138× `429`, `0× 502`** | không `502` ✓ |

⇒ **PASS**: giết engine không gây gián đoạn (0 lỗi 502); ring tự co rồi giãn lại khi engine quay về.

### Mục 4 — Load balancing (60 tenant khác nhau)
Phân phối qua metric `velox_limiter_checks_total` mỗi engine: **engine-1 = 10, engine-2 = 20, engine-3 = 21** — cả 3 đều `>0`, chia gần đều (bounded-load `1.25`). ✓

### A2 — Observability
- **Prometheus** (`:9092`): cả **9/9 target `up=1`** (3 engine `:9091`, 3 sync-agent `:7071`, gateway `:8090`, config `:8082`, prometheus self) — scrape qua DNS container của profile `stack`.
- `sum(velox_limiter_checks_total)` truy vấn được trong Prometheus sau ~1 chu kỳ scrape (15s).
- **Grafana** (`:3000`): dashboard *"Velox — Rate Limiter Overview"* (`uid=velox-overview`) tự nạp qua provisioning; datasource `uid=velox-prometheus`.

> **Lưu ý metric:** `velox_limiter_checks_total` / `..._check_duration_seconds` là `CounterVec`/`HistogramVec` **in-process từng engine** → chỉ xuất hiện series sau request đầu tiên, và **reset về 0 khi engine restart**. Vì `tenant_id` route sticky theo consistent hash, một tenant chỉ làm tăng counter ở đúng engine sở hữu nó. `velox_limiter_redis_errors_total` là counter thường nên luôn hiện (=0 khi không lỗi).
