# Webhook Buffer 設計

> 本文件說明 webhook buffer 的設計與實作細節，對應 `doc/spec.md` §7.0。

**建立日期**: 2026-11-16 ｜ **狀態**: ✅ 已實作（任務 9c）

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
4. **優雅降級**：不支援批量 IDs 的端點（CreditNotes、Payments）只記 log，交 cron cursor 增量（不做逐個抓取）

## 3. 架構

```
┌─────────────────────────────────────────────────────────────────────┐
│ Webhook Handler (pkg/handler)                                       │
│                                                                     │
│  POST /webhook ─► 驗簽 ─► 入列 webhook_events/sync_jobs ─► 回 200   │
│                                                                     │
│  Worker（單一背景 goroutine event loop）                                │
│    ┌─────────────────────────────────────────────────────────┐      │
│    │ WebhookBuffer                                           │      │
│    │                                                         │      │
│    │  events chan bufferItem（容量 1024，Add 非阻塞）         │      │
│    │                                                         │      │
│    │  pending: map[tenantID]map[Entity]map[xeroID]struct{}   │      │
│    │           (tenant, entity) → xero_id 集合（自動去重）     │      │
│    │                                                         │      │
│    │  flushTimer: 收到第一個 event 後啟動                     │      │
│    │                                                         │      │
│    │  觸發 flush 條件：                                       │      │
│    │    1. Timer 到期（預設 3 秒）                            │      │
│    │    2. 單一 (tenant, entity) 群組達到 batchSize（預設 30） │      │
│    │    3. Shutdown：flush 全部剩餘 pending                   │      │
│    └─────────────────────────────────────────────────────────┘      │
│                          │                                          │
│                          ▼                                          │
│              flush() → 取出 pending → 清空 buffer                   │
│                          │                                          │
│                          ▼                                          │
│    ┌─────────────────────────────────────────────────────────┐      │
│    │ 依 (tenant, entity) 群組分派（pkgsync.SupportsBatchIDs） │      │
│    │                                                         │      │
│    │  Invoices/Contacts（支援 IDs）:                          │      │
│    │    → sync.RunBatch(ctx, tenantID, entity, ids)          │      │
│    │    → GET /Invoices?IDs=uuid1,uuid2,...                  │      │
│    │    → 單一群組失敗只 slog.Warn，不中斷其他群組            │      │
│    │                                                         │      │
│    │  CreditNotes/Payments/Accounts（不支援 IDs）:           │      │
│    │    → 不呼叫 API，只 slog.Info 記錄                       │      │
│    │    → sync_jobs 已持久化，交由 cron cursor 增量處理       │      │
│    └─────────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────────┘
```

> 實作重點：所有 pending 狀態只在唯一的 event loop goroutine 內存取，故不需額外 mutex 保護；
> `Add` 僅把事件塞進帶緩衝的 channel，channel 滿時記 `slog.Warn` 後丟棄，永不阻塞 HTTP handler。
> flush 在 event loop 內同步執行，不另起未受控 goroutine。

## 4. 資料結構

### 4.1 WebhookBuffer

```go
// pkg/handler/buffer.go

// bufferItem 是單一 webhook 事件在 buffer 內的最小表示。
type bufferItem struct {
	tenantID string
	entity   pkgsync.Entity
	xeroID   string
}

type WebhookBuffer struct {
	runner     *pkgsync.Runner
	batchSize  int
	flushDelay time.Duration

	baseCtx context.Context // 建構期設定後唯讀，loop 所有 flush 皆用它

	events    chan bufferItem // 容量 1024
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool

	pending map[string]map[pkgsync.Entity]map[string]struct{}
	timer   *time.Timer
}

// NewWebhookBuffer 建立並啟動 buffer（內部啟動單一 event loop goroutine）。
func NewWebhookBuffer(ctx context.Context, runner *pkgsync.Runner, batchSize int, flushDelay time.Duration) *WebhookBuffer

// Add 加入一個 entry id；TrimSpace 後空值丟棄，channel 滿時記警告後丟棄，永不阻塞。
func (b *WebhookBuffer) Add(tenantID string, entity pkgsync.Entity, xeroID string)

// Shutdown 關閉 channel，等待 event loop flush 完剩餘 pending；ctx 逾時回 ctx.Err()，可重複呼叫。
func (b *WebhookBuffer) Shutdown(ctx context.Context) error
```

> 不另設 exported `Flush()`：flush 一律由 event loop 在 timer 到期、達 batch size 或
> Shutdown 時觸發，避免外部與 loop 併發碰 pending。

### 4.2 sync.RunBatch 與 SupportsBatchIDs

