# 放寬 invoice number 欄位長度（修正 Data too long）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: 🟢 完成（2026-11-17 驗證通過）
**優先級**: High

## 背景與目標

月度回補 INVOICES 時出現：

```
upsert invoice 8a052965-29bf-43a2-8421-62bb089b0bf7:
Error 1406 (22001): Data too long for column 'number' at row 1
```

原因：`xero_invoices.number` 目前是 `varchar(64)`，但 Xero `InvoiceNumber` 的 API 上限是 **255 字元**（官方 schema 明載 `max length = 255`）。實際 tenant 存在超過 64 字元的發票號，導致整筆 upsert 失敗、整個 INVOICES 實體同步中止（cursor 與資料同交易，因此該批全部回滾）。

同類欄位 `xero_credit_notes.number`、`xero_payments.invoice_number` 都是同一種 Xero 發票號，目前也都是 `varchar(64)`，屬相同缺陷，一併放寬避免下一個實體踩雷。

**目標**：把三個欄位放寬到 `varchar(255)`，讓合法的 Xero 發票號可完整鏡射，並補一個 255 字元的 DB 整合回歸測試。

**非目標**：不改同步核心邏輯、不做字串截斷（截斷會讓鏡射資料失真）、不動其他欄位（contact name 255／account name 255／currency 3 已足夠）。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 新增 `0010_widen_xero_invoices_number`（up＋down） | ✅ 完成 | general | `number varchar(255) NULL` |
| 2 | 新增 `0011_widen_xero_credit_notes_number`（up＋down） | ✅ 完成 | general | `number varchar(255) NULL` |
| 3 | 新增 `0012_widen_xero_payments_invoice_number`（up＋down） | ✅ 完成 | general | `invoice_number varchar(255) NULL` |
| 4 | 在 `pkg/store/queries_test.go` 檔尾追加 255 字元 invoice number 的 upsert 回歸測試 | ✅ 完成 | general | 依 §6 每支 migration 只動一張表；依 §8 只加 1 個案例；review 建議改 `require.Equal` 已補 |
| 5 | `go build` → 對 dev DB 套用 migration → `go test ./...` → 重跑 `sync --month` 驗證 | ✅ 完成 | general | `go build`／`go vet`／`go test ./...` 皆綠；2026-11 回補 INVOICES 成功（該 tenant 共 2123 筆，`number` 最長 112、334 筆 >64）；原失敗的 `8a052965…` 已入庫（68 字元），1406 已消除 |
| 6 | code-reviewer 對照本計畫審查 | ✅ 完成 | code-reviewer | 無 Critical/High；M1／M2 文件項由主 agent 處理 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准 | 使用者選擇「批准，按計畫實作（建議）」 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer | 無阻斷問題。M1 計畫狀態未更新、M2 缺 `sync --month` 實跑紀錄；L1 測試只比長度；L3 計畫誤寫 account name 150 | L1 已改 `require.Equal`；M1／L3 已更新本文件；M2 以 2026-11 月度回補 INVOICES 成功與遠端 DB 查詢佐證 |
| 2026-11-17 | code-reviewer | L2 建議補三欄位寬度測試、L4 建議 spec 註記 Xero 上限 255 | 屬可選項，暫不擴張（§8 不批量生成測試；spec 只列欄位名、未寫長度） |

## 備註

- 既有 DB 會由啟動時的 `store.Migrate` 自動套用 0010–0012，不需手動執行 SQL；三支 migration 都是 `ALTER TABLE ... MODIFY COLUMN`，不改舊檔。
- down 檔會把欄位改回 `varchar(64)`；若屆時已有超過 64 字元的資料，縮回會失敗，屬人工回滾時的預期風險。
- `doc/spec.md` 只列欄位名、未寫長度，無需修改；若 Review 認為要標註 API 上限，再補一行。
- 驗證環境提醒：`.env` 的 `MYSQL_DSN` 指向 `192.168.33.8:3306/xero_mirror`，非本機 docker（port 3307）；migration 已套用在實際 dev DB。
