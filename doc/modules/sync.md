# 同步核心（pkg/sync）

> 本文件說明 `pkg/sync` 的實作契約，對應 `doc/spec.md` §3（資料流）與 §9（指令／cron 語意）。端點能力以 spec §5 為準，本文件不重抄能力矩陣。

**建立日期**: 2026-11-17 ｜ **狀態**: ✅ 已實作（任務 8、9b、13、cursor chunk save 優化）

## 1. 職責與邊界

`pkg/sync` 是唯一的同步核心：cursor 增量、月度回補、webhook 批次三種觸發都走同一條 fetch → upsert 路徑（`AGENTS.md` §1.3）。

- 不碰 SQL：所有讀寫經 `pkg/store`，SQL 只存在 `pkg/store`。
- 不建 client：`*xero.Client` 與 `*store.Store` 由外部注入（`NewRunner`）。
- 不決定流程以外的政策：tenant 清單來自 `store.ListTenants`，實體順序來自 `Entities()`。
- handler 與 `commands.go` 只做接線，不得另寫抓取或寫入邏輯。

## 2. 型別與對外 API

```go
type Entity string   // 值同時寫入 sync_state.entity
type Window struct { // Cursor 與 MonthStart/MonthEnd 互斥；月度兩端必須成對
    Cursor     *time.Time
    MonthStart *time.Time
    MonthEnd   *time.Time
}
type Runner struct { client *xero.Client; store *store.Store }
```

| API | 用途 | 是否推進 cursor |
|-----|------|-----------------|
| `Run(ctx, tenantID, entity, Window)` | 單一實體同步；Window 決定查詢範圍 | 僅 `Cursor` 模式 |
| `RunBatch(ctx, tenantID, entity, ids)` | webhook 批量 IDs（去重保序、每批 30） | ❌ |
| `RunIncremental(ctx, tenantID, entity)` | 讀 `sync_state.cursor_time` 後呼叫 `Run` | 依 `Run` |
| `RunTenant(ctx, tenantID)` | 單一 tenant × `Entities()` 增量 | 依 `RunIncremental` |
| `RunAllIncremental(ctx)` | 全部 active tenant 增量（`sync`／cron） | 依 `RunTenant` |
| `RunAllMonth(ctx, start, end)` | 全部 active tenant 指定月度視窗 | ❌ |
| `RunEntityIncremental(ctx, entity)` | 全部 active tenant × 單一實體增量（`sync --entity`） | 依 `RunIncremental` |
| `RunEntityMonth(ctx, start, end, entity)` | 全部 active tenant × 單一實體月度視窗（`sync --month --entity`） | ❌ |
| `ParseEntity(raw)` | 大小寫不敏感解析 entity 名稱（含單數別名） | — |
| `Entities()` | 固定順序：Invoices、CreditNotes、Payments、Contacts、Accounts | — |
| `SupportsBatchIDs(e)` | 端點是否支援 `?IDs=`（供 webhook buffer 判斷） | — |

`RunIncremental` 讀不到 `sync_state`（`sql.ErrNoRows`）時視為首跑，`Cursor = nil` 走全量；其他讀取錯誤直接回錯，不會誤判成首跑。

## 3. 資料流

```
Runner.Run
  ├─ 驗證：未知實體／視窗不合法 → 直接回錯
  ├─ buildRequest：依 entitySpecs 決定 If-Modified-Since、where 與 order
  ├─ xero.Client.GetPages（分頁端點）或 Get（Accounts）
  ├─ decodePage → pageApplier.handle：逐頁累積，達 writeChunkRows（500）即 flush
  └─ total > 0 → 最後一次 flush 帶 cursor（-1 秒）：五表 upsert + sync_state（同交易）
```

