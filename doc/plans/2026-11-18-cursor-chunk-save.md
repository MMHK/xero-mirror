# Cursor Chunk Save 優化

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18  
**狀態**: 🟢 完成  
**優先級**: High

## 背景與目標

### 問題描述

目前 chunk save 邏輯存在以下問題：

1. **Cursor 只在成功完成時更新**：中間 flush 帶 `cursor=nil`，只有最後一次 flush 才帶 cursor
2. **中途失敗時 cursor 不更新**：下次增量會重新抓取所有已寫入的資料（雖然冪等 upsert 不會損壞資料，但效率低）
3. **中間 flush 會錯誤更新 `status` 和 `last_run_at`**：讓人誤以為同步成功完成
4. **空資料時 `status` 不會更新**：永遠停留在 `'running'`

### 研究結果

根據 Xero API 官方文檔研究：

| Entity | 支援 `order` | 支援分頁 | 預設排序 | 最佳化排序欄位 |
|--------|-------------|---------|---------|--------------|
| Contacts | ✅ | ✅ | `UpdatedDateUTC ASC, ContactID ASC` | `UpdatedDateUTC` |
| Accounts | ✅ | ❌ | 未明確說明（資料量小） | `UpdatedDateUTC` |
| Invoices | ✅ | ✅ | `UpdatedDateUTC ASC, InvoiceId ASC` | `UpdatedDateUTC` |
| CreditNotes | ✅ | ✅ | `UpdatedDateUTC ASC, CreditNoteID ASC` | `UpdatedDateUTC` |
| Payments | ✅ | ✅ | `UpdatedDateUTC ASC, PaymentId ASC` | `UpdatedDateUTC` |

**關鍵發現**：
- 所有五個實體都支援 `order` 參數
- 四個分頁端點預設已經按 `UpdatedDateUTC ASC` 排序
- Accounts 不分頁，一次返回全部資料

### 目標

1. 對於分頁端點（Contacts、Invoices、CreditNotes、Payments）：
   - 確保請求時明確帶 `order=UpdatedDateUTC`
   - 中間 flush 也帶 cursor，讓 sync_state 記錄進度
   - 中途失敗時 cursor 已經更新到最後一個成功的 chunk
   - 下次同步可以從中斷的地方繼續，不需要重新抓取所有資料

2. 對於不分頁端點（Accounts）：
   - 不需要修改，一次返回全部資料

3. 修復空資料時 `status` 不會更新的問題

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 修改 `buildRequest` 加入 `order=UpdatedDateUTC` | ✅ 完成 | general | 只對分頁端點生效 |
| 2 | 修改 `pageApplier.handle` 中間 flush 也帶 cursor | ✅ 完成 | general | cursor - 1 秒策略 |
| 3 | 修改 `ApplyBatch` 區分中間 flush 和最後 flush | ✅ 完成 | general | 中間只更新 cursor |
| 4 | 修復空資料時 `status` 不會更新的問題 | ✅ 完成 | general | 新增 UpsertSyncStateWithoutTx |
| 5 | 加入測試：驗證 chunk save 時 cursor 正確更新 | ✅ 完成 | general | 3 個測試案例（中間 flush cursor、空資料 status、最終 flush -1s） |
| 6 | 更新 `doc/modules/sync.md` 說明新的 cursor 邏輯 | ✅ 完成 | — | 加入 4.1 Chunk Save 章節 |
| 7 | 修復 `MarkSyncFailed` 未更新 `last_run_at` 的問題 | ✅ 完成 | — | 失敗時也記錄嘗試時間 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ✅ 已批准 | 實作完成，code review 通過 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 詳細設計

### 任務 1：修改 `buildRequest` 加入 `order=UpdatedDateUTC`

**檔案**：`pkg/sync/sync.go:539-552`

