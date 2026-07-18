# Bug Report: Gossip không hợp cluster trên k8s — seed DNS chưa sẵn sàng + `Join` không retry

- **Ngày**: 2026-07-18
- **Service**: `sync-agent` (chạy làm sidecar của `limiter-engine` trên Kubernetes)
- **File liên quan**: `cmd/sync-agent/main.go` (đường `Join`), `deploy/k8s/engine.yaml` (`SEEDS`)
- **Mức độ**: Cao — mỗi node thành một cluster 1 thành viên; gateway chỉ thấy 1 engine, mất routing/failover đa node
- **Trạng thái**: Đã khắc phục ở tầng manifest (đổi `SEEDS`) & kiểm chứng; **đề xuất fix tại code** (retry `Join`) — xem mục 7

---

## 1. Tóm tắt

Khi triển khai `deploy/k8s` lần đầu bằng `kubectl apply`, ba engine Pod lên đủ
(`2/2 Running`) nhưng **gossip không hợp thành một cluster**: mỗi `sync-agent`
sidecar chỉ thấy **chính nó**. Endpoint `/v1/members` (qua Service
`velox-membership` có load-balance) trả về `count:1` với đúng một thành viên
`local:true`.

Hệ quả: `api-gateway` poll `/v1/members` → chỉ nhận được **một** engine bất kỳ
mà LB trỏ tới → hash ring chỉ có 1 node → toàn bộ tenant dồn về 1 engine, mất
hoàn toàn ý nghĩa fleet 3 node và khả năng failover.

Khác với bug deadlock trước đây (service treo), lần này **mọi thứ "xanh"** —
Pod Ready, `/v1/members` trả lời tức thì — chỉ là **nội dung sai** (count:1 thay
vì count:3). Đây là kiểu lỗi âm thầm, nguy hiểm vì dễ tưởng đã chạy đúng.

---

## 2. Triệu chứng quan sát được

`/v1/members` (LB tới sidecar của engine-1) — chỉ thấy chính nó:

```json
{"local":"velox-engine-1","count":1,"members":[
  {"id":"velox-engine-1","addr":"10.244.1.9:7070","state":"alive","local":true,
   "role":"engine","engine_addr":"velox-engine-1.velox-engine:9090","healthy":true}
]}
```

Đáng chú ý: `healthy:true` — tức **health-check probe engine vẫn chạy đúng**
(sidecar dò được engine kế bên qua chính DNS `velox-engine-1.velox-engine:9090`).
Vậy DNS Pod **có** hoạt động; vấn đề nằm ở **thời điểm** gossip cố resolve seed.

---

## 3. Bằng chứng: log `Join` thất bại

Log `sync-agent` của cả ba Pod, ngay lúc khởi động:

```
# velox-engine-0 (start ts=1784386143.78)
[WARN] memberlist: failed to resolve velox-engine-0.velox-engine:7070:
       lookup velox-engine-0.velox-engine on 10.96.0.10:53: no such host   (ts=143.91)
join cluster failed  error="failed to resolve velox-engine-0.velox-engine:7070: ... no such host"

# velox-engine-1 (start ts=1784386146.85) → cùng lỗi ở ts=146.89
# velox-engine-2 (start ts=1784386152.14) → cùng lỗi ở ts=152.18
```

Mỗi node cố `Join` vào seed chỉ **~130ms sau khi khởi động**, và **không bao giờ
thử lại**. Sau đó là hàng loạt log `/v1/members` trả `200` bình thường — service
sống, chỉ là chưa từng vào được cluster của ai.

### Kiểm chứng DNS ở thời điểm hiện tại (sau khi Pod đã chạy vài phút)

```
# short name (dạng pod.service) — KHÔNG resolve:
$ nslookup velox-engine-0.velox-engine
** server can't find velox-engine-0.velox-engine: NXDOMAIN

# FQDN đầy đủ — resolve OK:
$ nslookup velox-engine-0.velox-engine.velox.svc.cluster.local
Name:   velox-engine-0.velox-engine.velox.svc.cluster.local
Address: 10.244.1.5
```

