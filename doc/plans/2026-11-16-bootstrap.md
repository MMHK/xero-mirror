# xero-mirror 建置計畫（bootstrap）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-16
**最後更新**: 2026-11-17
**狀態**: 🟢 完成（待 commit；任務 1–14 ✅、任務 15 ⏭️）
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
| 2026-11-16 | **Webhook buffer + batch get**：收集多個 entry IDs，去重後批量抓取，節省 API 配額 | `doc/spec.md` §5、§7.0、`doc/modules/webhook-buffer.md`、任務 9 |
| 2026-11-17 | **確認純唯讀同步**：本服務不發 `PUT`／`POST`／`DELETE` 給 Xero，只做 GET 鏡射，因此不會觸發自身 webhook，也無需迴圈防護 | 任務 9a–9e；風險表無需新增 webhook 迴圈項 |
| 2026-11-17 | **buffer 前置作業**：`entitySpec` 加 `supportsBatchIDs`、`.env.example` 加 buffer 變數、spec §7.0 落地；`pkg/config` 讀取留到任務 9c 一起做 | `pkg/sync/sync.go`、`.env.example`、`doc/spec.md`、任務 9b／9c |
| 2026-11-17 | **webhook 去重鍵**：Xero event 無 `eventId`，改 `resourceId + "_" + eventDate(UnixMilli)`；跨 tenant 不加維度（GUID 全域唯一）；入列失敗回 500 讓 Xero 重送（spec §7 明文例外） | `pkg/handler/webhook.go`、`doc/spec.md` §7、任務 9a |
| 2026-11-17 | **sub agent 模型**：改用 `coding-plan/ark-code-latest`（原 general／code-reviewer 模型回報不支援 function calling） | 派工流程（不影響程式） |
| 2026-11-17 | **buffer 範圍**：以 (tenantID, entity) 分組；只對 `supportsBatchIDs` 實體呼叫 `RunBatch`；CreditNotes／Payments 只記 log 交 cron 增量（不做逐個 fallback）；webhook 批量成功會把 `sync_state.status` 寫回 `idle`，目前無 `failed` 寫入路徑，暫不加 `ApplyBatch` 選項 | `pkg/handler`、`doc/modules/webhook-buffer.md`、任務 9c |

## 架構決策（對齊 AGENTS.md）