```go
// pkg/sync/sync.go

// SupportsBatchIDs 回傳實體端點是否支援 ?IDs= 批量查詢；未知實體回 false。
// buffer 以此決定呼叫 RunBatch 還是改交 cron，不直接讀 entitySpecs（未匯出）。
func SupportsBatchIDs(e Entity) bool

// RunBatch 批量抓取多個 entry IDs。
// 只支援有 IDs 參數的端點（Invoices、Contacts）；不支援的端點回錯誤。
func (r *Runner) RunBatch(ctx context.Context, tenantID string, entity Entity, ids []string) error
```

## 5. 各端點策略

| 端點 | 支援 `IDs` | 策略 | 說明 |
|------|-----------|------|------|
| `/Invoices` | ✅ | Buffer + Batch Get | `GET /Invoices?IDs=uuid1,uuid2,...` |
| `/Contacts` | ✅ | Buffer + Batch Get | `GET /Contacts?IDs=uuid1,uuid2,...` |
| `/CreditNotes` | ❌ | 不呼叫 API，交 cron | buffer 只 `slog.Info` 記錄，事件已在 `sync_jobs`，由 cursor 增量補抓 |
| `/Payments` | ❌ | 不呼叫 API，交 cron | 同上；不做逐個 `GET /Payments/{id}` fallback |
| `/Accounts` | ❌ | 不呼叫 API，交 cron | 不分頁、不支援 IDs，全量重跑由 cron 負責 |

### 5.1 不支援批量 IDs 的端點

實作採用**選項 B（改走 cursor 增量）**，不做選項 A 的逐個抓取：

- flush 時以 `pkgsync.SupportsBatchIDs(entity)` 判斷；回 false 的實體只記
  `slog.Info("實體不支援批量同步，交由 cron 增量處理", ...)`，不發任何 Xero 請求。
- webhook 事件在 handler 端已持久化到 `webhook_events` 與 `sync_jobs`，不會因 buffer 不處理而遺失。
- 優點：避免在 buffer flush 路徑裡對高頻事件逐個打 API、耗費配額；這類實體變更頻率通常較低，cron 增量即可收斂。

> 備註：`RunBatch` 對不支援的實體仍會明確回錯（`端點不支援批量 IDs`），作為最後防線；
> 正常流程由 buffer 端的 `SupportsBatchIDs` 先擋下，不會真的打到這個錯誤。

## 6. 配置參數

| 環境變數 | 說明 | 預設 |
|---------|------|------|
| `WEBHOOK_BUFFER_SIZE` | 單個 entity 的 batch size | 30 |
| `WEBHOOK_BUFFER_DELAY` | flush delay（毫秒） | 3000 |

## 7. 邊界情況

### 7.1 Buffer 期間服務重啟

**問題**：buffer 中的 events 存在記憶體，服務重啟會遺失。

**解法**：
1. **接受遺失**：webhook events 已寫入 `webhook_events` 和 `sync_jobs` 作待辦／審計紀錄；目前沒有 `sync_jobs` consumer，重啟後由 cron 增量收斂
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

`pkg/handler/buffer_test.go` 以 `httptest.Server` 回 `{"Invoices":[]}`、
`xero.NewClientWithTokenSource` 與 `NewRunner(client, nil)` 組合，用 channel +
`select`/`time.After` 等待，避免固定 sleep 造成 flaky：

1. **Batch size flush**：`batchSize=3`、`flushDelay=1h`，Add 3 個 id → 收到 1 次請求且 `IDs` 含 3 個
2. **Timer flush**：`batchSize=100`、`flushDelay=100ms`，Add 1 個 id → 100ms 級別內收到請求
3. **Shutdown flush**：`batchSize=100`、`flushDelay=1h`，Add 2 個 id 後 `Shutdown` → 收到請求，且重複 Shutdown 不 panic

去重邏輯由 `Runner.RunBatch` 既有的 `TestRunBatchDedupKeepsOrder` 覆蓋；buffer 層只負責收集與觸發。

## 9. 待辦事項

- [x] 實作 `pkg/handler/buffer.go`
- [x] 實作 `sync.RunBatch` 方法（任務 9b，已先完成）
- [x] 加入 `WEBHOOK_BUFFER_SIZE` 和 `WEBHOOK_BUFFER_DELAY` 環境變數
- [x] 更新 `pkg/config` 讀取新環境變數（含 `DefaultWebhookBufferSize`／`DefaultWebhookBufferDelayMs` 常數）
- [x] 匯出 `sync.SupportsBatchIDs` 能力查詢
- [x] 加入測試案例（`buffer_test.go` 3 案，含 `-race` 驗證）
- [x] `serve` 接線：建構 `WebhookBuffer` 並傳入 `NewWebhookHandler`，於 graceful shutdown 呼叫 `Shutdown`（任務 9e 已完成）