**修改內容**：
```go
func buildRequest(spec entitySpec, w Window) (url.Values, map[string]string) {
    query := url.Values{}
    headers := map[string]string{}
    
    // 確保按 UpdatedDateUTC 排序（chunk save 依賴此排序）
    // 只對分頁端點生效，Accounts 不分頁不需要
    if spec.paginated {
        query.Set("order", "UpdatedDateUTC")
    }
    
    if w.Cursor != nil && spec.supportsSince {
        headers["If-Modified-Since"] = w.Cursor.UTC().Format(http.TimeFormat)
    }
    if w.MonthStart == nil || w.MonthEnd == nil {
        return query, headers
    }
    if spec.monthFilter && spec.monthFilterField != "" {
        query.Set("where", monthWhere(spec.monthFilterField, *w.MonthStart, *w.MonthEnd))
    }
    return query, headers
}
```

### 任務 2：修改 `pageApplier.handle` 中間 flush 也帶 cursor

**檔案**：`pkg/sync/sync.go:420-423`

**修改內容**：
```go
if a.pending() >= writeChunkRows {
    // 中間 flush 也帶 cursor（因為資料已按 UpdatedDateUTC 排序）
    var cursor *time.Time
    if a.trackCursor {
        cursor = a.maxAt
    }
    if err := a.flush(cursor); err != nil {
        return 0, err
    }
}
```

### 任務 3：修改 `ApplyBatch` 區分中間 flush 和最後 flush

**檔案**：`pkg/store/batch.go:27-49`

**問題**：中間 flush 會把 `status` 設為 `'idle'`，但實際上同步還在進行中。

**解決方案**：新增一個參數 `isFinalFlush`，或者在 `Batch` 結構中新增一個欄位。

**選項 A**：在 `Batch` 結構中新增 `IsFinalFlush bool` 欄位
```go
type Batch struct {
    TenantID string
    Entity   string
    Cursor      *time.Time
    IsFinalFlush bool  // 新增：是否為最後一次 flush
    // ...
}
```

**選項 B**：根據 `Cursor` 是否為 nil 來判斷
- 如果 `Cursor != nil`，表示是最後一次 flush，更新 status 和 last_run_at
- 如果 `Cursor == nil`，表示是中間 flush，只更新 cursor_time

**建議**：選項 B 更簡單，但語意不夠清晰。選項 A 更明確。

**修改內容（選項 A）**：
```go
func (s *Store) ApplyBatch(ctx context.Context, b Batch) error {
    if b.empty() {
        return nil
    }
    var n int
    err := s.WithTx(ctx, func(tx *sql.Tx) error {
        count, applyErr := s.applyEntities(ctx, tx, b)
        if applyErr != nil {
            return applyErr
        }
        n = count
        
        // 只有最後一次 flush 才更新 status 和 last_run_at
        if b.IsFinalFlush {
            now := time.Now().UTC()
            return s.UpsertSyncState(ctx, tx, SyncState{
                TenantID: b.TenantID, Entity: b.Entity, CursorTime: b.Cursor,
                LastRunAt: &now, Status: "idle",
            })
        } else if b.Cursor != nil {
            // 中間 flush 只更新 cursor_time
            return s.UpdateCursor(ctx, tx, b.TenantID, b.Entity, b.Cursor)
        }
        return nil
    })
    if err != nil {
        return err
    }
    slog.Debug("批次寫入完成", "tenant", b.TenantID, "entity", b.Entity, "count", n)
    return nil
}
```

**新增 `UpdateCursor` 方法**：
```go
// UpdateCursor 只更新 cursor_time，不更新 status 和 last_run_at。
// 用於中間 flush，讓 sync_state 記錄進度但不改變狀態。
func (s *Store) UpdateCursor(ctx context.Context, tx *sql.Tx, tenantID, entity string, cursor *time.Time) error {
    // SQL: UPDATE sync_state SET cursor_time = GREATEST(cursor_time, ?) WHERE tenant_id = ? AND entity = ?
    // 需要新增一個新的 SQL 常數
}
```

### 任務 4：修復空資料時 `status` 不會更新的問題

**檔案**：`pkg/sync/sync.go:136-144`

