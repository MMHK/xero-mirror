# Live Xero 整合測試（TEST_XERO_TENANT_ID / TEST_XERO_ACCESS_TOKEN）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: ✅ 完成（實跑待憑證；任務 7 維持 🟡 部分完成）
**優先級**: Medium

## 背景與目標

目前 `pkg/xero`、`pkg/sync` 的 Xero 呼叫全部用 `httptest.Server` mock，優點是 `go test ./...` 零憑證、零配額，缺點是無法驗證真實 Xero API 的行為（tenant header 綁定、分頁筆數、`IDs` 參數、429/`Retry-After` 的實際格式、`/Date(...)/` 時間格式）。

`AGENTS.md` §8 允許「需要真憑證的測試用 `t.Skip()`」，因此本計畫新增**opt-in live 測試**：未設 `TEST_XERO_ACCESS_TOKEN`／`TEST_XERO_TENANT_ID` 一律 skip，預設流程完全不變。

**目標**

1. 用本機環境變數提供 access token 與 tenant，驗證真實 read-only 呼叫。
2. 只讀（GET），**不發任何 PUT／POST／DELETE**（本服務定位即純唯讀）。
3. 預設 `go test ./...` 不因缺憑證而紅，也不消耗任何 Xero 配額。
4. 文件寫明 access token 生命週期（約 30 分鐘）與手動更新方式。

**非目標**

- 不做 CI 自動跑（access token 30 分鐘即失效，且會吃配額）。
- 不使用 `TEST_XERO_REFRESH_TOKEN`（refresh token 每次輪換，放 env 會被第一次 refresh 弄壞）。
- 不驗證 webhook（需公開 HTTPS 端點，超出範圍）。

## 變數設計

| 變數 | 必填 | 說明 |
|------|------|------|
| `TEST_XERO_TENANT_ID` | 是 | Xero organisation 的 `tenantId`（`GET /connections` 可查） |
| `TEST_XERO_ACCESS_TOKEN` | 是 | 手動取得的 access token，效期約 30 分鐘 |

兩者缺一即 `t.Skip()`（不設預設值、不寫入 `.env.example` 真值）。
**不新增** `TEST_XERO_CLIENT_ID`／`SECRET`：本計畫只驗已取得的 token，不驗 OAuth 交換流程（OAuth 已有 mock 測試）。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 新增 `pkg/xero/live_test.go`：`requireLiveXero(t)` helper（讀 env、缺值 `t.Skip`、不印 token） | ✅ 完成 | general | review ✅ |
| 2 | 案例 A：`GET /Connections` 斷言回傳的 `tenantId` 包含 `TEST_XERO_TENANT_ID`（驗 token 有效＋tenant 綁定） | ✅ 完成 | general | 1 次 API call |
| 3 | 案例 B：`GET /Invoices?page=1` 斷言 200、可解析、筆數 ≤100；每個都有 `InvoiceID`（驗分頁與 DTO 解析） | ✅ 完成 | general | 1 次 API call |
| 4 | 案例 C：`GET /Contacts?page=1` 同 B（驗第二個實體與 `Xero-tenant-id` header 路徑） | ✅ 完成 | general | 1 次 API call |
| 5 | `.env.example` 補 `TEST_XERO_TENANT_ID=`／`TEST_XERO_ACCESS_TOKEN=` 空值＋註解（標明僅測試用） | ✅ 完成 | general | 無真值 |
| 6 | `doc/dev-setup.md` 新增「Live Xero 測試」章節：取得 token 步驟、30 分鐘限制、配額提醒、執行指令 | ✅ 完成 | general | review ✅ |
| 7 | 驗證：無 env 時 `go test ./pkg/xero` 全 skip；有 env 時 3 案例實跑綠；`go test ./...` 預設全綠 | 🟡 部分完成 | — | 無 env 的 3 SKIP 與全套綠已驗；**有 env 的實跑待使用者提供 token**（access token 約 30 分鐘，需手動重取） |
| 8 | （review 追蹤）`requireLiveXero` 改用 `strings.TrimSpace` 判空，避免 `" "` 誤觸真實 API | ✅ 完成 | general | 全空白 env 實測 3 SKIP |

**依賴順序**：1 → (2、3、4) → (5、6) → 7。一次只派一個邏輯任務（1–4 可同一批），完成送 `code-reviewer`。

## 技術細節

```go
// pkg/xero/live_test.go
func requireLiveXero(t *testing.T) (token, tenantID string) {
    t.Helper()
    token = os.Getenv("TEST_XERO_ACCESS_TOKEN")
    tenantID = os.Getenv("TEST_XERO_TENANT_ID")
    if token == "" || tenantID == "" {
        t.Skip("未設定 TEST_XERO_ACCESS_TOKEN／TEST_XERO_TENANT_ID")
    }
    return token, tenantID
}
```

- client 用既有 `NewClientWithTokenSource("https://api.xero.com/api.xro/2.0", func(...) (string, error) { return token, nil })`；token 以閉包注入，不走 `TokenManager`（避免測試碰 DB／refresh）。
- 不印 token／完整 payload（AGENTS §4）；錯誤訊息不得包含 `Authorization` header。
- 每案例最多 1 次 API call，整體 ≤3 次／run；不加重試風暴（client 內建 429 重試仍生效）。

## 風險與對策

| # | 風險 | 對策 |
|---|------|------|
| 1 | 有人把真 token 寫進 `.env.example` 或 commit | `.env.example` 只放空值；`.env` 已 gitignore；review 檢查 |
| 2 | token 過期導致測試紅 | 文件寫明 30 分鐘；測試失敗訊息提示「需重新取得 token」，不自動 refresh |
| 3 | 誤跑到寫入端點 | 案例只允許 GET；code review 檢查 |
| 4 | 吃掉配額或 429 | 每 run ≤3 calls；僅手動執行，不進 CI／cron |
| 5 | `go test ./...` 因缺 env 變紅 | helper 一律 `t.Skip()`，不用 `t.Fatal` |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准 | 照計畫開工（pkg/xero 三案例 + .env.example + dev-setup 文件） |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer | 通過；唯一 Low：`" "` 空白 token 不 skip | 任務 8 已修（`strings.TrimSpace`，全空白 env 實測 SKIP） |

## 備註

- 若之後要 CI 化，建議改用「測試專用 Xero app + 測試 DB 存 refresh token」方案，不在本計畫範圍。
- 與 `doc/plans/2026-11-16-bootstrap.md` 任務 11（測試工具）獨立，可並行。
- 排程順序：等 9e 複審完成後再開工，避免搶同一批未 commit 檔案。
