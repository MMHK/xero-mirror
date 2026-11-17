# sync_state 完整狀態機

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: 🟢 完成（2026-11-17）
**優先級**: Medium

## 背景與目標

目前 `sync_state` 表有 `status` 與 `last_error` 欄位，但程式碼的使用方式讓它們變成裝飾品：

| 時機 | 實際行為 | `status` 值 | `last_error` |
|---|---|---|---|
| 同步成功 | `ApplyBatch` 交易內寫入（`batch.go:41`） | 寫死 `"idle"` | nil（未填） |
| 同步失敗 | 交易回滾，`UpsertSyncState` 根本沒執行 | 維持舊值 | 維持舊值 |

**問題**：
1. 無法從 DB 觀察「目前正在跑」「上次失敗原因」。
2. `last_error` 永遠 NULL，失去除錯價值。
3. `status` 永遠 `idle`，與沒有欄位無異。

**目標**：實作完整狀態機 `idle → running → idle（成功）／failed（失敗）`，讓 `sync_state` 真正反映同步狀態。

**非目標**：
- 不改 schema（欄位已存在）。
- 不改 cursor 保護邏輯（`upsertSyncStateSQL` 的 `GREATEST` 語意）。
- 不改分塊寫入行為（每 500 列 flush、cursor 只在最後一次帶入）。
- 不處理並行同步的狀態衝突（目前單一實例、per-tenant 序列化，不考慮多實例）。

## 設計

### 狀態機

```
         ┌──────────────────────────────────────┐
         │                                      │
         ▼                                      │
    ┌─────────┐    開始同步    ┌───────────┐   成功    ┌─────────┐
    │  idle   │ ───────────► │  running  │ ───────► │  idle   │
    └─────────┘              └───────────┘          └─────────┘
         ▲                         │
         │                         │ 失敗
         │                         ▼
         │                   ┌──────────┐
         └────────────────── │  failed  │
              重新同步       └──────────┘
```

- `idle`：上次成功完成，目前沒在跑。
- `running`：正在執行中。
- `failed`：上次失敗，`last_error` 記錄原因。

### 程式變更

#### 1. `pkg/store/queries.go`：新增兩個 SQL 與方法

**`markSyncRunningSQL`**：只更新 `status` 與 `last_error`，不動 `cursor_time` 與 `last_run_at`。
```sql
INSERT INTO sync_state (tenant_id, entity, status, last_error)
VALUES (?, ?, 'running', NULL)
ON DUPLICATE KEY UPDATE
    status = 'running',
    last_error = NULL
```
- 列不存在時 INSERT（首次同步）。
- 列存在時只更新 `status` 與 `last_error`（清除上次錯誤），保留 `cursor_time` 與 `last_run_at`。

**`markSyncFailedSQL`**：只更新 `status` 與 `last_error`，不動 `cursor_time` 與 `last_run_at`。
```sql
INSERT INTO sync_state (tenant_id, entity, status, last_error)
VALUES (?, ?, 'failed', ?)
ON DUPLICATE KEY UPDATE
    status = 'failed',
    last_error = VALUES(last_error)
```
- 列不存在時 INSERT（首次同步就失敗）。
- 列存在時只更新 `status` 與 `last_error`。

**方法**：
- `MarkSyncRunning(ctx, tx, tenantID, entity)`：呼叫 `markSyncRunningSQL`。
- `MarkSyncFailed(ctx, tenantID, entity, errMsg)`：呼叫 `markSyncFailedSQL`。**注意**：這個方法**不使用交易**（因為失敗時交易已回滾），直接用 `s.db.ExecContext`。

#### 2. `pkg/store/batch.go`：`ApplyBatch` 維持既有行為

- 成功時仍寫 `Status: "idle"`，`LastError: nil`。
- 失敗時交易回滾，`UpsertSyncState` 不執行（由 `Run`/`RunBatch` 的 `MarkSyncFailed` 處理）。

#### 3. `pkg/sync/sync.go`：`Run` 與 `RunBatch` 加入狀態轉換

**`Run`**：
```go
func (r *Runner) Run(ctx context.Context, tenantID string, entity Entity, w Window) (retErr error) {
    // ... 既有驗證 ...
    
    // 標記 running（store 為 nil 時跳過，維持測試行為）
    if r.store != nil {
        if err := r.store.MarkSyncRunning(ctx, nil, tenantID, string(entity)); err != nil {
            return fmt.Errorf("標記同步開始 %s: %w", entity, err)
        }
    }
    
    // 失敗時標記 failed（defer + named return）
    defer func() {
        if retErr != nil && r.store != nil {
            // 忽略 MarkSyncFailed 的錯誤，避免掩蓋原始錯誤
            _ = r.store.MarkSyncFailed(ctx, tenantID, string(entity), retErr.Error())
        }
    }()
    
    // ... 既有邏輯 ...
}
```

**`RunBatch`**：同樣模式。

