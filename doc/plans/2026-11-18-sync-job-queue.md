# Sync Job Queue 重構

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟢 完成
**優先級**: Medium

## 背景與目標

### 現狀分析

目前 webhook 事件處理有三條路徑：

1. **`enqueue()`**：寫入 `webhook_events` + `sync_jobs`（以 `(tenant_id, entity, xero_id)` 去重）
2. **`dispatchToBuffer()`**：即時分派到兩個 buffer
   - `WebhookBuffer`：聚合 IDs → `runner.RunBatch()`（支援 `?IDs=` 的實體：Contacts、Invoices）
   - `SingleGetBuffer`：延遲後 → `runner.RunSingle()`（不支援批量的實體：Accounts、CreditNotes、Payments）
3. **Cron 增量補抓**：`runCronSync` → `RunAllIncremental()`，靠 `sync_state` cursor，不依賴 `sync_jobs`

### 問題

- `sync_jobs` 只寫不讀：`GetPendingSyncJobs` / `MarkSyncJobDone` 沒有被任何 production code 呼叫
- 兩個 buffer 的去重邏輯與 `sync_jobs` 重疊
- Buffer flush 失敗時，事件已在 `sync_jobs` 但沒有機制重試

### 目標

用一個獨立的 **SyncJobQueue** 取代兩個 buffer，直接從 `sync_jobs` 拉取 pending records 並處理：

- 移除 `WebhookBuffer` 和 `SingleGetBuffer`
- 建立 `SyncJobQueue`：背景 goroutine 定期 poll `sync_jobs`
- 依 entity 是否支援 `?IDs=` 分組批量處理
- 成功的 job 刪除，失敗的 job 移至 `failed_sync_jobs` 表（類似 Laravel 設計）

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 建立 `pkg/handler/sync_queue.go`：SyncJobQueue 實作 | ✅ 完成 | general | 背景 poll + 分組 + 呼叫 runner |
| 2 | 新增 migration `0005_add_sync_jobs_last_error` | ✅ 完成 | — | sync_jobs 加 last_error TEXT 欄位 |
| 3 | 新增 migration `0006_create_failed_sync_jobs` | ✅ 完成 | — | 建立 failed_sync_jobs 表 |
| 4 | 更新 `pkg/store/queries.go`：DeleteSyncJob + InsertFailedSyncJob | ✅ 完成 | — | 成功刪除，失敗移至 failed_sync_jobs |
| 5 | 修改 `pkg/handler/webhook.go`：移除 dispatchToBuffer | ✅ 完成 | general | webhook handler 只做 enqueue |
| 6 | 修改 `pkg/handler/mux.go`：移除 buffer 相關依賴 | ✅ 完成 | general | NewMux 不再接收 buffer |
| 7 | 修改 `commands.go`：移除 buffer 建立與關閉邏輯 | ✅ 完成 | general | serve 啟動 SyncJobQueue 取代 |
| 8 | 修改 `pkg/config/config.go`：替換 buffer 設定為 queue 設定 | ✅ 完成 | general | SyncJobPollInterval、SyncJobBatchSize |
| 9 | 刪除 `pkg/handler/buffer.go` 和 `pkg/handler/single_buffer.go` | ✅ 完成 | — | 連同測試檔 |
| 10 | 更新 `doc/spec.md` 與 `doc/modules/sync-queue.md` | ✅ 完成 | — | 反映新架構 |

## 設計細節

### SyncJobQueue 行為

```
背景 goroutine
    │
    ├─→ 從 sync_jobs 拉取 pending records（上限 batchSize）
    │
    ├─→ 有數據時：
    │   ├─→ 按 (tenant_id, entity) 分組
    │   ├─→ 對每一組：
    │   │   ├─→ 支援 batch IDs（Contacts、Invoices）：
    │   │   │   └─→ 收集 xero_ids → runner.RunBatch() → 成功刪除 / 失敗移至 failed_sync_jobs
    │   │   │
    │   │   └─→ 不支援 batch IDs（Accounts、CreditNotes、Payments）：
    │   │       └─→ 逐筆 runner.RunSingle() → 成功刪除 / 失敗移至 failed_sync_jobs
    │   └─→ 繼續下一輪 pull（不等待）
    │
    └─→ 無數據時：
        └─→ 等待 pollInterval 後再試
```

**設計原則**：忙輪詢 + 空閒等待。有工作時持續處理不等待，佇列清空後才進入冷卻期。

### Entity 支援矩陣

| Entity | supportsBatchIDs | singlePath | 處理方式 |
|--------|------------------|------------|----------|
| Contacts | ✅ | — | `RunBatch` 批量 |
| Invoices | ✅ | — | `RunBatch` 批量 |
| Accounts | ❌ | `/Accounts/%s` | `RunSingle` 逐筆 |
| CreditNotes | ❌ | `/CreditNotes/%s` | `RunSingle` 逐筆 |
| Payments | ❌ | `/Payments/%s` | `RunSingle` 逐筆 |

### 設定變更

| 移除 | 新增 | 說明 |
|------|------|------|
| `WEBHOOK_BUFFER_SIZE` | `SYNC_JOB_BATCH_SIZE` | 每次 poll 拉取的 sync_jobs 上限（預設 100） |
| `WEBHOOK_BUFFER_DELAY` | `SYNC_JOB_POLL_INTERVAL` | 佇列為空時的等待間隔（預設 5000ms） |
| `SINGLE_BUFFER_DELAY` | — | 不再需要 |

**Poll 行為**：有 pending jobs 時持續處理不等待；佇列清空後才等待 `SYNC_JOB_POLL_INTERVAL` 再試。

### 失敗處理

- `RunBatch` 失敗：整組 job 從 `sync_jobs` 刪除，插入 `failed_sync_jobs`，記錄錯誤訊息
- `RunSingle` 失敗：單筆 job 從 `sync_jobs` 刪除，插入 `failed_sync_jobs`，記錄錯誤訊息
- `sync_jobs` 永遠只包含 pending 的 job，新的 webhook 事件可以正常 INSERT
- 失敗的 job 不自動重試，可由 cron 增量補抓或從 `failed_sync_jobs` 手動重試

### Shutdown 行為

- 收到關閉訊號後停止 poll
- 等待當前處理中的 job 完成（或逾時）
- 不保證剩餘 pending job 處理完畢（下次啟動會繼續）

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ✅ 已批准 | 使用者批准並實作完成 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 備註

- `sync_jobs` 的 `reason` 欄位記錄事件來源（`webhook:CREATE`、`webhook:UPDATE` 等）
- `sync_jobs` 永遠只包含 pending 的 job，成功刪除，失敗移至 `failed_sync_jobs`
- `failed_sync_jobs` 儲存失敗的 job，供除錯與未來重試（migration 0006 新增）
- 未來可擴充：admin endpoint 從 `failed_sync_jobs` 重新插入 `sync_jobs`
