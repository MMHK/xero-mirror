# xero-mirror 建置計畫（bootstrap）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-16
**最後更新**: 2026-11-16
**狀態**: 🟡 已批准、暫停中（使用者中止所有 sub agent，等待續行指示）
**優先級**: High

## 背景與目標

把 Xero Accounting 資料鏡射到本地 MySQL 的單一 Go 服務。本計畫負責從 scaffold 建到可運作的第一版：

1. 同步五個實體：Invoices、CreditNotes、Payments、Contacts、Accounts。
2. 支援四種觸發：cursor 增量 sync、按月視窗回補 sync、cron 排程、Xero webhook 批次 sync。
3. 四者共用**同一套** fetch → upsert 核心；webhook handler 只決定「要重同步什麼」。
4. token 以 AES-256-GCM 密文落地 MySQL；refresh 輪換值與新 access token 同交易寫回。

## 範圍與非目標

**範圍**

- 單一 binary（root `main.go` + stdlib `flag` subcommand：`sync`、`serve`、`auth`）。
- 只允許 `pkg/config`、`pkg/xero`、`pkg/store`、`pkg/sync`、`pkg/handler` 五個模組。
- Schema 內嵌於 `pkg/store/migrations/*.sql`，啟動時自動套用；SQL 全部手寫在 `pkg/store`。
- webhook 驗簽／去重／入列、admin 手動觸發、health 檢查。
- 必要文件：`doc/spec.md`、`doc/modules/*.md`。

**非目標（本輪不做）**

- 不做雙向寫回 Xero（只做 mirror，Xero 是唯一來源）。
- 不做 UI／dashboard／報表。
- 不做跨 process 分散式鎖（單實例部署；per-tenant refresh 用 process 內單飛）。
- 不引入 `AGENTS.md` §3 白名單外的依賴。

## 決策沿革（會影響舊計畫內容，勿誤讀）

| 日期 | 決策 | 影響範圍 |
|------|------|----------|
| 2026-11-16 | 初始批准：12 個任務、五個同步實體 | 全計畫 |
| 2026-11-16 | **移除 sqlc 與 goose**，改手寫 SQL + 純 DDL 檔 | `AGENTS.md` §0／§2／§3／§6／§10／§11、`doc/spec.md`、任務 2～7 |
| 2026-11-16 | **migration 改 `//go:embed` 內嵌、啟動時自動套用**（自寫 `store.Migrate`，不引入工具） | `AGENTS.md` §2／§3／§6／§10、`doc/spec.md` §2／§4.1／§9、任務 3 |
| 2026-11-16 | **允許 root 的 `commands.go`**（同 `package main`）承接 `runSync`／`runServe`／`runAuth`；`main.go` 的 ≤80 行只約束入口檔 | `AGENTS.md` §2、`doc/spec.md` §2、任務 9／10 |

## 架構決策（對齊 AGENTS.md）

| 主題 | 決策 | 依據 |
|------|------|------|
| 入口 | `main.go`（≤80 行）負責 config 載入、分派、graceful shutdown；`commands.go`（同 package main）放三個 `run*` 接線；`flag` 自管參數 | §2、禁 cobra/viper |
| 單一同步核心 | `sync.Run(ctx, tenantID, entity, Window)`；`Window` 只有 Cursor 或 MonthStart/MonthEnd，內部流程相同 | §1.3 |
| 資料流 | `webhook → webhook_events/sync_jobs 入列 → 同步核心`；`cron → 同步核心`；`同步核心 → pkg/xero → pkg/store` | §1.3、§5 |
| 增量水位 | `sync_state(tenant_id, entity).cursor_time`，與資料 upsert **同一交易**提交 | §6 |
| 限流 | `x/time/rate` per-tenant limiter（5 併發 / ~60 次分 / 每日上限）；429 讀 `Retry-After` | §5 |
| 金額 | 全程 `string`，DB `DECIMAL(18,4)`，禁 `float64` | §4、§6 |
| 時間 | `time.Time` UTC 入庫；`XeroTime` 顯式解析 `/Date(ms+offset)/` 與 ISO8601 | §4 |
| token | DB 存 AES-256-GCM 密文（版本號 + 隨機 nonce），金鑰 `XERO_TOKEN_ENC_KEY` | §5 |
| refresh | per-tenant `sync.Mutex` 單飛；新 access + 新 refresh 同交易寫回 | §5 |
| Migration | 自寫 `store.Migrate`：`//go:embed all:migrations`、`schema_migrations` 版本表、`GET_LOCK` 併發鎖、冪等；不引入 sqlc／goose／ORM | §3、§6、§10 |
| 軟刪除 | 照實鏡射 `STATUS`（`DELETED`/`ARCHIVED`），不因缺席刪本地列 | §5 |