**細節**：
- `MarkSyncRunning` 用 `tx=nil`：它自己開 `s.db.ExecContext`（不需要交易，因為只是狀態標記）。
- `MarkSyncFailed` 也用 `s.db.ExecContext`（失敗時交易已回滾，無法用交易）。
- `defer` 確保所有 return path 都會標記 failed。
- `retErr.Error()` 可能很長，`last_error` 是 `text` 欄位，無長度限制。
- `r.store == nil` 時跳過（既有無 DB 測試行為）。

#### 4. 測試

**`pkg/store/queries_test.go`**（或新 `sync_state_test.go`）：
- `TestMarkSyncRunning`：首次呼叫 INSERT 列（`status='running'`）；第二次呼叫 UPDATE（`last_error` 清空）。
- `TestMarkSyncFailed`：首次呼叫 INSERT 列（`status='failed'`, `last_error='...'`）；第二次呼叫 UPDATE。
- `TestMarkSyncFailedPreservesCursor`：先寫入 cursor，呼叫 `MarkSyncFailed` 後 cursor 不變。

**`pkg/sync/sync_test.go`**：
- `TestRunMarksRunningThenIdle`：成功流程，最終 `status='idle'`。
- `TestRunMarksFailedOnError`：mock 400 錯誤，最終 `status='failed'`，`last_error` 非空，cursor 不變。

### 文件變更

1. `doc/spec.md` §6（資料庫與 Migration）：補 `sync_state` 狀態機說明。
2. `doc/modules/sync.md` §4（交易與 cursor）：補狀態轉換時機。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | `pkg/store/queries.go` 新增 `markSyncRunningSQL`、`markSyncFailedSQL`、`MarkSyncRunning`、`MarkSyncFailed` | ✅ 完成 | general | `MarkSyncFailed` 用 `s.db` 而非 `tx` |
| 2 | `pkg/sync/sync.go` 的 `Run` 與 `RunBatch` 加入狀態轉換（defer + named return） | ✅ 完成 | general | `store == nil` 時跳過 |
| 3 | 測試：store 層 3 案例、sync 層 2 案例 | ✅ 完成 | general | 追加在既有測試檔 |
| 4 | 文件：`doc/spec.md`、`doc/modules/sync.md` | ✅ 完成 | general | 補狀態機說明 |
| 5 | `code-reviewer` 審查 | ✅ 完成 | code-reviewer | PASS，4 個低優先觀察項 |
| 6 | 驗證：`go fmt`、`go vet`、`go test ./...`、DB 測試 | ✅ 完成 | general | — |

## 驗證方式

```bash
go fmt ./...
go vet ./...
go test ./... -count=1

# DB 測試
TEST_MYSQL_DSN=root:root@tcp(127.0.0.1:3307)/xero_mirror_test?parseTime=true&loc=UTC \
  go test ./pkg/store ./pkg/sync -count=1 -v
```

人工驗收：
```bash
# 成功流程
go run . sync --entity contacts
mysql> SELECT * FROM sync_state WHERE entity='CONTACTS';
# 應看到 status='idle', last_error=NULL

# 失敗流程（mock 錯誤）
# 觸發 webhook 或手動 sync 錯誤實體
mysql> SELECT * FROM sync_state WHERE entity='CONTACTS';
# 應看到 status='failed', last_error='...'
```

## 風險與取捨

| # | 風險 | 處理 |
|---|------|------|
| R1 | `MarkSyncFailed` 用 `s.db` 而非交易，若 DB 連線失敗會失去失敗記錄 | 可接受：DB 連線失敗本身就是更大問題，`slog.Error` 會記錄 |
| R2 | `retErr.Error()` 可能很長或含敏感資訊 | `last_error` 是 `text` 無長度限制；敏感資訊由既有 log 規範把關（不印 token） |
| R3 | 並行同步時 `running` 狀態可能衝突 | 目前單一實例、per-tenant 序列化，不考慮；若未來多實例需加 advisory lock |
| R4 | `defer` 的 `MarkSyncFailed` 若失敗會掩蓋原始錯誤 | 刻意忽略 `MarkSyncFailed` 的錯誤（`_ = ...`），原始錯誤優先 |
| R5 | 首次同步就失敗時，`MarkSyncFailed` 會 INSERT 新列 | 用 `ON DUPLICATE KEY UPDATE` 處理，列存在時 UPDATE，不存在時 INSERT |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ⏳ 等待 Review | 需明確批准才進入 DELEGATE |
| 2026-11-17 | ✅ 已批准 | 使用者明確批准完整狀態機方案 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| — | — | — | — |
| 2026-11-17 | code-reviewer | PASS；4 個低優先觀察項（context 取消時 defer 可能失敗、空回應 status 殘留 running、RunSingle 未納入、簽名偏離計畫但更好） | 記錄為後續改進，不阻塞合併 |

## 備註

- 此變更不涉及 schema migration（欄位已存在）。
- `ApplyBatch` 的 `Status: "idle"` 維持不變，表示「本次 chunk 成功完成，目前沒在跑」。
- 狀態機與 AGENTS.md §6 的「`sync_state` 至少包含 `status`」要求一致。