| 主題 | 決策 | 依據 |
|------|------|------|
| 入口 | `main.go`（≤80 行）負責 config 載入、分派、graceful shutdown；`commands.go`（同 package main）放三個 `run*` 接線；`flag` 自管參數 | §2、禁 cobra/viper |
| 單一同步核心 | `sync.Run(ctx, tenantID, entity, Window)`；`Window` 只有 Cursor 或 MonthStart/MonthEnd，內部流程相同 | §1.3 |
| 批量同步 | `sync.RunBatch(ctx, tenantID, entity, ids)`；用於 webhook buffer flush，只支援有 `IDs` 參數的端點 | §5、§7.0 |
| 資料流 | `webhook → webhook_events/sync_jobs 入列 → buffer 收集 → 同步核心`；`cron → 同步核心`；`同步核心 → pkg/xero → pkg/store` | §1.3、§5、§7.0 |
| Webhook buffer | 收集多個 entry IDs，去重後批量抓取；timer 到期或 batch size 觸發 flush；不支援批量 IDs 的實體只記 log 交 cron 增量（不做逐個 fallback） | §7.0、`doc/modules/webhook-buffer.md` |
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
| 9a | `pkg/handler` webhook 基礎：驗簽（raw body + HMAC-SHA256 + constant-time）、去重入列（`webhook_events` + `sync_jobs`）、`POST /webhook` 路由 | ✅ 完成 | general（ark-code-latest） | 兩輪 review；去重鍵 `resourceId_eventDateUnixMilli`；DB 測試以 `GET_LOCK('xero-mirror:test')` 跨 package 序列化；spec §7 已補 500／400／去重鍵 |
| 9b | `pkg/sync` 批量同步：`Runner.RunBatch(ctx, tenantID, entity, ids)`；支援 `?IDs=uuid1,uuid2,...` 的端點（Invoices、Contacts） | ✅ 完成 | general（ark-code-latest） | review ✅；去重保序、每批 30、`trackCursor=false`（不推進 cursor）、全部成功才一次 `ApplyBatch`；3 測試綠 |
| 9c | `pkg/handler` webhook buffer：收集 entry IDs、去重、timer 到期或 batch size 觸發 flush、呼叫 `sync.RunBatch` | ✅ 完成 | general（ark-code-latest） | 兩輪 review（修 `shutdownCtx` data race）；`SupportsBatchIDs`、config 新變數、`-race -count=10` 綠；模組 doc 已同步 |
| 9d | `pkg/handler` health + admin：`GET /health`（DB ping）、`POST /admin/sync`（手動觸發）；路由表面照 spec §7.1，其他方法 405 | ✅ 完成 | general（ark-code-latest） | review ✅ 無阻塞；`NewMux`、entity 別名表、UTC 半開月視窗、`ErrNoRows` 視為全量；spec 已補 admin 部署邊界 |
| 9e | `pkg/handler` cron + graceful shutdown：`robfig/cron` 註冊增量 sync job、`http.Server.Shutdown` 等待當前 sync 收尾、buffer flush 剩餘 entries | ✅ 完成 | general（ark-code-latest） | 兩輪 review；ctx 生命週期修正（`cronCtx`／`bufCtx` 獨立、shutdown 30s 上限、`SkipIfStillRunning`）；煙霧 exit 0 |
| 10 | `auth` subcommand：一次性授權 `localhost:{3000-9000}/callback`，token 加密落地 MySQL | ✅ 完成 | general（deepseek-flash） | 合併審查通過；state 驗證、10 分鐘上限、密文落地；重構與多 tenant refresh 另列任務 14／15 |
| 14 | `pkg/xero/auth.go` 重構：把 `runAuth` 內授權流程（state、臨時 callback server、tenant 寫入）搬離 `main`，補拒絕／取消／多 tenant 測試 | ✅ 完成 | general（deepseek-flash） | 5 個測試、timeout 切分、多 tenant 部分寫入訊息；`commands.go` 只剩接線 |
| 15 | 多 tenant refresh token 輪換：同一 refresh token 更新所有 tenant，或改 user 級單飛鎖 | ⏭️ 跳過 | — | 使用者確認**單一 tenant**；改在 spec §6 註明限制即可，不做程式改動 |
| 11 | 測試與工具：`tests/testing.go`、`docker-compose.test.yml`、必測三項（冪等、失敗後 cursor 可續跑、webhook 簽章 401） | ✅ 完成 | general（deepseek-flash） | 4 份 lock helper 收斂完成；新增 fetch 失敗 cursor 不動測試；`-race` 全綠 |
| 12 | `doc/modules/*.md` 與 `doc/spec.md` 同步；`go fmt` + `go vet` + `go test ./...` 收尾 | ✅ 完成 | general（deepseek-flash） | 新增 `doc/modules/sync.md`；spec §2 目錄、§3 資料流／規則、§9 指令與 sync 語意、§10 測試 DSN 已同步；計畫收尾 |
| 13 | `commands.go` `runSync` 接線：`--month YYYY-MM` 回補或依 cursor 增量，逐 tenant 跑五實體 | ✅ 完成 | general（deepseek-flash） | review 後已修：runCronSync 收斂、tenant 級失敗計數、`_test` 守門、`DeleteTenant` cleanup、`ListTenants` 只列 active；測試 3 次全綠 |

**依賴順序**：1 → 2 → 3 → 4 → (5、6) → 7 → 8 → 9a → 9b → 9c → 9d → 9e → 10 → 11 → 12；任務 13（`runSync` 接線）可在 9e 後並行。一次只派一個邏輯任務，完成即送 `code-reviewer` 審查。

## 目前 repo 狀態（2026-11-17 收尾）

已 commit（HEAD `00826a2 chore: local dev docker compose 環境`）：任務 1–8 + 任務 9a（webhook 驗簽／入列）+ dev 環境（`docker-compose.yml`、`scripts/init-dev.sql`、`doc/dev-setup.md`）。

> 註：`89527f0` 的 commit 訊息寫「webhook buffer 與 handler」，實際僅 handler；buffer 屬任務 9c，訊息與內容不符，後續以計畫為準（不改寫歷史）。

工作區未 commit（待提交）：任務 9b–9e、10、11、13、14 的程式與測試，加上任務 12 的文件收尾。`git status --short` 主要項目：