## 資料表草案（每張表一支 migration，最終以實作為準）

| 表 | 主鍵 | 重點欄位 |
|----|------|----------|
| `xero_tenants` | `tenant_id` | `tenant_name`、`access_token_enc`、`refresh_token_enc`、`token_expiry`、`scopes`、`auth_status`（`active`/`auth_required`）、`updated_at` |
| `sync_state` | `(tenant_id, entity)` | `cursor_time`、`last_run_at`、`status`（`idle`/`running`/`failed`/`auth_required`）、`last_error` |
| `webhook_events` | `event_id` | `tenant_id`、`entity`、`xero_id`、`event_date`、`received_at`、`processed_at` |
| `sync_jobs` | `id` | `tenant_id`、`entity`、`xero_id`、`reason`、`status`（`pending`/`running`/`done`/`failed`）、`created_at`、`finished_at`；唯一鍵 `(tenant_id, entity, xero_id)` |
| `xero_contacts` | `(tenant_id, contact_id)` | `name`、`status`、`xero_updated_at`、`synced_at` |
| `xero_accounts` | `(tenant_id, account_id)` | `code`、`name`、`type`、`status`、`xero_updated_at`、`synced_at` |
| `xero_invoices` | `(tenant_id, invoice_id)` | `type`、`number`、`contact_id`、`status`、`date`、`due_date`、`total`、`amount_due`（DECIMAL）、`currency`、`xero_updated_at`、`synced_at` |
| `xero_credit_notes` | `(tenant_id, credit_note_id)` | 同 invoice 結構加 `remaining_credit`、`amount_due` |
| `xero_payments` | `(tenant_id, payment_id)` | `invoice_number`、`contact_id`、`amount`、`date`、`status`、`xero_updated_at`、`synced_at` |