**修改內容**：
```go
// 無論是否有資料，都要更新 status 和 last_run_at
if r.store != nil {
    now := time.Now().UTC()
    var cursor *time.Time
    if a.trackCursor {
        cursor = a.maxAt
    }
    
    if a.total > 0 {
        // 有資料時，最後一次 flush 會更新 sync_state
        if err := a.flush(cursor); err != nil {
            return fmt.Errorf("同步 %s: %w", entity, err)
        }
    } else {
        // 沒有資料時，也要更新 status 和 last_run_at
        if err := r.store.UpsertSyncState(ctx, nil, store.SyncState{
            TenantID: tenantID,
            Entity:   string(entity),
            CursorTime: cursor,
            LastRunAt: &now,
            Status:   "idle",
        }); err != nil {
            slog.Warn("更新 sync_state 失敗", "tenant", tenantID, "entity", entity, "err", err)
        }
    }
}
```

**問題**：`UpsertSyncState` 需要 `tx *sql.Tx`，但此時沒有交易。

**解決方案**：
- 選項 A：新增一個不需要 tx 的版本（使用 `db.ExecContext`）
- 選項 B：使用 `WithTx` 包一層（但沒有資料要寫入，只有 sync_state）

**建議**：選項 A 更簡單，新增一個 `UpsertSyncStateWithoutTx` 方法。

### 任務 5：加入測試

**測試案例**：
1. 模擬 chunk save 中途失敗，確認 cursor 已更新到最後一個成功的 chunk
2. 模擬空資料，確認 status 更新為 `'idle'`
3. 模擬中間 flush，確認 cursor 更新但 status 不變

### 任務 6：更新文件

**檔案**：`doc/modules/sync.md`

**更新內容**：
- 說明 chunk save 時 cursor 的更新邏輯
- 說明 `order=UpdatedDateUTC` 的重要性
- 說明 Accounts 端點不分頁的特殊處理

## 風險與決策（已確認）

### 風險 1：同一 `UpdatedDateUTC` 的資料可能被切斷

**決策**：使用 **-1 秒策略**

- cursor 儲存時減 1 秒（`cursor.Add(-time.Second)`）
- 下次增量會從 `cursor - 1s` 開始抓取
- 可能會重複抓取部分資料，但冪等 upsert 會處理
- 相互覆蓋部分可以接受

**實作**：
```go
// 在 flush 時，cursor 減 1 秒
if a.trackCursor && a.maxAt != nil {
    cursor := a.maxAt.Add(-time.Second)
    // ...
}
```

### 風險 2：Accounts 端點不分頁

**決策**：每次全量 sync

- Accounts 端點 `paginated: false`，一次返回全部資料
- 不需要 chunk save，也不需要排序
- 不需要修改現有邏輯

### 風險 3：`If-Modified-Since` 的時間精度

**決策**：使用秒精度，毫秒差異可接受

- HTTP header 的時間格式是 RFC 1123，精度到秒
- Xero 的 `UpdatedDateUTC` 精度到毫秒
- 毫秒差異造成的相互覆蓋可以接受
- 配合 -1 秒策略，不會漏資料

### 風險 4：增量查詢方式

**決策**：使用 `If-Modified-Since`，不使用 `where=UpdatedDateUTC>DateTime(...)`

- 現有邏輯已經使用 `If-Modified-Since`
- 不需要修改
- 配合 -1 秒策略，不會漏資料

## 約束條件

### sync action 與 cron job 執行邏輯一致性

**要求**：`sync` action 與 cron job 在沒有引入 `-month` 參數下是相同的執行邏輯或代碼。

**現有邏輯**：
- `go run . sync`：呼叫 `runSync()` → `runner.RunAllIncremental(ctx)`
- cron job：呼叫 `runCronSync()` → `runner.RunAllIncremental(ctx)`

**結論**：兩者已經使用相同的 `RunAllIncremental`，不需要修改。

**驗證**：
- `commands.go:runSync()` 和 `commands.go:runCronSync()` 都呼叫 `runner.RunAllIncremental(ctx)`
- 兩者的差異只在 context 處理和 log 輸出，核心邏輯相同

## 備註

- 本 plan 只針對分頁端點（Contacts、Invoices、CreditNotes、Payments）的 chunk save 優化
- Accounts 端點不分頁，不需要修改
- 需要確認 Xero API 的 `If-Modified-Since` 行為（strictly greater than 還是 greater than or equal）
- 需要確認是否要使用 `where=UpdatedDateUTC>DateTime(...)` 代替 `If-Modified-Since`
