# sync 子命令支援 --entity 參數

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**最後更新**: 2026-11-17
**狀態**: 🟢 完成（待 commit；任務 1–7 ✅）
**優先級**: Medium

## 背景與目標

目前 `sync` 子命令一次跑完 `Entities()` 的五個實體（Invoices → CreditNotes → Payments → Contacts → Accounts）。實測時若只想補單一實體（例如 Payments），也只能等前面實體跑完，且 `--month` 對 CreditNotes／Payments／Contacts／Accounts 不具篩選效果（`monthFilter=false`，等同全歷史重抓），造成長時間等待與不必要的 API 配額消耗。

目標：新增 `--entity` 參數，讓 `sync` 可以只同步指定實體；不指定時行為與現在完全相同（向後相容）。

**非目標**

- 不改 `--month` 對各實體的篩選能力（`monthFilter`）。
- 不動 webhook buffer、cron、admin 的既有行為（admin 僅改為共用同一份 entity 名稱解析）。
- 不新增 subcommand、不引入新依賴。

## 設計

### 1. entity 名稱解析集中到 `pkg/sync`

新增 exported `ParseEntity` 與別名表（大小寫不敏感，含單數別名）：

```go
// pkg/sync/sync.go（或獨立 entity.go，視行數而定）
func ParseEntity(raw string) (Entity, bool)
```

別名至少涵蓋現有 admin 的集合，並補齊單數：`contact`、`account`、`invoice`、`creditnote`／`creditnotes`／`credit_notes`、`payment`／`payments`。

`pkg/handler/admin.go` 的 `adminEntityAliases` 與 `parseAdminEntity` 改為呼叫 `pkgsync.ParseEntity`，刪掉重複對照表，避免兩份名稱清單日後漂移。

### 2. `pkg/sync` 新增單一實體的編排方法

沿用既有命名與失敗語意，不新增分層：

```go
// 全部 active tenant × 單一實體增量
func (r *Runner) RunEntityIncremental(ctx context.Context, entity Entity) error

// 全部 active tenant × 單一實體月度視窗
func (r *Runner) RunEntityMonth(ctx context.Context, start, end time.Time, entity Entity) error
```

實作方式：把 `RunAllIncremental`／`RunAllMonth` 的 tenant 迴圈抽成私有 helper（接受 `entities []Entity`），兩個 `RunAll*` 傳 `Entities()`，兩個 `RunEntity*` 傳 `[]Entity{entity}`。失敗計數與 `slog.Warn` 續跑語意維持不變；單一實體失敗即回 `同步失敗 N 個 tenant`。

### 3. CLI 接線（`commands.go`、`main.go`）

`sync` 新增 `--entity <名稱>`（單值，不做 CSV／可重複）：

```
sync [-month YYYY-MM] [-entity contacts|accounts|invoices|creditnotes|payments]
```

分派矩陣：

| `--month` | `--entity` | 呼叫 |
|-----------|-----------|------|
| 無 | 無 | `RunAllIncremental`（現況） |
| 無 | 有 | `RunEntityIncremental` |
| 有 | 無 | `RunAllMonth`（現況） |
| 有 | 有 | `RunEntityMonth` |

- `--entity` 值不合法 → 回 `參數錯誤`，不連線 DB、不發任何請求。
- `runSyncMode` 增加 entity 參數；完成的 log 帶 `entity` 欄位（未指定時省略）。
- 同步更新 `main.go` 的 `usageText` 與 `commands.go` 的 `syncUsageText`。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | `pkg/sync`：新增 `ParseEntity` 與 entity 別名表 | ✅ 完成 | general | 置於新檔 `pkg/sync/entity.go` |
| 2 | `pkg/sync`：抽出 tenant 迴圈 helper；新增 `RunEntityIncremental`／`RunEntityMonth` | ✅ 完成 | general | `runTenantEntities`／`runAllByEntities`／`runAllMonthByEntities` |
| 3 | `pkg/handler/admin.go`：改用 `pkgsync.ParseEntity`，移除重複別名表 | ✅ 完成 | general | 對外行為未變 |
| 4 | `commands.go`／`main.go`：新增 `--entity` flag、驗證與 usage 文案 | ✅ 完成 | general | 四種分派組合皆驗證 |
| 5 | 測試：`TestParseEntity`（表驅動）；`RunEntityIncremental`／`RunEntityMonth` 整合測試（無 `TEST_MYSQL_DSN` 時 skip） | ✅ 完成 | general | 每月度測試驗證 cursor 不推進 |
| 6 | 文件同步：`README.md`、`doc/spec.md` §9、`doc/modules/sync.md` | ✅ 完成 | general | 指令與方法表 |
| 7 | 驗證：`go fmt ./...`、`go vet ./...`、`go test ./...` | ✅ 完成 | general | 全綠（含測試 DB 整合案例） |

## 驗收方式

1. `go build -o xero-mirror.exe .` 成功。
2. `go run . sync -h` 顯示 `--entity` 說明。
3. `go run . sync --entity payments` 只出現 Payments 的同步 log／只寫 `xero_payments` 與對應 `sync_state`。
4. `go run . sync --entity nonsense` 在連 DB 前即回參數錯誤、exit 1。
5. 不帶 `--entity` 的 `sync` 與 `sync --month` 行為與改動前一致。
6. `go test ./...` 全綠；無 DB 時整合測試 SKIP 而非失敗。

## 風險與取捨

| # | 風險 | 對策 |
|---|------|------|
| 1 | `RunAll*` 重構造成失敗計數／續跑語意改變 | 私有 helper 只改「entities 來源」，維持原有 Warn 與回錯條件；既有測試不得修改 |
| 2 | admin 改用 `ParseEntity` 後別名集合改變 | 只增不減既有別名；既有 admin 測試需保持綠燈 |
| 3 | `--entity` 誤用名稱（如 `credit_note`） | 解析失敗即回錯並列出合法值 |
| 4 | 單一實體失敗就讓整個指令 exit 1 | 符合「該輪指定實體全失敗即失敗」；log 已含 tenant 與 entity |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准 | 使用者確認「確認並開始」，進入 DELEGATE |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer（第 1 輪） | W1：`sync --month` 完成 log 掉 `window`（行為回歸）；W2：端點測試未計數所有路徑；W3：缺 `RunEntityMonth` 整合測試；W4：端到端測試依賴 DB 全域乾淨；W5：usage 文案未說明可併用；W6：工作區混入 UI 變更（提交時需拆分）；W7：別名測試覆蓋不足 | W1–W5、W7 已修正，W6 留待 commit 時處理 |
| 2026-11-17 | code-reviewer（第 2 輪） | 結論「可合併」；T1：`RunEntityMonth` 測試用空回應抓不到 cursor 回歸；T2：缺 `creditnotes`／`invoice` 別名案例 | T1 改為回一筆真實 Payment 並斷言 `CursorTime` 為 nil；T2 已補齊，兩者皆 PASS |

## 備註

- 動機來自實測：`sync --month 2026-11` 在 Invoices 完成後，CreditNotes 需重抓全歷史（約 1.3 萬筆），且多個 sync／serve 併發時 429 退避使等待更久。
- `--entity` 只解決「只跑指定實體」；`--month` 對 CN／Payments 仍為全量，若後續要做月篩選，另開計畫。