> 系統表 `schema_migrations(version varchar(64) PK, applied_at DATETIME(6))` 由 `store.Migrate` 建立與維護，不算業務表。
> 每張實體表都要 `synced_at` + `xero_updated_at`（§6 強制）。所有表 `utf8mb4_unicode_ci`、`DATETIME(6)` UTC。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 建立 `doc/spec.md`（目錄結構、資料流、資料表、指令、環境變數、錯誤處理） | ✅ 完成 | general | 三輪 review 通過 |
| 2 | scaffold：`go.mod`、root `main.go`（`sync`/`serve`/`auth` 分派 + graceful shutdown）、`pkg/config`、`.env.example` | ✅ 完成 | general | 兩輪 review 通過；`main.go` 72 行、config 3 個測試綠燈 |
| 3 | `pkg/store/migrations/`（每表一支 up/down）＋ `pkg/store/migrate.go`（embed／版本表／`GET_LOCK`／冪等）＋ `runSync` 連線後呼叫 migrate | ✅ 完成 | general | 三輪 review；`applied=9 → 0` 冪等實測；鎖綁同一 `*sql.Conn` |
| 4 | `pkg/store` 手寫 SQL 存取層：token 雙欄位同交易 upsert、`sync_state` upsert、各實體 upsert、webhook 去重 insert | ✅ 完成 | general | review ✅；18 支方法、4 測試綠；7 項改善已套（`UpdateTenantTokens`、`commands.go`、測試庫守門、DSN 拒 `clientFoundRows`、金額正規化） |
| 5 | `pkg/xero` OAuth2：authorize URL、code exchange、refresh 輪換、AES-256-GCM、`/connections`、per-tenant 單飛 token 管理 | ✅ 完成 | general | 兩輪 review；5 測試綠；`invalid_grant` 落 `auth_required`、同交易寫回 |
| 6 | `pkg/xero` client：tenant header、分頁依端點能力、429/`Retry-After`、per-tenant limiter（5 併發 / ~60 次分 / 每日上限）、`XeroTime`、context + timeout | ✅ 完成 | general | review ✅；10 測試綠；`client.go` 333 行 |
| 7 | `pkg/store` 交易編排：`Batch` + `ApplyBatch`（資料 upsert 與 cursor 同交易） | ✅ 完成 | general | review ✅；7 個 store 測試綠（含 rollback 停在 T1） |
| 8 | `pkg/sync` 核心：`Runner.Run(ctx, tenantID, Entity, Window)` fetch → upsert；cursor 與月度視窗共用；冪等 | ✅ 完成 | general | 兩輪 review；8 個 sync 測試綠（含 DB 的 cursor 不倒退） |
| 9 | `pkg/handler` + `serve`：webhook 驗簽（raw body + HMAC-SHA256 + constant-time）、去重入列、health、admin 觸發、cron、graceful shutdown | ⬜ 待處理 | — | 401／400／200 語意；路由照 spec §7.1，其他方法 405；`runServe` 先在 `commands.go` 接 config + `store.OpenMigrated` |
| 10 | `auth` subcommand：一次性授權 `localhost:{3000-9000}/callback`，token 加密落地 MySQL | ⬜ 待處理 | — | 禁 `127.0.0.1`；`runAuth` 同樣先 config + `store.OpenMigrated` |
| 11 | 測試與工具：`tests/testing.go`、`docker-compose.test.yml`、必測三項（冪等、失敗後 cursor 可續跑、webhook 簽章 401） | ⬜ 待處理 | — | 無 DB 時 `t.Skip()` |
| 12 | `doc/modules/*.md` 與 `doc/spec.md` 同步；`go fmt` + `go vet` + `go test ./...` 收尾 | ⬜ 待處理 | — | §9、§14 |

**依賴順序**：1 → 2 → 3 → 4 → (5、6) → 7 → 8 → (9、10) → 11 → 12。一次只派一個邏輯任務，完成即送 `code-reviewer` 審查。

## 目前 repo 狀態（2026-11-16 暫停點）

| 檔案 | 狀態 |
|------|------|
| `AGENTS.md` | ✅ 已移除 sqlc／goose，改內嵌自動 migrate |
| `doc/spec.md` | ✅ 260 行，與 `AGENTS.md` 一致 |
| `doc/plans/bootstrap-plan.md` | ✅ 本文件 |
| `go.mod` / `go.sum` | ✅ `go 1.24.0`；`require` godotenv + testify；`go.sum` 存在 |
| `main.go` | ✅ 58 行（入口 + 分派 + usage） |
| `commands.go` | ✅ 53 行（`runSync`／`runServe`／`runAuth` 接線，後兩者仍 stub） |
| `pkg/config/config.go` | ✅ 驗證齊全（含 DSN 拒 `clientFoundRows`） |
| `pkg/xero/{oauth,crypto,connections,token}.go` | ✅ OAuth2 + AES-256-GCM + 單飛 token；5 測試綠 |
| `pkg/store/{store,models,queries,migrate}.go` + `migrations/` | ✅ 18 支存取方法、9 組 migration、5 個測試 |
| `.env.example` | ✅ 8 必填 + `XERO_DAILY_CALL_LIMIT`，繁中註解、無真值 |
| `sqlc.yaml` | ✅ 已刪除 |
| `tests/`、`pkg/{sync,handler}` | ❌ 尚未建立 |

全部未 commit、untracked。

## 風險與易踩雷點

