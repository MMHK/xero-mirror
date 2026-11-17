# Webhook Buffer 設計

> 本文件說明 webhook buffer 的設計與實作細節，對應 `doc/spec.md` §7.0。

**建立日期**: 2026-11-16 ｜ **狀態**: 🟡 設計中（待實作）

## 1. 背景與問題

Xero webhook 會在 entry 變更時發送 event。同一個 entry 短時間內可能觸發多個 event：

```
Invoice A 的生命週期：
  10:00  建立 → event (evt-001, inv-A)
  10:01  更新 → event (evt-002, inv-A)
  10:02  付款 → event (evt-003, inv-A)
```

如果每個 event 都立即呼叫 Xero API 抓取資料，會浪費 API 配額：

| 策略 | API 呼叫次數 | 說明 |
|------|-------------|------|
| 無 buffer，每個 event 立即處理 | 3 次 | 每次 event 都呼叫 `GET /Invoices/inv-A` |
| 有 buffer，收集後批量處理 | 1 次 | 收集 3 秒內的 events，一次 `GET /Invoices?IDs=inv-A` |

## 2. 設計目標

1. **收集多個 entry IDs**：同一個 entity 的多個 entry IDs 收集在一起
2. **自動去重**：同一個 entry ID 的多個 events 只保留一個
3. **批量抓取**：達到 batch size 或 timer 到期後，一次 API 呼叫抓取所有 entries
4. **優雅降級**：不支援批量 IDs 的端點（CreditNotes、Payments）走逐個處理或 cursor 增量

## 3. 架構

```
┌─────────────────────────────────────────────────────────────────────┐
│ Webhook Handler (pkg/handler)                                       │
│                                                                     │
│  POST /webhook ─► 驗簽 ─► 入列 webhook_events/sync_jobs ─► 回 200   │
│                                                                     │
│  Worker (background goroutine)                                      │
│    ┌─────────────────────────────────────────────────────────┐      │
│    │ WebhookBuffer                                           │      │
│    │                                                         │      │
│    │  pending: map[Entity]map[string]struct{}                │      │
│    │           Entity → set of xero_ids（自動去重）           │      │
│    │                                                         │      │
│    │  flushTimer: 收到第一個 event 後啟動                     │      │
│    │                                                         │      │
│    │  觸發 flush 條件：                                       │      │
│    │    1. Timer 到期（預設 3 秒）                            │      │
│    │    2. 單個 entity 達到 batchSize（預設 30）              │      │
│    │    3. Shutdown 信號                                      │      │
│    └─────────────────────────────────────────────────────────┘      │
│                          │                                          │
│                          ▼                                          │
│              flush() → 取出 pending → 清空 buffer                   │
│                          │                                          │
│                          ▼                                          │
│    ┌─────────────────────────────────────────────────────────┐      │
│    │ 依 entity 分派                                          │      │
│    │                                                         │      │
│    │  Invoices/Contacts (支援 IDs):                          │      │
│    │    → sync.RunBatch(ctx, tenantID, entity, ids)          │      │
│    │    → GET /Invoices?IDs=uuid1,uuid2,...                  │      │
│    │                                                         │      │
│    │  CreditNotes/Payments (不支援 IDs):                     │      │
│    │    → 逐個呼叫 sync.Run(ctx, tenantID, entity, Window{}) │      │
│    │    → 或改走 cursor 增量（不處理 webhook）                │      │
│    └─────────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────────┘
```

## 4. 資料結構

### 4.1 WebhookBuffer

```go
// pkg/handler/buffer.go

type WebhookBuffer struct {
    mu          sync.Mutex
    pending     map[Entity]map[string]struct{}  // entity → set of xero_ids
    flushTimer  *time.Timer
    batchSize   int           // 單個 entity 的 batch size，預設 30
    flushDelay  time.Duration // flush delay，預設 3 秒
    runner      *sync.Runner
    tenantID    string
}

// NewWebhookBuffer 建立 webhook buffer。
func NewWebhookBuffer(runner *sync.Runner, tenantID string, batchSize int, flushDelay time.Duration) *WebhookBuffer

// Add 加入一個 entry id 到 buffer；同一個 id 多次加入只算一次。
func (b *WebhookBuffer) Add(entity Entity, xeroID string)

// Flush 立即 flush buffer；可由外部呼叫或 timer 觸發。
func (b *WebhookBuffer) Flush()

// Shutdown 優雅關閉：停止 timer，flush 剩餘 buffer。
func (b *WebhookBuffer) Shutdown(ctx context.Context)
```