> Lưu ý: `nslookup` của busybox không áp search-domain nên short name ra NXDOMAIN;
> resolver của Go (dùng bởi cả memberlist lẫn health-probe) **có** áp search-domain,
> nên short name của `ENGINE_ADDR` vẫn probe được **một khi record đã tồn tại**.
> Điểm mấu chốt không phải "short vs FQDN" mà là **record chưa tồn tại lúc `Join`**.

---

## 4. Nguyên nhân gốc

Ba yếu tố cộng hưởng:

### 4.1 `Join` chỉ chạy một lần, không retry

`cmd/sync-agent/main.go` gọi `Join` đúng một lần trong luồng khởi động:

```go
if n, err := gossip.Join(seeds); err != nil {
    // Not fatal: the node still runs and peers can join it later.
    log.Warn("join cluster failed", zap.Error(err))
}
```

Comment "peers can join it later" **đúng trên môi trường compose** (DNS container
có sẵn ngay), nhưng **sai trên k8s**: nếu `Join` fail, node này không bao giờ chủ
động thử lại, và cũng không ai "join ngược" vào nó nếu tất cả cùng fail.

### 4.2 Record DNS của headless Service chưa được publish lúc Pod vừa boot

DNS per-Pod của một headless Service chỉ xuất hiện sau khi EndpointSlice được cập
nhật — có độ trễ vài trăm ms → vài giây sau khi Pod khởi tạo. `sync-agent` gọi
`Join` chỉ ~130ms sau boot ⇒ lúc đó seed **chưa resolve được** ⇒ `no such host`.

`publishNotReadyAddresses: true` giúp record xuất hiện sớm hơn (không đợi Ready)
nhưng **vẫn không tức thời** — không đủ nhanh cho một lời gọi `Join` ở mốc 130ms.

### 4.3 Seed trỏ vào **một** Pod cụ thể (pod-0) làm hỏng thêm

`SEEDS` ban đầu là `velox-engine-0.velox-engine:7070` — chỉ nhắm **đúng pod-0**.
Điều này khiến:
- Toàn bộ cluster phụ thuộc DNS của **một** Pod sẵn sàng đúng thời điểm.
- **pod-0 tự seed chính nó** — vô nghĩa; nó không thể "join" ai để mồi cluster.
- Nếu pod-0 restart, các node khác không có cách rejoin (seed đã chết).

### Chuỗi gây lỗi

```
Pod khởi động  →  gossip.Join("velox-engine-0.velox-engine:7070")   (t ≈ boot+130ms)
                    → resolve seed  →  record CHƯA publish  →  "no such host"
                    → Join trả lỗi  →  log Warn, KHÔNG retry
                    → node đứng một mình  →  /v1/members count:1  (vĩnh viễn)
```

---

## 5. Cách khắc phục (tầng manifest)

Đổi `SEEDS` từ **DNS của một Pod** sang **FQDN của headless Service**. Tên Service
resolve ra **tất cả** Pod IP (và với `publishNotReadyAddresses` gồm cả Pod chưa
Ready), nên bất kỳ Pod nào còn sống đều làm seed để mồi một node mới — không còn
phụ thuộc pod-0, và bền vững khi restart.

### Trước

```yaml
# deploy/k8s/engine.yaml (sidecar env)
- { name: SEEDS, value: "velox-engine-0.velox-engine:7070" }   # chỉ pod-0, short name
```

### Sau

```yaml
- { name: SEEDS, value: "velox-engine.velox.svc.cluster.local:7070" }  # headless Service FQDN → mọi Pod
```

Vì sao cách này hội tụ đáng tin cậy dù `Join` vẫn không retry:

- StatefulSet khởi động **tuần tự** (pod-0 → pod-1 → pod-2). Khi pod-1/pod-2
  `Join`, pod-0 đã chạy trước đó và record của nó đã kịp publish ⇒ resolve OK.
