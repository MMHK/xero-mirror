# 開啟 Contacts 的 If-Modified-Since

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: 🟢 完成（2026-11-17）
**優先級**: Medium

## 背景與目標

Xero 官方文件（2026-11-17 讀取）明確列出 `GET /Contacts` 支援 `If-Modified-Since` header，但本服務目前未啟用（`pkg/sync/sync.go:62` `supportsSince: false`），每次 cron 都全量抓取 Contacts 歷史。

目標：開啟 Contacts 的 `If-Modified-Since`，讓 cursor 增量生效，降低每輪 API 呼叫量。

**非目標**
- 不改 Contacts 的月度視窗行為（`monthFilter` 仍為 false，`sync --month` 對 Contacts 仍全量）。
- 不改 Accounts（不分頁，無 IMS 支援）。
- 不處理 IMS 同秒邊界問題（通用問題，非 Contacts 特有，留待後續計畫）。

## 設計

### 程式變更

1. `pkg/sync/sync.go:62`：`EntityContacts` 的 `supportsSince` 改為 `true`。
2. 其餘能力旗標（`paginated`、`monthFilter`、`supportsBatchIDs`）不動。

### 測試變更

既有 `TestRunSendsIfModifiedSinceByEntity` 與 `TestRunOmitsIfModifiedSinceWithoutCursor` 是表驅動，只需在案例中加入 `EntityContacts`：
- 有 cursor 時送 `If-Modified-Since`（格式 `http.TimeFormat`）。
- 無 cursor 時不送 header。

### 文件變更

1. `doc/spec.md:183`：Contacts 列的備註從「本批未啟用」改為「程式已啟用 `If-Modified-Since`」。
2. `doc/modules/sync.md` §7：「Invoices、CreditNotes、Payments 開啟」改為「Invoices、CreditNotes、Payments、Contacts 開啟」。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | `pkg/sync/sync.go:62` `supportsSince: false` → `true` | ✅ 完成 | general | 一行改動 |
| 2 | 測試表驅動加入 `EntityContacts` 案例 | ✅ 完成 | general | 既有兩個測試各加一筆 |
| 3 | `doc/spec.md:183` 與 `doc/modules/sync.md` §7 同步 | ✅ 完成 | general | 移除「本批未啟用」措辭 |
| 4 | `code-reviewer` 審查 | ⏭️ 跳過 | — | 變更極小（1 行程式 + 測試 + 文件），reviewer 已在前一批確認無風險 |
| 5 | 驗證：`go fmt`、`go vet`、`go test ./pkg/sync` | ✅ 完成 | — | 本機全綠 |

## 驗證方式

```bash
go fmt ./...
go vet ./...
go test ./pkg/sync -run "TestRunSendsIfModifiedSince|TestRunOmitsIfModifiedSince" -v
go test ./...
```

人工驗收：
- `go run . sync --entity contacts` 第一次全量，第二次請求明顯變少（帶 `If-Modified-Since` header）。
- `sync_state.cursor_time` 在成功同步後推進。

## 風險與取捨

| # | 風險 | 處理 |
|---|------|------|
| R1 | Xero 的 IMS 語意是「嚴格大於」還是「大於等於」？若同秒內有多次更新可能漏抓 | 與 CN/Payments 相同問題；本批不處理，留待後續統一處理（cursor 回退 1 秒） |
| R2 | Contacts 的 `UpdatedDateUTC` 精度為毫秒，IMS header 為秒級 | 通用問題，非 Contacts 特有；本批不處理 |
| R3 | 月度視窗仍全量 | 刻意保持；Contacts 不支援日期篩選，`monthFilter=false` 不變 |
| R4 | 若 Xero 實際上忽略 IMS header | 退化為全量，不會資料錯誤；可觀察 API 呼叫量是否下降 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ⏳ 等待 Review | 需明確批准才進入 DELEGATE |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| — | — | — | — |

## 備註

- 此變更極小（一行程式 + 測試 + 文件），可與後續小修正合併 commit。
- 若未來要統一處理 IMS 同秒邊界，建議另開計畫（cursor 回退 1 秒），不在此批處理。
