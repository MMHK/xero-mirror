# 啟用所有實體的月度日期過濾

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**最後更新**: 2026-11-18
**狀態**: 🟢 完成（待 commit；任務 1–5 ✅）
**優先級**: High

## 背景與目標

### 問題描述

在 `2026-11-17-sync-entity-flag.md` 完成後，`sync --entity` 可以只同步指定實體，但 `--month` 參數對 CreditNotes、Payments、Contacts、Accounts 不具篩選效果（`monthFilter=false`），等同全歷史重抓。

使用者回報：「配合 `--entity` 參數的話，`--month` 參數就沒有 filter 效果了」。

### 根因分析

根據 Xero API 文件研究，各實體的日期過濾能力如下：

| 端點 | Date 欄位 | UpdatedDateUTC 欄位 | 建議過濾欄位 |
|------|----------|-------------------|-------------|
| `/Invoices` | ✅ | ✅ | `Date` |
| `/CreditNotes` | ✅ | ✅ | `Date` |
| `/Payments` | ✅ | ✅ | `Date` |
| `/Contacts` | ❌ | ✅ | `UpdatedDateUTC` |
| `/Accounts` | ❌ | ✅ | `UpdatedDateUTC` |

原程式碼只有 Invoices 啟用 `monthFilter: true`，其餘四個實體為 `false`，導致 `--month` 參數被忽略。

### 目標

讓所有五個實體在 `--month` 模式下都能進行日期過濾：
- Invoices / CreditNotes / Payments：使用 `Date` 欄位
- Contacts / Accounts：使用 `UpdatedDateUTC` 欄位

**非目標**

- 不改動 cursor 增量模式（`If-Modified-Since`）的行為
- 不動 webhook buffer、cron、admin 的既有行為
- 不新增 subcommand、不引入新依賴

## 設計

### 1. 擴展 `entitySpec` struct

新增 `monthFilterField` 欄位，指定各實體使用的日期過濾欄位名稱：

```go
type entitySpec struct {
    path             string
    paginated        bool
    supportsSince    bool
    monthFilter      bool
    monthFilterField string // 新增：月度過濾使用的欄位名稱（"Date" 或 "UpdatedDateUTC"）
    supportsBatchIDs bool
    singlePath       string
}
```

### 2. 更新 `entitySpecs` 對照表

所有實體都啟用 `monthFilter: true`，並指定對應的 `monthFilterField`：

```go
var entitySpecs = map[Entity]entitySpec{
    EntityContacts:    {monthFilter: true, monthFilterField: "UpdatedDateUTC", ...},
    EntityAccounts:    {monthFilter: true, monthFilterField: "UpdatedDateUTC", ...},
    EntityInvoices:    {monthFilter: true, monthFilterField: "Date", ...},
    EntityCreditNotes: {monthFilter: true, monthFilterField: "Date", ...},
    EntityPayments:    {monthFilter: true, monthFilterField: "Date", ...},
}
```

### 3. 修改 `monthWhere` 函式

讓 `monthWhere` 接受欄位名稱參數，動態產生 where 條件：

```go
// 修改前
func monthWhere(start, end time.Time) string {
    return fmt.Sprintf("Date>=DateTime(%d,%d,%d)&&Date<DateTime(%d,%d,%d)", ...)
}

// 修改後
func monthWhere(field string, start, end time.Time) string {
    return fmt.Sprintf("%s>=DateTime(%d,%d,%d)&&%s<DateTime(%d,%d,%d)",
        field, ..., field, ...)
}
```

### 4. 修改 `buildRequest` 函式

使用 `spec.monthFilterField` 呼叫 `monthWhere`：