- Seed là tên Service (nhiều IP) nên node mới luôn tìm được **một peer đang sống**
  để mồi; pod-0 tự mồi cluster 1-thành-viên là hợp lệ với vai trò node đầu tiên.
- FQDN loại bỏ luôn mọi mơ hồ về search-domain/ndots.

---

## 6. Kiểm chứng

Sau khi `kubectl apply` lại (rolling update do đổi Pod template):

```json
{"local":"velox-engine-2","count":3,"members":[
  {"id":"velox-engine-0","state":"alive","role":"engine","healthy":true,
   "engine_addr":"velox-engine-0.velox-engine:9090"},
  {"id":"velox-engine-1","state":"alive","role":"engine","healthy":true, ...},
  {"id":"velox-engine-2","state":"alive","role":"engine","healthy":true,"local":true, ...}
]}
```

- Cả ba sidecar đồng thuận `count:3`, tất cả `alive` + `healthy:true`.
- Gateway dựng ring 3 node; traffic `POST /v1/check` cho `200` rồi `429` đúng kỳ vọng.
- Kill 1 engine giữa tải: `800×200, 0×502` — failover hoạt động.

Bản Helm (`deploy/helm/govelox`, template đã nhúng sẵn `SEEDS` = Service FQDN) khi
`helm install` **hội tụ ngay lần đầu** — không cần re-apply thủ công như bản raw.

---

## 7. Bài học & phòng ngừa

1. **Trên k8s, DNS chưa sẵn sàng ngay khi Pod boot.** Bất kỳ thao tác nào phụ
   thuộc DNS trong luồng khởi động (join gossip, resolve peer, connect seed) phải
   chịu được resolve fail ở những giây đầu — bằng **retry**, không phải "thử một
   lần rồi thôi".
2. **Seed off một tập (Service), không off một Pod.** FQDN của headless Service
   resolve ra mọi Pod IP → bất kỳ node sống nào cũng mồi được, bền khi restart.
   Trỏ vào một Pod cụ thể tạo điểm phụ thuộc đơn lẻ.
3. **Dùng FQDN cho seed liên-node.** Loại bỏ phụ thuộc search-domain/ndots vốn
   khác nhau giữa các cluster/CNI.
4. **Cảnh giác lỗi "xanh nhưng sai".** Khác deadlock (service chết), lỗi này để
   service Ready và trả lời bình thường nhưng **nội dung sai** (`count:1`). Phải
   kiểm chứng *giá trị* (đủ 3 thành viên), không chỉ *trạng thái* (Pod Ready, 200).
5. **Giả định của môi trường dev có thể sai ở prod.** Comment "peers can join
   later" đúng với DNS container của docker-compose nhưng sai với vòng đời
   EndpointSlice của k8s.

### Đề xuất fix tại code (chưa làm — dự kiến Phase 3)

Sửa manifest chỉ **giảm nhẹ** triệu chứng; gốc rễ là `Join` không retry. Nên bọc
`Join` trong vòng lặp có backoff, chạy tới khi mồi được ít nhất một peer:

```go
// Phác thảo — retry Join tới khi thành công (hoặc ctx hủy).
func joinWithRetry(ctx context.Context, gossip *cluster.Memberlist, seeds []string, log *zap.Logger) {
    if len(seeds) == 0 {
        return // node đầu tiên: không có seed là bình thường
    }
    backoff := time.Second
    for {
        if n, err := gossip.Join(seeds); err == nil && n > 0 {
            log.Info("joined cluster", zap.Int("contacted", n))
            return
        }
        select {
        case <-ctx.Done():
            return
        case <-time.After(backoff):
        }
        if backoff < 15*time.Second {
            backoff *= 2
        }
    }
}
```

Với retry ở code, cụm sẽ tự hội tụ **bất kể** thời điểm DNS sẵn sàng hay thứ tự
khởi động — kể cả khi seed trỏ vào một Pod. Đây là cách phòng ngừa đúng gốc; đổi
`SEEDS` sang Service FQDN vẫn nên giữ như một lớp bền vững bổ sung.
```
