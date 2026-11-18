# Sync Job Queue 設計

> 本文件說明 sync job queue 的設計與實作細節，對應 `doc/spec.md` §7.0。

**建立日期**: 2026-11-18 ｜ **狀態**: ✅ 已實作

## 1. 背景

原 webhook 事件處理使用兩個 in-memory buffer（`WebhookBuffer` 與 `SingleGetBuffer`），
webhook 事件到達後同時寫入 `sync_jobs` 並分派到 buffer。但 `sync_jobs` 只寫不讀，
buffer 與 `sync_jobs` 的去重邏輯重疊，且 buffer flush 失敗時沒有補抓機制。

**重構目標**：用一個獨立的 `SyncJobQueue` 取代兩個 buffer，直接從 `sync_jobs` 拉取 pending records 並處理。

## 2. 設計目標

1. **單一資料來源**：`sync_jobs` 是唯一的待處理佇列，不再有 in-memory buffer
2. **去重由 DB 保證**：`(tenant_id, entity, xero_id)` UNIQUE KEY 確保同一資源只有一筆 job
3. **批量優化**：支援 `?IDs=` 的實體（Contacts、Invoices）按 `(tenant, entity)` 分組批量拉取
4. **忙輪詢 + 空閒等待**：有 pending jobs 時持續處理不等待；佇列清空後才進入冷卻期
5. **失敗獨立追蹤**：失敗的 job 移至 `failed_sync_jobs` 表，`sync_jobs` 永遠只包含 pending 的 job

## 3. 架構

```
┌─────────────────────────────────────────────────────────────────────┐
│ Webhook Handler (pkg/handler)                                       │
│                                                                     │
│  POST /webhook ─► 驗簽 ─► 入列 webhook_events/sync_jobs ─► 回 200   │
│                                                                     │
│  SyncJobQueue（單一背景 goroutine）                                   │
│    ┌─────────────────────────────────────────────────────────┐      │
│    │                                                         │      │
│    │  loop:                                                  │      │
│    │    1. GetPendingSyncJobs(batchSize)                     │      │
│    │    2. 有數據 → groupJobs() 按 (tenant, entity) 分組     │      │
│    │    3. 對每一組：                                         │      │
│    │       ├─ SupportsBatchIDs → RunBatch(ids)              │      │
    │    │       │    → 成功刪除 / 失敗移至 failed_sync_jobs        │      │
    │    │       └─ 不支援 → 逐筆 RunSingle(id)                   │      │
    │    │            → 成功刪除 / 失敗移至 failed_sync_jobs        │      │
│    │    4. 繼續下一輪（不等待）                                │      │
│    │                                                         │      │
│    │    無數據 → 等待 pollInterval 後再試                     │      │
│    └─────────────────────────────────────────────────────────┘      │
└─────────────────────────────────────────────────────────────────────┘
```

## 4. 各端點策略

| 端點 | 支援 `IDs` | 處理方式 | 說明 |
|------|-----------|---------|------|
| `/Invoices` | ✅ | `RunBatch` 批量 | `GET /Invoices?IDs=uuid1,uuid2,...` |
| `/Contacts` | ✅ | `RunBatch` 批量 | `GET /Contacts?IDs=uuid1,uuid2,...` |
| `/CreditNotes` | ❌ | `RunSingle` 逐筆 | `GET /CreditNotes/{id}` |
| `/Payments` | ❌ | `RunSingle` 逐筆 | `GET /Payments/{id}` |
| `/Accounts` | ❌ | `RunSingle` 逐筆 | `GET /Accounts/{id}` |

> 與舊 buffer 的差異：舊架構中不支援批量 IDs 的實體（CreditNotes、Payments、Accounts）
> 不做逐個抓取，只交 cron 增量。新架構所有實體都會被 SyncJobQueue 處理。

## 5. 資料結構

### 5.1 SyncJobQueue

