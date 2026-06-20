# Bug Report: Deadlock ở sync-agent `/v1/members` (gossip membership)

- **Ngày**: 2026-06-20
- **Service**: `sync-agent`
- **File lỗi**: `internal/syncagent/cluster/memberlist.go`
- **Mức độ**: Cao — gateway mất hoàn toàn khả năng khám phá engine, hash ring rỗng
- **Trạng thái**: Đã sửa & kiểm chứng end-to-end

---

## 1. Tóm tắt

Sau khi triển khai health-check cho engine sidecar (Phase 2), `sync-agent` bị
**deadlock** ở đường đọc danh sách thành viên. Endpoint `/v1/members` treo vĩnh
viễn, khiến `api-gateway` poll liên tục thất bại với
`context deadline exceeded` và không bao giờ dựng được hash ring để route tới
`limiter-engine`.

Triệu chứng đặc trưng: service khởi động **bình thường** (log `sync-agent ready`),
chạy được **~2 giây** rồi mới treo; `/healthz` vẫn sống, chỉ `/v1/members` chết.

---

## 2. Triệu chứng quan sát được

Log lặp lại ở `api-gateway` mỗi 5 giây (chu kỳ poll):

```
{"level":"error","caller":"client/sync_poller.go:68",
 "msg":"failed to fetch members from sync-agent",
 "error":"Get \"http://sync-agent-1:7072/v1/members\":
          context deadline exceeded (Client.Timeout exceeded while awaiting headers)"}
```

Chẩn đoán nhanh đã loại trừ nguyên nhân mạng:

| Kiểm tra | Kết quả |
|---|---|
| `sync-agent-1` container `up`, port `7072` mapped | ✅ |
| Từ trong gateway: `wget sync-agent-1:7072/healthz` | ✅ trả ngay (exit 0) |
| Từ trong gateway: `wget sync-agent-1:7072/v1/members` | ❌ treo tới timeout |
| Từ chính `sync-agent-1`: `wget localhost:7072/v1/members` (timeout 12s) | ❌ vẫn treo |

→ Không phải lỗi mạng/DNS/port. `/healthz` sống nhưng `/v1/members` chết ⇒
**handler bị block ở tầng ứng dụng**, cụ thể là đường đọc membership.

---

## 3. Bằng chứng: goroutine dump

Gửi `SIGQUIT` cho tiến trình (signal này không bị app bắt) để Go in toàn bộ
stack. Stack thủ phạm:

```
goroutine 66 [sync.RWMutex.RLock, 13 minutes]:
runtime.semacquire1(...)
github.com/hashicorp/memberlist.(*Memberlist).NumMembers(...)
github.com/Vyzz1/go-velox.git/internal/syncagent/cluster.(*Memberlist).refresh(...)
github.com/Vyzz1/go-velox.git/internal/syncagent/cluster.(*Memberlist).NotifyUpdate(...)
github.com/hashicorp/memberlist.(*Memberlist).aliveNode(...)        ← giữ nodeLock.Lock()
github.com/Vyzz1/go-velox.git/internal/syncagent/cluster.(*Memberlist).SetEngineHealthy(...)

goroutine 119 [sync.RWMutex.RLock, 13 minutes]:
github.com/Vyzz1/go-velox.git/internal/syncagent/cluster.(*Memberlist).Members(...)   ← request /v1/members, kẹt theo
```

Nhiều goroutine khác cũng kẹt ở `sync.RWMutex.RLock` trên **cùng một địa chỉ
khóa** — dấu hiệu kinh điển của một writer giữ khóa và không bao giờ nhả.

---

## 4. Nguyên nhân gốc

### 4.1 `sync.RWMutex` không reentrant

memberlist bảo vệ bảng thành viên bằng một `nodeLock` (`sync.RWMutex`). Đặc tính
quan trọng: **một goroutine đang giữ write-lock mà gọi `RLock()` (hoặc ngược lại)
trên cùng khóa sẽ tự deadlock** — Go RWMutex không cho phép tái nhập (reentrant).

### 4.2 memberlist gọi callback của ta KHI đang giữ write-lock