```text
 M .env.example, commands.go, go.mod, go.sum
 M doc/spec.md, doc/dev-setup.md, doc/modules/webhook-buffer.md, doc/plans/2026-11-16-bootstrap.md
 M pkg/config/config.go, pkg/config/config_test.go
 M pkg/handler/webhook.go, pkg/handler/webhook_test.go
 M pkg/store/queries.go, pkg/store/queries_test.go, pkg/store/store.go, pkg/store/migrate_test.go
 M pkg/sync/sync.go, pkg/sync/sync_test.go
?? docker-compose.test.yml, tests/
?? pkg/handler/{admin.go, buffer.go, buffer_test.go, health.go, mux.go, route_test.go}
?? pkg/xero/{auth.go, auth_test.go, live_test.go}
?? doc/plans/2026-11-17-live-xero-tests.md, doc/modules/sync.md
```

模組現況：

| 檔案 | 狀態 |
|------|------|
| `main.go` | ✅ 約 58 行（入口 + 分派 + usage） |
| `commands.go` | ✅ 約 262 行（`runSync`／`runServe`／`runAuth` 全部接線；cron、`SkipIfStillRunning`、graceful shutdown 已落地） |
| `pkg/config/config.go` | ✅ 必填驗證；已讀 `XERO_DAILY_CALL_LIMIT`／`WEBHOOK_BUFFER_SIZE`／`WEBHOOK_BUFFER_DELAY` |
| `pkg/xero/*` | ✅ OAuth2／`Authorize`／AES-256-GCM／單飛 token／client；live 測試未設 env 全 skip |
| `pkg/store/*` + `migrations/`（9 組） | ✅ 手寫 SQL 存取層、`Batch`／`ApplyBatch`（資料 + cursor 同交易）、`Migrate`、webhook 去重與 `sync_jobs` 入列 |
| `pkg/sync/*` | ✅ `Run`／`RunBatch`／`RunIncremental`／`RunTenant`／`RunAllIncremental`／`RunAllMonth`／`Entities`／`SupportsBatchIDs` |
| `pkg/handler/*` | ✅ webhook 驗簽入列、buffer（batch／timer／shutdown flush）、health、admin、mux |
| `tests/testing.go` | ✅ `RequireTestDatabase`／`LockTestDatabase`／`OpenTestDB`；未設 `TEST_MYSQL_DSN` 即 skip |
| `doc/` | ✅ `spec.md`、`dev-setup.md`、`modules/{webhook-buffer,sync}.md`、`plans/` ×2 |