- **分頁排序**：分頁端點（Contacts、Invoices、CreditNotes、Payments）請求時明確帶 `order=UpdatedDateUTC`，確保資料按時間排序，chunk save 安全。Accounts 不分頁，不需要排序。
- **分塊寫入**：`writeChunkRows = 500`，每頁累積後達門檻即 `flush(cursor, false)`（中間 flush），cursor 為 `maxAt - 1 秒`；以 `ApplyBatch` 單一交易寫入並清空待寫 slice；flush 失敗立即回錯且不清空（該批回滾）。
- **中間 flush 的 cursor 更新**：中間 flush 帶 cursor（`maxAt - 1 秒`），`ApplyBatch` 只更新 `cursor_time`，不更新 `status` 和 `last_run_at`（避免提前把 status 設為 idle）。
- **最終 flush**：`total > 0` 時執行最終 flush（`isFinal=true`），帶 cursor（`maxAt - 1 秒`），`ApplyBatch` 更新 `cursor_time`、`last_run_at`、`status = 'idle'`。
- **-1 秒策略**：cursor 儲存時減 1 秒（`cursor.Add(-time.Second)`），下次增量從 `cursor - 1s` 開始抓取。可能會重複抓取部分資料，但冪等 upsert 會處理。這樣做是為了避免因為時間精度差異（HTTP header 精度到秒，Xero `UpdatedDateUTC` 精度到毫秒）而漏資料。
- **空資料處理**：端點回空陣列（`total == 0`）時不呼叫 `ApplyBatch`，但會呼叫 `UpsertSyncStateWithoutTx` 更新 `status = 'idle'` 和 `last_run_at`，避免 status 永遠停留在 `'running'`。
- 中途失敗時，先前已 flush 的 chunk 留在 DB（短期部分更新可見），cursor 已更新到最後一個成功的 chunk；`ON DUPLICATE KEY UPDATE` 冪等，重跑不會產生重複列。
- `RunBatch` 先 `dedupIDs`（丟空字串、保首次出現順序），再以 `maxBatchIDs = 30` 分批；每批同樣分塊 flush，結束時再 flush 一次但不帶 cursor（不推進水位）。

## 4. cursor 規則

| 觸發 | Window | `trackCursor` | 水位行為 |
|------|--------|---------------|----------|
| `sync`（無 `--month`）／cron | `Cursor`（`nil` = 首跑全量） | true | `Batch.Cursor` = 本批最大 `xero_updated_at` - 1 秒 |
| `sync --month`／`POST /admin/sync?month=` | `MonthStart`／`MonthEnd`（UTC 半開） | false | `Batch.Cursor` 保持 `nil` |
| webhook buffer flush | 批量 IDs | false | `Batch.Cursor` 保持 `nil` |

`trackCursor` 在 `Run` 內以 `w.MonthStart == nil` 決定，`RunBatch` 固定 `false`。`nil` 水位由 `pkg/store/queries.go` 的 `upsertSyncStateSQL` 保護，語意是「不動」而非「清空」：

```sql
cursor_time = IF(
    VALUES(cursor_time) IS NULL,
    cursor_time,
    GREATEST(VALUES(cursor_time), IFNULL(cursor_time, VALUES(cursor_time)))
)
```

- 月度與 batch 送 `cursor_time = NULL` → 保留原水位；`last_run_at`／`status` 仍會更新。
- `Cursor` 模式送實際水位 → 只前進不後退；較舊的值或整批 `xero_updated_at` 皆為 `nil` 時也不推進。
- **-1 秒策略**：cursor 儲存時減 1 秒（`maxAt.Add(-time.Second)`），避免漏資料。重複抓取可接受，冪等 upsert 會處理。
- **排序保證**：分頁端點自動加入 `order=UpdatedDateUTC`，確保 chunk save 時 cursor 推進是有序的。

### 4.1 Chunk Save 與 Cursor 更新

分頁端點（Contacts、Invoices、CreditNotes、Payments）支援 chunk save：

- **中間 flush**（達 `writeChunkRows = 500`）：帶 cursor（`maxAt - 1s`），`IsFinalFlush = false`
  - 只更新 `cursor_time`，不更新 `status` 和 `last_run_at`
  - 由 `UpdateCursor` 方法處理
- **最終 flush**（`total > 0` 時）：帶 cursor（`maxAt - 1s`），`IsFinalFlush = true`
  - 更新 `cursor_time`、`last_run_at`、`status = 'idle'`
  - 由 `UpsertSyncState` 方法處理（同交易內）