| # | 風險 | 對策 |
|---|------|------|
| 1 | refresh token 輪換漏寫回 → 永久斷線 | 新 access + 新 refresh 同一交易；單飛鎖 |
| 2 | webhook 先解析 body 再驗簽 → 假造事件可入列 | 保留 raw body，先驗簽才 `json.Unmarshal` |
| 3 | 先做事再回 200 → Xero 逾時重送、webhook 被停用 | 驗簽後立即回 200，事件寫 DB 待辦 |
| 4 | cursor 與資料不同交易 → 失敗漏資料 | 同一 tx 提交 |
| 5 | 金額誤用 `float64`／時間未轉 UTC | 全程 `string` + `DECIMAL`；`XeroTime` 顯式解析 |
| 6 | 併發過高觸發 429 | per-tenant limiter；429 讀 `Retry-After` 退避 |
| 7 | 把 API 沒回傳當刪除 → 本地資料消失 | 照實鏡射 `STATUS`，不主動 delete |
| 8 | 啟動自動 migrate：多實例併發或 DDL 失敗造成半套用 | `GET_LOCK` 序列化；每支 migration 單一動作；失敗不寫版本、重跑即續 |
| 9 | 無版本表以外的部署紀錄 → 人工難查「跑到哪支」 | `schema_migrations` 即查詢入口；回滾需人工 `.down.sql` + 刪版本列 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-16 | ✅ 已批准 | 12 個任務；同步範圍五個實體 |
| 2026-11-16 | 🔄 要求修改 | 移除 sqlc／goose（改手寫 SQL） |
| 2026-11-16 | 🔄 要求修改 | migration 改 `//go:embed` 內嵌 + 啟動自動套用 |
| 2026-11-16 | ⏸ 暫停 | 中止所有 sub agent，等待續行指示 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-16 | code-reviewer（第 1 輪） | 9 項（Window 語意、分頁假設、缺路由表面 + 6 項 Low） | 全部修正 |
| 2026-11-16 | code-reviewer（第 2 輪） | 5 項（矩陣無規則、唯一鍵含可變欄位、計畫未同步、變數驗證矛盾、`auth_status` 權責） | 全部修正 |
| 2026-11-16 | code-reviewer（第 3 輪） | 無阻塞項 | 任務 1 標 ✅ |
| 2026-11-16 | code-reviewer（任務 2） | 12 項，3 項標為阻塞（`go.sum` 未 commit、`main.go` 行數餘裕、`go` 指令 1.26.4） | 7 項小修完成 |
| 2026-11-16 | code-reviewer（任務 2 複審） | 7 項小修全過、3 個偏離可接受 | 任務 2 標 ✅；`serve`/`auth` 的 config + migrate 接線延到任務 9/10 |
| 2026-11-16 | code-reviewer（任務 3） | 10 項，1 High（`GET_LOCK` 未綁同一連線，可能鎖洩漏） | 8 項修正完成 |
| 2026-11-16 | code-reviewer（任務 3 複審） | 8 項全過；另 3 項 Low／Info | 任務 3 標 ✅；Low 項留任務 4 順手修 |
| 2026-11-16 | code-reviewer（任務 4） | 7 項 non-blocking | 判 ✅ 通過；7 項改善完成 |
| 2026-11-16 | code-reviewer（任務 5） | 6 項 Medium/Low（`auth_required`、`NewCipher` 型別、零值 expiry、refresh 空值、`ConnectionsURL`、測試缺口） | 修正完成 |
| 2026-11-16 | code-reviewer（任務 5 複審） | 7 項全過 | 任務 5 標 ✅ |

## 未決事項

1. tenant 數量：單一 tenant 或多 tenant（表已按多 tenant 設計）。
2. MySQL 來源：本機既有實例，或補 `docker-compose.yml`。
3. `auth` subcommand 是否必要（是否已有可匯入的 token）。
4. `serve` 部署位置（本機／container／雲），影響 cron 與 shutdown。
5. webhook 註冊：專案內實作 `PUT /webhooks` 一次性指令，或人工在 Xero 後台設定。

## 備註

- 任務 2 續行方式：先 review 現有產出（修 `sqlc.yaml`、`go.mod` 版本），補完驗收再標 ✅，避免重跑整個 scaffold。
- 派工時一併附：檔案路徑、預期行為、`AGENTS.md` 約束、驗收指令。
- 未決事項有變動 → 先更新本計畫再開工。