任務 1–14 全部 ✅，任務 15（多 tenant refresh 輪換）⏭️ 跳過（使用者確認單一 tenant，限制已寫入 spec §6）。

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
| 10 | webhook buffer 記憶體遺失（服務重啟） | buffer 中的 events 已寫入 `sync_jobs`（持久化），重啟後 worker 可繼續處理；接受 buffer 遺失 |
| 11 | batch IDs 超過 URL 長度限制 | `batchSize` 預設 30（留 buffer）；`RunBatch` 內部可再分批（每批最多 30 個 IDs） |
| 12 | 不支援批量 IDs 的端點（CreditNotes、Payments）浪費 API 配額 | 只記 log、交 cron 增量（不做逐個抓取）；webhook 事件仍寫入 `sync_jobs` 作待辦紀錄 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-16 | ✅ 已批准 | 12 個任務；同步範圍五個實體 |
| 2026-11-16 | 🔄 要求修改 | 移除 sqlc／goose（改手寫 SQL） |
| 2026-11-16 | 🔄 要求修改 | migration 改 `//go:embed` 內嵌 + 啟動自動套用 |
| 2026-11-16 | ⏸ 暫停 | 中止所有 sub agent，等待續行指示 |
| 2026-11-17 | ▶️ 續行 | 同步計畫文件後續做任務 9a；確認純唯讀同步（不寫回 Xero） |

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
| 2026-11-17 | code-reviewer（任務 9a） | 4 阻塞（去重未被測試證明、DB 測試跨 package 互踩、500 與 spec「先回 200」衝突、去重鍵與 spec 不一致）；Low 若干（空 tenantId／resourceId、reason 長度、未支援 category 無 log、405 缺 Allow） | 已派工修正：去重鍵改 `resourceId+eventDate` 毫秒、補 tenant 精準查詢與事件數斷言、DB 測試加 `GET_LOCK` 序列化、spec §7 補 500／去重鍵／400 |
| 2026-11-17 | code-reviewer（任務 9a 複審） | 4 阻塞全數確認修復；另有 4 項 Low（spec §5 資料流殘留 eventId 文案、`event_id` 長度前提未註記、`lockTestDatabase` 重複、新繞行路徑缺斷言） | spec 已改 `resourceId+eventDate` 並註記長度前提；後 2 項留任務 11；任務 9a 標 ✅ |
| 2026-11-17 | code-reviewer（任務 9b） | 無阻塞；4 項低（webhook 批量成功會把 `sync_state.status` 蓋回 `idle`、log 記原始 ids 長度、全空白 id 未過濾、缺「中途失敗不寫入」整合測試） | 任務 9b 標 ✅；status 覆寫列入 9c 決策（現階段無 `failed` 寫入路徑，暫不改碼）；其餘留 9c／11 順手處理 |
| 2026-11-17 | code-reviewer（任務 9c） | 1 阻塞（`shutdownCtx` data race，實機重現）、2 Medium（spec §7.0 舊設計、模組 doc 自相矛盾）、Low（無用 shutdown chan、Handle 41 行、buffer 測試缺口） | 已修：改唯讀 `baseCtx`、Shutdown ctx 只等待、移除死欄位、Handle 抽 helper、spec／模組 doc 同步 |
| 2026-11-17 | code-reviewer（任務 9c 複審） | 3 項確認修復；race 以 8×200 併發壓力測試驗證；4 項非阻塞（spec §3 RunBatch 舊文案、plan 術語、矩陣註記、`drainTimer`） | 前 3 項已修；`drainTimer` 留 9e；任務 9c 標 ✅ |
| 2026-11-17 | code-reviewer（任務 9d） | 無阻塞；Low（admin 無認證需文件註記、缺 503／404／month 400 等測試）、Info（admin 無單飛／節流） | spec §7.1 已補 admin 部署邊界；測試缺口留任務 11；單飛評估留 9e；任務 9d 標 ✅ |
| 2026-11-17 | code-reviewer（任務 9e） | 3 阻塞（`shutdown(Background)` 無限等待、cron job 不可取消與 `db.Close()` 競爭、buffer 最後 flush 用已取消 signal ctx）、Medium（cron 不防重疊）、Low 4 項 | 已修：雙獨立 ctx、30s 總上限、`SkipIfStillRunning`、spec §9 同步；Low 留任務 11/12 |
| 2026-11-17 | code-reviewer（任務 9e 複審） | 3 阻塞與 M1 確認修復、無回歸；煙霧 exit 0、關機順序正確；2 項非阻塞（總預算語意、缺 shutdown 單元測試） | 任務 9e 標 ✅；測試留任務 11 |
| 2026-11-17 | code-reviewer（任務 13 與 10 合併） | 無阻塞；2 Medium（多 tenant 共用 refresh token 輪換缺口、auth 流程放 `main` 違反 §2 分層）、Low（authCtx 範圍、部分寫入訊息、測試缺口、測試殘留、lock helper 四份、`errors.New`） | 13／10 標 ✅；分層重構列任務 14、多 tenant refresh 列任務 15（待確認 tenant 數量）；其餘併任務 11/14 清理 |

## 未決事項

1. tenant 數量：✅ 已確認**單一 tenant**（表仍按多 tenant 設計；多 tenant refresh 輪換不支援，見任務 15）。
2. MySQL 來源：✅ 已解決，用 `docker compose` 啟動 MySQL 8.4（port 3307，見 `doc/dev-setup.md`）。
3. `auth` subcommand 是否必要（是否已有可匯入的 token）。
4. `serve` 部署位置（本機／container／雲），影響 cron 與 shutdown。
5. webhook 註冊：專案內實作 `PUT /webhooks` 一次性指令，或人工在 Xero 後台設定。

## 備註

- 任務 2 續行方式：先 review 現有產出（修 `sqlc.yaml`、`go.mod` 版本），補完驗收再標 ✅，避免重跑整個 scaffold。
- 派工時一併附：檔案路徑、預期行為、`AGENTS.md` 約束、驗收指令。
- 未決事項有變動 → 先更新本計畫再開工。
- **2026-11-17 暫停點**：任務 9a–9e 已完成並複審通過；sub agent 模型（`coding-plan`）5 小時配額用盡，14:31（+08）重置後續做任務 13 → 10 → 11 → 12。工作區大量未 commit 變更，暫停前建議先 commit。
- **已知限制（後續任務）**：
  1. `sync_jobs` 目前**沒有 consumer**：webhook 事件只作待辦／審計，實際資料由 cron／`sync` 增量收斂；若需「重啟後補做」，要另外實作 job worker。
  2. `webhook_events` 無清理機制，長期會持續成長；建議加保留期清理（例如 cron 每日刪 30 天前資料）。
  3. `tests/testing.go` 的 `OpenTestDB` 目前無人使用，可保留或任務 16 移除。