```go
// pkg/handler/sync_queue.go

type SyncJobQueue struct {
    runner       *pkgsync.Runner
    store        *store.Store
    batchSize    int           // 每次 poll 上限
    pollInterval time.Duration // 佇列為空時的等待間隔
    baseCtx      context.Context
    done         chan struct{}
    closeOnce    sync.Once
    mu           sync.Mutex
    closed       bool
}
```

### 5.2 sync_jobs 表

```sql
CREATE TABLE sync_jobs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    xero_id varchar(64) NOT NULL,
    reason varchar(64) NOT NULL,
    status varchar(32) NOT NULL DEFAULT 'pending',  -- 永遠為 'pending'
    last_error TEXT NULL,                            -- 未使用（失敗 job 移至 failed_sync_jobs）
    created_at datetime(6) NOT NULL,
    finished_at datetime(6) NULL,                    -- 未使用
    PRIMARY KEY (id),
    UNIQUE KEY uniq_sync_jobs_resource (tenant_id, entity, xero_id),
    INDEX idx_sync_jobs_status (status, created_at)
);
```

**設計原則**：
- `sync_jobs` 永遠只包含 pending 的 job
- 成功的 job：直接刪除
- 失敗的 job：刪除後插入 `failed_sync_jobs`
- 新的 webhook 事件可以正常 INSERT，不會被舊記錄阻擋

### 5.3 failed_sync_jobs 表

```sql
CREATE TABLE failed_sync_jobs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    xero_id varchar(64) NOT NULL,
    reason varchar(64) NOT NULL,
    last_error text,
    failed_at datetime(6) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_failed_sync_jobs_tenant_entity (tenant_id, entity),
    INDEX idx_failed_sync_jobs_failed_at (failed_at)
);
```

**用途**：
- 儲存失敗的 job，供除錯與未來重試
- 不設 UNIQUE KEY，允許同一資源多次失敗
- 預留作人工除錯與後續重試用途（目前無 web 入口可重新插入 `sync_jobs`）

## 6. 配置參數

| 環境變數 | 說明 | 預設 |
|---------|------|------|
| `SYNC_JOB_BATCH_SIZE` | 每次 poll 拉取的 sync_jobs 上限 | 100 |
| `SYNC_JOB_POLL_INTERVAL` | 佇列為空時的等待間隔（毫秒） | 5000 |

## 7. 邊界情況

### 7.1 服務重啟

**問題**：處理中的 job 可能因為服務重啟而停留在 `pending` 狀態。

**解法**：重啟後 SyncJobQueue 會繼續拉取 pending jobs 處理，不會遺失。

### 7.2 失敗重試

**問題**：job 失敗後移至 `failed_sync_jobs`，不會自動重試。

**解法**：
- cron 增量同步會靠 cursor 水位補抓遺漏的資料
- 失敗的 job 可在 `failed_sync_jobs` 查詢錯誤原因
- 未來可擴充：人工從 `failed_sync_jobs` 重新插入 `sync_jobs`

### 7.3 同一個 entry 的多次變更

**問題**：Invoice A 短時間內被更新 3 次。

**解法**：`sync_jobs` 的 UNIQUE KEY `(tenant_id, entity, xero_id)` 確保只有一筆 job，
新事件會刷新 `status = 'pending'`（`ON DUPLICATE KEY UPDATE`），處理時抓取最新狀態。

## 8. 與舊架構的差異

| 面向 | 舊架構（WebhookBuffer + SingleGetBuffer） | 新架構（SyncJobQueue） |
|------|------------------------------------------|----------------------|
| 資料來源 | in-memory channel | DB `sync_jobs` 表 |
| 去重 | 各自實作（map + channel） | DB UNIQUE KEY |
| 不支援批量的實體 | 只記 log，交 cron | `RunSingle` 逐筆處理 |
| 失敗記錄 | 無 | 獨立 `failed_sync_jobs` 表 |
| 服務重啟 | buffer 中的事件遺失 | pending jobs 繼續處理 |
| 即時性 | buffer timer 延遲（3-5 秒） | poll 間隔（預設 5 秒，可調整） |
| 去重衝突 | 無（in-memory） | 成功/失敗後刪除，不擋住新 INSERT |