Trong `hashicorp/memberlist@v0.5.4`, hàm `aliveNode` (xử lý cập nhật trạng thái
một node) giữ write-lock trong **toàn bộ** thân hàm và gọi event-delegate ngay
bên trong:

```go
// memberlist/state.go (v0.5.4) — rút gọn
func (m *Memberlist) aliveNode(a *alive, notify chan struct{}, bootstrap bool) {
    m.nodeLock.Lock()
    defer m.nodeLock.Unlock()
    // ...
    if m.config.Events != nil {
        // ...
        m.config.Events.NotifyUpdate(&state.Node)   // ← gọi callback của TA, lock đang giữ
    }
}
```

`deadNode` và `suspectNode` cũng gọi `NotifyLeave`/`NotifyJoin` theo cùng kiểu
(callback chạy trong vùng `nodeLock.Lock()`).

### 4.3 Callback của ta lại gọi ngược vào memberlist

Code lỗi: cả ba callback gọi `refresh()`, mà `refresh()` gọi
`m.ml.NumMembers()` — hàm này cần `nodeLock.RLock()`:

```go
// CODE LỖI
func (m *Memberlist) NotifyUpdate(n *memberlist.Node) {
    m.log.Debug("member updated", ...)
    m.refresh()                                  // ← gọi ngược vào memberlist
}

func (m *Memberlist) refresh() {
    if m.ml != nil {
        membersGauge.Set(float64(m.ml.NumMembers()))   // ← NumMembers() => nodeLock.RLock()
    }
}
```

```go
// memberlist/memberlist.go
func (m *Memberlist) NumMembers() (alive int) {
    m.nodeLock.RLock()           // ← xin read-lock trên khóa mà goroutine này ĐANG giữ write-lock
    defer m.nodeLock.RUnlock()
    // ...
}
```

### 4.4 Chuỗi gây deadlock

```
SetEngineHealthy(true)              // health-check probe engine lần đầu THÀNH CÔNG
  → ml.UpdateNode()                 // re-broadcast meta mới (healthy: false → true)
    → aliveNode()                   // memberlist: nodeLock.Lock()  [GIỮ WRITE-LOCK]
      → NotifyUpdate()              // callback của ta, chạy trong vùng khóa
        → refresh()
          → ml.NumMembers()         // xin nodeLock.RLock()
                                    // ✗ cùng goroutine đang giữ write-lock
                                    // ✗ RWMutex không reentrant → KẸT VĨNH VIỄN
```

Goroutine này không bao giờ nhả write-lock ⇒ **mọi** lời gọi `Members()` /
`Local()` / `NumMembers()` sau đó (gồm request `/v1/members`) đều kẹt khi xin
read-lock.

### 4.5 Vì sao treo sau ~2s, không treo lúc khởi động

- Lúc khởi động `healthy = false`, meta của node chưa đổi ⇒ `NotifyUpdate` không
  bị kích hoạt ⇒ chưa dính bẫy ⇒ log `sync-agent ready` bình thường.
- Sau ~2s, health-check probe engine lần đầu **thành công** → đổi `healthy`
  `false → true` → gọi `UpdateNode()` → đúng chuỗi 4.4 → kẹt từ thời điểm đó.
- `/healthz` không đụng membership nên không bao giờ kẹt — đó là lý do nó vẫn
  sống và dễ gây hiểu nhầm là "service vẫn ổn".

---

## 5. Cách khắc phục

**Nguyên tắc**: trong các callback `Notify*`, **không bao giờ gọi ngược lại
memberlist** (`NumMembers`, `Members`, …), vì chúng chạy trong vùng memberlist
đang giữ `nodeLock`.

Việc cập nhật metric `velox_cluster_members` được tách ra một **goroutine nền
riêng** (`gaugeLoop`) gọi `NumMembers()` **ngoài** vùng khóa, định kỳ mỗi giây.

### Trước

```go
func (m *Memberlist) NotifyJoin(n *memberlist.Node)   { m.log.Info(...);  m.refresh() }
func (m *Memberlist) NotifyLeave(n *memberlist.Node)  { m.log.Info(...);  m.refresh() }
func (m *Memberlist) NotifyUpdate(n *memberlist.Node) { m.log.Debug(...); m.refresh() }

func (m *Memberlist) refresh() {
    if m.ml != nil {
        membersGauge.Set(float64(m.ml.NumMembers()))   // gọi từ trong callback → deadlock
    }
}
```