- **空資料**（`total == 0`）：不呼叫 `ApplyBatch`
  - 由 `UpsertSyncStateWithoutTx` 更新 `status = 'idle'` 和 `last_run_at`
  - cursor 也會更新（`maxAt - 1s`，如果有的話）

**Accounts 端點**不分頁（`paginated = false`），每次全量 sync，不需要 chunk save。

### 4.2 狀態轉換時機

`sync_state.status` 遵循 `idle → running → idle/failed` 狀態機：

| 時機 | 方法 | status | last_error | cursor_time | last_run_at |
|------|------|--------|------------|-------------|-------------|
| `Run`／`RunBatch` 開始前 | `MarkSyncRunning` | `running` | `NULL` | 不變 | 不變 |
| 中間 flush 成功 | `UpdateCursor`（交易內） | 不變 | 不變 | 推進（`maxAt - 1s`） | 不變 |
| 最終 flush 成功 | `UpsertSyncState`（交易內） | `idle` | `NULL` | 推進（`maxAt - 1s`） | 更新為當前時間 |
| 空資料成功 | `UpsertSyncStateWithoutTx` | `idle` | `NULL` | 推進（`maxAt - 1s`，如果有） | 更新為當前時間 |
| `Run`／`RunBatch` 失敗（defer） | `MarkSyncFailed` | `failed` | 錯誤訊息 | 不變 | 更新為當前時間 |

- `MarkSyncRunning` 與 `MarkSyncFailed` 用 `s.db.ExecContext`，不用交易（失敗時資料交易已回滾，無法用交易）。
- `MarkSyncFailed` 會更新 `last_run_at`，確保從 `sync_state` 可以看出最近一次嘗試同步的時間（即使失敗）。
- `r.store == nil` 時跳過狀態轉換（維持無 DB 的單元測試行為）。
- `defer MarkSyncFailed` 確保所有 return path 都會標記 failed；忽略 `MarkSyncFailed` 自身的錯誤，避免掩蓋原始錯誤。

## 5. 失敗語意

| 層級 | 行為 |
|------|------|
| 單頁 fetch／解析失敗 | `Run` 回錯；先前已 flush 的 chunk 留在 DB，cursor 已更新到最後一個成功的 chunk（`maxAt - 1s`），status 為 `failed`（重跑冪等覆蓋） |
| `ApplyBatch` 失敗 | 該 chunk 整批回滾，先前已 flush 的資料保留，cursor 已更新到前一個成功的 chunk，回錯 |
| `RunTenant` 單一實體失敗 | `slog.Warn` 後續跑下一個實體 |
| `RunTenant` 五個實體全失敗 | 回錯（`tenant ... 全部 5 個實體同步皆失敗`） |
| `RunAllIncremental` 單一 tenant 失敗 | `slog.Warn` 後續跑下一個 tenant |
| `RunAllMonth` 任一實體失敗 | 記該 tenant 失敗，續跑其餘 entity／tenant |
| `RunEntityIncremental`／`RunEntityMonth` 未知實體 | 讀 tenant 清單前直接回錯 |
| `ListTenants` 失敗 | 直接回錯，不跑任何 tenant |

`RunAllIncremental` 與 `RunAllMonth` 都以 tenant 為失敗計數單位，最後回 `同步失敗 N 個 tenant`；全成功回 `nil`。非致命錯誤一律 `slog.Warn` + 降級續跑，單一 entity／tenant 失敗不得讓整輪崩潰。

呼叫端處理：

- `sync` subcommand（`runSync`）原樣回傳錯誤，`main.go` 記錄後以 exit 1 結束。
- cron（`runCronSync`）只 `slog.Warn("cron sync not all successful")`，不中斷排程。
- webhook buffer 對 `RunBatch` 失敗只 `slog.Warn`，事件已在 `sync_jobs`，交由 cron 增量收斂。

## 6. tenant 清單

`RunAllIncremental`／`RunAllMonth` 都先呼叫 `store.ListTenants`：

```sql
SELECT ... FROM xero_tenants WHERE auth_status = 'active' ORDER BY tenant_id
```