```go
if spec.monthFilter && spec.monthFilterField != "" {
    query.Set("where", monthWhere(spec.monthFilterField, *w.MonthStart, *w.MonthEnd))
}
```

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | `pkg/sync/sync.go`：擴展 `entitySpec` struct，新增 `monthFilterField` 欄位 | ✅ 完成 | general | |
| 2 | `pkg/sync/sync.go`：更新 `entitySpecs`，所有實體啟用 `monthFilter: true` 並指定欄位 | ✅ 完成 | general | |
| 3 | `pkg/sync/sync.go`：修改 `monthWhere` 函式，接受欄位名稱參數 | ✅ 完成 | general | |
| 4 | `pkg/sync/sync.go`：修改 `buildRequest` 函式，使用 `spec.monthFilterField` | ✅ 完成 | general | |
| 5 | `pkg/sync/sync_test.go`：更新 `TestRunMonthWindowFallback`，驗證 Contacts 使用 `UpdatedDateUTC` | ✅ 完成 | general | |
| 6 | `pkg/sync/sync_test.go`：新增 `TestRunMonthUsesCorrectDateField` 表驅動測試，驗證所有實體 | ✅ 完成 | general | |
| 7 | `doc/spec.md`：更新端點能力矩陣與月度回補說明 | ✅ 完成 | general | |
| 8 | 驗證：`go fmt ./...`、`go vet ./...`、`go test ./pkg/sync` | ✅ 完成 | general | 全綠 |

## 驗收方式

1. `go build -o xero-mirror.exe .` 成功。
2. `go test ./pkg/sync -v` 全綠，包含新增的 `TestRunMonthUsesCorrectDateField`。
3. 實機測試：
   - `go run . sync --month=2026-05 --entity=invoices` → 送出 `where=Date>=DateTime(2026,5,1)&&Date<DateTime(2026,6,1)`
   - `go run . sync --month=2026-05 --entity=creditnotes` → 送出 `where=Date>=DateTime(2026,5,1)&&Date<DateTime(2026,6,1)`
   - `go run . sync --month=2026-05 --entity=payments` → 送出 `where=Date>=DateTime(2026,5,1)&&Date<DateTime(2026,6,1)`
   - `go run . sync --month=2026-05 --entity=contacts` → 送出 `where=UpdatedDateUTC>=DateTime(2026,5,1)&&UpdatedDateUTC<DateTime(2026,6,1)`
   - `go run . sync --month=2026-05 --entity=accounts` → 送出 `where=UpdatedDateUTC>=DateTime(2026,5,1)&&UpdatedDateUTC<DateTime(2026,6,1)`
4. `go fmt ./...` 與 `go vet ./...` 無問題。

## 風險與取捨

| # | 風險 | 對策 |
|---|------|------|
| 1 | Contacts / Accounts 使用 `UpdatedDateUTC` 過濾，可能抓到「当月更新但非当月建立」的資料 | 這是 by design：`UpdatedDateUTC` 反映最後修改時間，適合增量同步場景；若需嚴格按建立日期，應使用 `Date` 欄位（但 Contacts / Accounts 無此欄位） |
| 2 | Accounts 不分頁，`where` 條件可能讓回傳資料量過大 | Accounts 通常數量有限（數百筆），且 Xero API 支援 `where` 過濾，風險可控 |
| 3 | 既有測試可能依賴 `monthFilter=false` 的行為 | 已更新 `TestRunMonthWindowFallback`，並新增 `TestRunMonthUsesCorrectDateField` 覆蓋所有實體 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ✅ 已批准 | 使用者確認「補完 plan MD」，進入實作 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| （待填寫） | | | |

## 備註

- 本計畫接續 `2026-11-17-sync-entity-flag.md`，解決其第 118 行提到的後續問題：「`--month` 對 CN／Payments 仍為全量，若後續要做月篩選，另開計畫」。
- 根據 Xero 官方文件，所有五個實體都支援 `where` 參數進行日期過濾，只是欄位名稱不同（`Date` vs `UpdatedDateUTC`）。
- 修改後，`--entity` 配合 `--month` 可以精確控制同步範圍，大幅減少 API 配額消耗與等待時間。