### Sau

```go
// Các callback chạy khi memberlist đang giữ nodeLock, nên KHÔNG được gọi ngược
// vào memberlist (NumMembers, Members, ...): RWMutex không reentrant ⇒ self-deadlock.
// Ở đây chỉ log; gauge được làm tươi out-of-band bởi gaugeLoop.
func (m *Memberlist) NotifyJoin(n *memberlist.Node)   { m.log.Info("member joined", ...) }
func (m *Memberlist) NotifyLeave(n *memberlist.Node)  { m.log.Info("member left", ...) }
func (m *Memberlist) NotifyUpdate(n *memberlist.Node) { m.log.Debug("member updated", ...) }

// gaugeLoop gọi NumMembers từ goroutine riêng — ngoài vùng callback đang giữ
// nodeLock — nên không bao giờ tự khóa. Thoát khi Shutdown đóng m.stop.
func (m *Memberlist) gaugeLoop() {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    membersGauge.Set(float64(m.ml.NumMembers()))
    for {
        select {
        case <-m.stop:
            return
        case <-ticker.C:
            membersGauge.Set(float64(m.ml.NumMembers()))
        }
    }
}
```

Thay đổi kèm theo:
- Thêm field `stop chan struct{}` vào struct `Memberlist`.
- `New()`: khởi tạo `stop` và `go m.gaugeLoop()` (thay cho `m.refresh()`).
- `Shutdown()`: `close(m.stop)` trước khi `m.ml.Shutdown()` để dừng goroutine nền.

---

## 6. Kiểm chứng

```bash
gofmt -l internal/syncagent/cluster/memberlist.go   # (không output)
go vet ./internal/syncagent/...                      # sạch
go build ./...                                        # BUILD-OK

docker compose --profile stack up -d --build sync-agent-1 sync-agent-2 sync-agent-3
curl -s http://localhost:7072/v1/members              # trả NGAY, không treo
```

Kết quả `/v1/members` (trước đây treo, nay trả tức thì):

```json
{"local":"sync-agent-1","count":3,"members":[
  {"id":"sync-agent-1","role":"engine","engine_addr":"limiter-engine-1:9090","healthy":true,...},
  {"id":"sync-agent-2","role":"engine","engine_addr":"limiter-engine-2:9090","healthy":true,...},
  {"id":"sync-agent-3","role":"engine","engine_addr":"limiter-engine-3:9090","healthy":true,...}
]}
```

- `api-gateway` hết log `failed to fetch members`; hash ring dựng được.
- Traffic qua `POST /v1/check` cho ra `200` rồi `429` đúng như kỳ vọng.

---

## 7. Bài học & phòng ngừa

1. **Không gọi ngược vào thư viện từ trong callback của nó.** Với
   `hashicorp/memberlist`, mọi `EventDelegate` (`NotifyJoin/Leave/Update`) và
   phần lớn `Delegate` chạy trong khi memberlist giữ `nodeLock`. Callback phải
   "rẻ và không khóa": chỉ log, gửi vào channel, hoặc set biến cục bộ.
2. **`sync.RWMutex` của Go không reentrant.** Giữ write-lock rồi xin read-lock
   (hay ngược lại) trên cùng khóa, dù gián tiếp qua nhiều lớp hàm, đều deadlock.
3. **Tách công việc nền ra goroutine riêng.** Việc cần đọc trạng thái thư viện
   (đếm thành viên, snapshot…) nên chạy ngoài callback, theo ticker hoặc theo
   yêu cầu, không nhét vào đường sự kiện.
4. **`SIGQUIT` là công cụ chẩn đoán deadlock nhanh nhất.** Một goroutine dump chỉ
   ra ngay stack đang kẹt và loại khóa đang chờ; đừng đoán mò khi có thể dump.
5. **Cảnh giác với "chạy được rồi mới chết".** Deadlock phụ thuộc sự kiện (ở đây
   là lần probe health thành công đầu tiên) thường không lộ lúc khởi động —
   `/healthz` xanh không đồng nghĩa service khỏe.