- 只列 `active`；`auth_required` 的 tenant（refresh 回 `invalid_grant`）需重新 `auth` 才會回到排程（spec §6）。
- 空清單回空 slice，不算錯誤（`tenants=0, failed=0`）。
- 計畫文件早期稱 `LoadTenants`，實作名稱為 `store.ListTenants`。

## 7. 端點能力

分頁、增量、月度篩選、批量 IDs 的正式能力矩陣見 `doc/spec.md` §5，本文件不重抄。程式端唯一來源是 `pkg/sync/sync.go` 的 `entitySpecs`：

- `supportsSince`：目前對 Invoices、CreditNotes、Payments、Contacts 開啟；其餘實體即使帶 `Cursor`，`buildRequest` 也不會加 `If-Modified-Since`，退化為全量 fetch + 冪等 upsert。
- `monthFilter`：目前只對 Invoices 開啟，組 `Date>=DateTime(...)&&Date<DateTime(...)`；其餘實體月度視窗退化為全量。
- `supportsBatchIDs`：只對 Invoices、Contacts 開啟，並以 `SupportsBatchIDs` 對外匯出。
- `paginated`：只對 Accounts 關閉（單次回傳全部）。

未開啟的能力一律關閉，不得假設可用；核對結果先回寫 spec §5，再改 `entitySpecs`。

## 8. 觸發來源

| 觸發 | 入口 | 實際呼叫 |
|------|------|----------|
| `sync`（無 `--month`） | `commands.go` `runSync` → `runSyncMode` | `RunAllIncremental` |
| `sync --month YYYY-MM` | `runSync` → `syncWindow` 組 UTC 半開區間 | `RunAllMonth` |
| `sync --entity NAME` | `runSync` → `parseSyncEntity` → `runSyncMode` | `RunEntityIncremental`／`RunEntityMonth` |
| cron | `commands.go` `runCronSync` | `RunAllIncremental` |
| `POST /admin/sync` | `pkg/handler` `AdminHandler` | `Run`（單 tenant／entity） |
| `POST /webhook` | `pkg/handler` `WebhookHandler` → `WebhookBuffer` | `RunBatch`（僅 Invoices／Contacts） |

## 9. 指令與測試入口

```bash
go run . sync                            # 依 cursor 增量（全部 active tenant）
go run . sync --month 2026-05            # 月度回補（不推進 cursor）
go run . sync --entity payments          # 只同步指定實體
go run . serve                           # cron 增量 + webhook

go test ./pkg/sync                       # 本模組全部測試
go test ./pkg/sync -run TestRunBatch -v  # 單一主題
```

需要 DB 的案例（cursor 不倒退、月度不推進、fetch 失敗保留水位）走測試 MySQL：

```bash
docker compose -f docker-compose.test.yml up -d
TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
  go test ./pkg/sync -count=1
```

未設 `TEST_MYSQL_DSN` 時 DB 案例一律 `t.Skip()`，`go test ./...` 不會因為沒有 MySQL 而紅；Xero 呼叫全部以 `httptest.Server` mock，不需真實憑證。

## 10. 已知限制

- 分塊寫入：每累積 500 列就 `flush` 一次，記憶體上限約 600 列（未達門檻的尾段）＋單頁 JSON；交易時間短，不會因資料量大而拉長。
- 中途失敗時先前已 flush 的 chunk 會留在 DB（可能見到部分新資料），cursor 已更新到最後一個成功的 chunk（`maxAt - 1s`）；下次重跑從該 cursor 繼續，靠 `ON DUPLICATE KEY UPDATE` 冪等覆蓋重複抓取的資料。
- -1 秒策略可能導致重複抓取部分資料（最多 1 秒內的資料），但冪等 upsert 會處理，不會產生重複列。
- 月度視窗對不支援日期篩選的實體退化為全量，`sync --month` 對這些實體等同全量重跑（結果與全量 sync 相同）。
- `RunBatch` 只支援 Invoices／Contacts；其他實體回 `端點不支援批量 IDs`，正常流程由 buffer 的 `SupportsBatchIDs` 先擋下。
- `RunTenant` 部分成功時回 `nil`（錯誤已記 log），呼叫端無法從回傳值區分「全成功」與「部分失敗」；需要精確結果時以 log 的 `failed` 欄位為準。