### 4.2 sync.RunBatch

```go
// pkg/sync/sync.go

// RunBatch 批量抓取多個 entry IDs。
// 只支援有 IDs 參數的端點（Invoices、Contacts）。
// 不支援的端點回錯誤，呼叫端需改用逐個處理。
func (r *Runner) RunBatch(ctx context.Context, tenantID string, entity Entity, ids []string) error
```

## 5. 各端點策略

| 端點 | 支援 `IDs` | 策略 | 說明 |
|------|-----------|------|------|
| `/Invoices` | ✅ | Buffer + Batch Get | `GET /Invoices?IDs=uuid1,uuid2,...` |
| `/Contacts` | ✅ | Buffer + Batch Get | `GET /Contacts?IDs=uuid1,uuid2,...` |
| `/CreditNotes` | ❌ | 逐個抓取 | `GET /CreditNotes/{id}`，或改走 cursor 增量 |
| `/Payments` | ❌ | 逐個抓取 | `GET /Payments/{id}`，或改走 cursor 增量 |
| `/Accounts` | ❌ | 全量 + 冪等 upsert | 不分頁、不支援 IDs，webhook 觸發時全量重跑 |

### 5.1 不支援批量 IDs 的端點

對於 `/CreditNotes` 和 `/Payments`，有兩種選擇：

**選項 A：逐個抓取**
```go
for _, id := range ids {
    if err := r.Run(ctx, tenantID, entity, Window{}); err != nil {
        slog.Warn("逐個同步失敗", "entity", entity, "id", id, "error", err)
    }
}
```

**選項 B：改走 cursor 增量**
- Webhook 只記錄到 `sync_jobs`，不立即處理
- 由 cron 定期觸發 cursor 增量 sync
- 適合變更頻率低的端點

**建議**：對於 CreditNotes 和 Payments，預設走 cursor 增量（因為它們的變更頻率通常較低）。

## 6. 配置參數

| 環境變數 | 說明 | 預設 |
|---------|------|------|
| `WEBHOOK_BUFFER_SIZE` | 單個 entity 的 batch size | 30 |
| `WEBHOOK_BUFFER_DELAY` | flush delay（毫秒） | 3000 |

## 7. 邊界情況

### 7.1 Buffer 期間服務重啟

**問題**：buffer 中的 events 存在記憶體，服務重啟會遺失。

**解法**：
1. **接受遺失**：webhook events 已寫入 `webhook_events` 和 `sync_jobs`，重啟後可由 worker 重新處理
2. **持久化 buffer**：將 buffer 寫入 DB，重啟後恢復（複雜度高，不建議）

**建議**：採用解法 1，接受 buffer 遺失。`sync_jobs` 已經持久化，重啟後 worker 可以繼續處理。

### 7.2 Batch size 超過 URL 長度限制

**問題**：Xero API 的 URL 長度限制約 40 個 IDs。

**解法**：
- `batchSize` 預設 30，留 buffer 給其他 query parameters
- 超過 `batchSize` 時立即 flush，避免累積過多
- `RunBatch` 內部可再分批（每批最多 30 個 IDs）

### 7.3 同一個 entry 的多次變更

**問題**：Invoice A 在 buffer 期間被更新 3 次，只抓取最後一次狀態。

**解法**：這是預期行為。Xero API 回傳的是最新狀態，不需要抓取中間狀態。

## 8. 測試案例

1. **去重**：同一個 entry ID 加入 3 次，buffer 只保留 1 個
2. **Timer flush**：收到 event 後 3 秒內沒有新 event，自動 flush
3. **Batch size flush**：累積 30 個 entry IDs 後立即 flush
4. **Shutdown flush**：收到 shutdown 信號，flush 剩餘 buffer
5. **不支援批量 IDs 的端點**：CreditNotes 走逐個處理或 cursor 增量

## 9. 待辦事項

- [ ] 實作 `pkg/handler/buffer.go`
- [ ] 實作 `sync.RunBatch` 方法
- [ ] 加入 `WEBHOOK_BUFFER_SIZE` 和 `WEBHOOK_BUFFER_DELAY` 環境變數
- [ ] 更新 `pkg/config` 讀取新環境變數
- [ ] 加入測試案例
