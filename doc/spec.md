# xero-mirror 規格（spec）

> 本文件是 `AGENTS.md` 的落地版規格；衝突時一律以 `AGENTS.md` 為準。開工前必讀，行為／介面變更後必須同步更新本文件。

**建立日期**: 2026-11-16 ｜ **狀態**: 🟢 已建立（bootstrap 任務 1） ｜ **範圍**: 第一版 mirror 服務（單一 binary）

## 1. 專案定位

`xero-mirror` 是一個**單一 Go 服務**，把 Xero Accounting 資料**單向鏡射**到本地 MySQL。Xero 是唯一來源，本服務不寫回 Xero。

| 項目 | 內容 |
|------|------|
| 技術棧 | Go 1.24+、MySQL、`database/sql`（手寫 SQL，不用 ORM / codegen）、stdlib `log/slog`、`net/http` |
| 入口 | root `main.go`（≤ 80 行）：讀 config、subcommand 分派、graceful shutdown |
| 部署 | 單實例常駐；不引入跨 process 分散式鎖 |
| 非目標 | 雙向寫回 Xero、UI／報表、Items／TaxRates、`AGENTS.md` §3 白名單外的依賴 |

**四種觸發，一套核心**

| 觸發 | 用途 | 進入點 |
|------|------|--------|
| cursor 增量 | 日常增量，靠 `sync_state.cursor_time` 續跑 | `sync` subcommand／cron |
| 按月視窗回補 | 重跑或補歷史月份 | `sync --month YYYY-MM` |
| cron 排程 | `serve` 常駐時定期觸發增量 | `commands.go` 的 `runServe` 以 `SYNC_CRON` 註冊 `cron.AddFunc`；`runCronSync` 直接呼叫 `sync.Runner.RunAllIncremental`（`ListTenants` 只取 active），逐 tenant 對五個實體跑 `RunTenant` 增量 |
| webhook 批次 | 只重同步事件影響到的資源 | `POST /webhook` |

四者**共用同一套 fetch → upsert 核心**，不得各自實作抓取或寫入邏輯。

## 2. 目錄結構

```
.
├── main.go               # 入口（≤80 行）：config、subcommand 分派、graceful shutdown
├── commands.go           # subcommand 接線（同 package main）：runSync / runServe / runAuth
├── pkg/
│   ├── config/           # .env 載入、必填驗證、DSN 組裝
│   ├── xero/             # OAuth2、一次性授權流程、tenant header、分頁、429 重試、DTO、XeroTime
│   ├── store/            # repository：手寫 SQL（唯一碰 SQL 的層）＋內嵌 migration
│   │   └── migrations/   # 內嵌 DDL（NNN_name.up.sql / NNN_name.down.sql，經 //go:embed 打包）
│   ├── sync/             # 同步編排：cursor、月度視窗、冪等 upsert
│   └── handler/          # webhook、health、admin 觸發（cron 在 commands.go 註冊，同步邏輯一律轉 pkg/sync）
├── scripts/              # Docker 初始化腳本（init-dev.sql）
├── tests/                # 測試 helper：RequireTestDatabase／LockTestDatabase／OpenTestDB
├── doc/
│   ├── spec.md           # 架構與規格（必讀）
│   ├── dev-setup.md      # 本機開發環境（dev／測試 MySQL、live 測試）
│   ├── modules/          # 各模組設計（webhook-buffer.md、sync.md）
│   └── plans/            # 實作計畫
├── docker-compose.yml        # 本機 dev MySQL 8.4（host port 3307）
├── docker-compose.test.yml   # 測試 MySQL（host port 13315，拋棄式、無 volume）
├── README.md                 # 專案入口：快速啟動、指令、環境變數（使用者指定）
├── .env.example
└── go.mod
```

**硬性限制**

- `pkg/` 底下**只允許** `config`、`xero`、`store`、`sync`、`handler` 五個模組，不新增第六個；新檔案優先放進既有模組，不要塞 root。
- root 只允許 `main.go` 與 `commands.go`。`main.go` 的 ≤80 行只約束入口檔；subcommand 實作放 `commands.go`，且只做接線（config → DB → 呼叫 `pkg/`），業務邏輯一律在 `pkg/`。
- **禁止目錄**：`internal/`、`domain/`、`application/`、`infrastructure/`、`adapter/`、`lib/`、`common/`、`layers/`、`features/`、`cmd/`。
- subcommand 用 stdlib `flag` + `switch`，**禁** cobra／viper；文件只放 `doc/`（root `README.md` 為使用者要求的專案入口，不在此限），**禁** `docs/`、`architecture.md`、其他 README。
- `store` 是**唯一**碰 SQL 的層；`handler` 不寫同步邏輯，一律轉呼叫 `pkg/sync`。
- `pkg/store/migrations/*.sql` 以 `//go:embed all:migrations` 內嵌；`go:embed` 屬 stdlib，**不需新增依賴**，也不引入 migration 工具。

## 3. 資料流

```
Xero webhook ─► POST /webhook ─驗簽─┬ 簽章錯 → 401
                                    ├ body 不合法 → 400
                                    └ 正確 → 立即回 200（先回應、後處理）
                                          ▼
                     webhook_events（resourceId+eventDate 去重）＋ sync_jobs（待辦入列）
                                          ├─► WebhookBuffer ─► RunBatch（僅 Invoices／Contacts；不推進 cursor）
                                          └─► 其餘實體／失敗案例留 sync_jobs，交 cron 增量

cron／sync 手動／--month ─► Runner.RunAllIncremental／RunAllMonth
                                          ▼
                          Runner.Run(ctx, tenantID, entity, Window)
                                          ├─► pkg/xero（分頁、限流、429 退避、token refresh）
                                          ▼
                     pkg/store（每 chunk 一個交易：該 chunk 實體 upsert ＋ sync_state 水位）
                                          ▼
                                        MySQL
```

**分塊寫入**：`Runner.Run`／`RunBatch` 每累積 `writeChunkRows = 500` 列就 `ApplyBatch` 一次（該 chunk 不帶水位）；全部頁完成後再 flush 一次，僅此時帶入本輪水位。中途失敗可能留下先前的 chunk，但 `sync_state.cursor_time` 不動，重跑以冪等 upsert 覆蓋。

**同步核心契約**

```go
// 唯一 fetch → upsert 核心；Window 只決定時間範圍，流程完全相同。
func (r *Runner) Run(ctx context.Context, tenantID string, entity Entity, w Window) error

// 批量抓取多個 entry IDs（用於 webhook buffer flush）。
// 只支援有 IDs 參數的端點（Invoices、Contacts）；不支援的端點回錯誤，由 webhook buffer 交 cron 增量處理。
func (r *Runner) RunBatch(ctx context.Context, tenantID string, entity Entity, ids []string) error

// 全部 active tenant 的入口：sync（無 --month）與 cron 都走 RunAllIncremental，
// sync --month YYYY-MM 走 RunAllMonth；以 tenant 為失敗計數單位。
func (r *Runner) RunAllIncremental(ctx context.Context) error
func (r *Runner) RunAllMonth(ctx context.Context, start, end time.Time) error
```

| `Window` 種類 | 欄位 | 推算 Xero 查詢條件 |
|---------------|------|--------------------|
| Cursor | `Cursor *time.Time`（讀自 `sync_state.cursor_time`；`nil` 代表首跑全量） | 端點支援時用 `If-Modified-Since`（`http.TimeFormat`，GMT）；不支援則全量 |
| Month | `MonthStart` / `MonthEnd *time.Time`（該月 00:00 起、下月 00:00 止，UTC，半開區間；必須成對，且與 `Cursor` 互斥） | 端點支援日期篩選時以區間回補（`/Invoices` 用 `where=Date>=DateTime(y,m,d)&&Date<DateTime(...)`），配合冪等 upsert |
| Batch IDs | `ids []string`（由 webhook buffer 收集的去重 entry IDs） | `?IDs=uuid1,uuid2,...`（URL 長度限制約 40 個，超過需分批） |

**Window 退化規則**：月度視窗不是通用規則。`/Accounts`、`/Contacts` **不支援**日期區間過濾，對這兩者 Month 視窗退化為**全量 fetch + 冪等 upsert**，語意等同一次全量重跑（結果必須與全量 sync 相同）；日期欄位要用 `Date` 還是 `UpdatedDateUTC`，一律依端點能力決定，不得假設兩者皆可用。

**規則**

1. 只有 `sync` subcommand、cron 與 handler（webhook／admin）轉發時建立 `Window`；webhook **不得**自組 HTTP 請求或寫 SQL。
2. 單一 entity 失敗 → `slog.Warn` 降級，同輪其他 entity 繼續。
3. cursor 更新必須與該批資料 upsert **同一交易**提交，否則失敗後漏資料。
4. 冪等：同一批資料連跑兩次，DB 結果必須相同（`ON DUPLICATE KEY UPDATE`）。
5. **進入點固定**：`sync`（無 `--month`）與 cron 都走 `Runner.RunAllIncremental`；`sync --month YYYY-MM` 走 `Runner.RunAllMonth`。cursor 增量用 `Window{Cursor}`，月度回補用 `Window{MonthStart, MonthEnd}`（UTC 半開）。
6. **只有 `Cursor` 模式推進水位**：月度回補與 webhook batch 同步送 `Batch.Cursor = nil`，`upsertSyncStateSQL` 以 `IF(VALUES(cursor_time) IS NULL, cursor_time, ...)` 保留原 `cursor_time`，只更新 `last_run_at`／`status`；因此重跑歷史月份不會動到既有增量水位（既不前進也不回退），也不會清空。
7. **失敗語意**：單一 entity 失敗只 `slog.Warn` 續跑；`RunAll*` 以 tenant 為失敗單位，部分 tenant 失敗只 Warn 並在最後回 `同步失敗 N 個 tenant`——`sync` subcommand 以 exit 1 結束，cron 只記 Warn 不中斷排程。

## 4. 資料表與同步實體（本版五個）

Schema 唯一來源是 `pkg/store/migrations/` 的編號 DDL（`NNN_name.up.sql` 與成對的 `.down.sql`），以 `//go:embed all:migrations` 打包進 binary；`//go:embed` 屬 stdlib，不需新增依賴。SQL 全部手寫在 `pkg/store`，用 `?` placeholder、禁字串拼接，`pkg/store` 是唯一 SQL 層、其他套件不得出現 SQL 字串；禁止用 struct tag 當 schema。所有表：`utf8mb4_unicode_ci`、`DATETIME(6)` 存 UTC、金額 `DECIMAL(18,4)`、禁 `FLOAT`/`DOUBLE`。

| 表 | 主鍵 | Xero 端點 | 關鍵欄位 |
|----|------|-----------|----------|
| `schema_migrations` | `version`（`varchar(64)`） | — | `applied_at`（`DATETIME(6)`）；系統表，由 `store.Migrate` 建立與維護，**不算業務表** |
| `xero_tenants` | `tenant_id` | — | `tenant_name`、`access_token_enc`、`refresh_token_enc`、`token_expiry`、`scopes`、`auth_status`、`updated_at` |
| `sync_state` | `(tenant_id, entity)` | — | `cursor_time`、`last_run_at`、`status`、`last_error` |
| `webhook_events` | `event_id` | — | `tenant_id`、`entity`、`xero_id`、`event_date`、`received_at`、`processed_at` |
| `sync_jobs` | `id`（自增） | — | `tenant_id`、`entity`、`xero_id`、`reason`、`status`、`created_at`、`finished_at` |
| `xero_invoices` | `(tenant_id, invoice_id)` | `GET /Invoices` | `type`、`number`、`contact_id`、`status`、`date`、`due_date`、`total`、`amount_due`、`currency` |
| `xero_credit_notes` | `(tenant_id, credit_note_id)` | `GET /CreditNotes` | 同 invoices 結構，另加 `remaining_credit` |
| `xero_payments` | `(tenant_id, payment_id)` | `GET /Payments` | `invoice_number`、`contact_id`、`amount`、`date`、`status` |
| `xero_contacts` | `(tenant_id, contact_id)` | `GET /Contacts` | `name`、`status` |
| `xero_accounts` | `(tenant_id, account_id)` | `GET /Accounts` | `code`、`name`、`type`、`status` |

**強制**

- 每張實體表必含 `synced_at`（本地寫入時間）與 `xero_updated_at`（Xero `UpdatedDateUTC`）；上表為省版面未逐列列出。
- 金額保留 Xero 回傳字串，加總／比較用十進位字串或整數最小單位，**禁 `float64`**。
- 軟刪除只鏡射 `STATUS`（`DELETED` / `ARCHIVED`），**不得**因 API 沒回傳就 `DELETE` 本地列。
- `sync_state.status` 列舉值固定為 `idle` / `running` / `failed` / `auth_required`；`auth_required` 目前為**保留值**（未實作寫入），授權失效由 `xero_tenants.auth_status` 表達、`ListTenants` 只列 `active` 來跳過，**不得**把 refresh 回傳的 `invalid_grant` 直接寫進 entity 級狀態。
- `xero_tenants.auth_status` 為 `active` / `auth_required`：refresh 失敗（`invalid_grant`）時只在此標記需重新授權。
- **權責集中**（§4／§6 共用）：refresh 層**只**寫 `xero_tenants.auth_status`；`sync_state.status` 由同步編排讀 `auth_status` 後投影，任何層都不得直接把 `invalid_grant` 寫進 `sync_state`。
- `sync_jobs` 去重：唯一鍵 `(tenant_id, entity, xero_id)`（**不含**可變的 `status`），`status` 為可變欄位，入列時用 `ON DUPLICATE KEY UPDATE` 只刷新待辦狀態；`status` 列舉值固定為 `pending` / `running` / `done` / `failed`，webhook 重送不得產生重複 job。

### 4.1 Migration 自動套用

- **執行時機**：`store.Migrate(ctx, *sql.DB)` 在建立 MySQL 連線後、任何業務邏輯前執行。`sync`／`serve`／`auth` 三個 subcommand 都必須先 migrate；連不上 MySQL 時 `slog.Error` + `os.Exit(1)`，**不得半啟動**。
- **流程**：先確保 `schema_migrations` 存在 → `SELECT GET_LOCK('xero-mirror:migrate', n)` 取 advisory lock（避免多實例同時 migrate）→ 依檔名排序比對已套用版本 → 只跑未套用的 up 檔 → 成功後寫入版本列 → 最後 `RELEASE_LOCK`。全程綁同一條 `*sql.Conn`（`GET_LOCK`／`RELEASE_LOCK` 為 session 級，跨連線 release 會回 0 並讓鎖洩漏）。
- **註解限制**：不做完整 SQL parser，略過整行 `--` 註解；新註解必須獨占整行，不支援區塊註解與行內 `--`。
- **冪等**：空 DB 連跑兩次結果相同；第二次不重套任何已記錄版本。
- **語句切分**：SQL 檔以 `;` 結尾的語句分行執行，檔案內不得在字串中出現 `;`。
- **不可回滾**：MySQL DDL 隱式提交無法交易回滾，每支 migration 只做一件事（一張表或一組索引）；失敗時**不寫版本**，修好後重跑。若 DDL 已成功但版本列寫入失敗（斷線／逾時），錯誤訊息會要求人工確認後補 `INSERT schema_migrations`，或先執行該支 `.down.sql` 再重跑，避免重跑撞 `already exists`。
- **檔案配對**：每個 `.up.sql` 必須有對應 `.down.sql`，缺檔視為設定錯誤（啟動即報錯）；DB 已記錄但檔案已移除的版本只 `slog.Warn`，不阻擋啟動。
- **`.down.sql`** 只供人工回滾（執行後刪掉 `schema_migrations` 對應列），程式**不自動**執行 down。

## 5. Xero API 約定（強制）

| 項目 | 約定 |
|------|------|
| Base URL | `https://api.xero.com/api.xro/2.0` |
| `/connections` 例外 | 掛 `https://api.xero.com`，**不可**加 `/api.xro/2.0` |
| 必要 header | `Authorization: Bearer <accessToken>` ＋ `Xero-tenant-id: <tenantID>`；header 缺失先懷疑 tenant 綁定，不要直接判定 token 過期 |
| 分頁 | 依端點能力（見下表）；支援者 `page=1..N`、每頁 100，直到回傳 < 100 或空陣列；`/Accounts` 不分頁，單次回傳全部；一律不假設總頁數 |
| 限流 | per-tenant：5 併發、約 60 次/分（`golang.org/x/time/rate`），**另**有每日呼叫上限（值由 config 決定，不硬編碼）；**禁**全域 fan-out |
| 429 | 讀 `Retry-After` 退避重試；屬預期狀況，不算錯誤告警 |
| timeout | 對外請求一律帶 `context.Context` 與 timeout，禁無 timeout client |
| 增量 | 優先 `If-Modified-Since` 或 `where=UpdatedAtUTC>...`（支援度依端點，見下表），實際水位寫進 `sync_state.cursor_time` |
| 不支援增量 | 退回全量 + 冪等 upsert，不得重設或跳過水位 |
| 時間 | `UpdatedDateUTC` 可能為 `/Date(1234567890000+0000)/`，顯式解析毫秒 + 時區偏移，**禁** RFC3339 |
| JSON tag | Xero DTO 照原樣 PascalCase（`json:"InvoiceID"`），不統一成 `snake_case`；DTO 與 DB 模型分開定義 |

**端點能力矩陣**（2026-11 核對）

| 端點 | 分頁 | 增量／篩選方式 | 批量 IDs | 備註 |
|------|------|----------------|----------|------|
| `GET /Invoices` | `page=1..N`，每頁 100 | `If-Modified-Since`（cursor）；Month 用 `where=Date>=DateTime(YYYY,MM,DD)&&Date<DateTime(YYYY,MM,DD)` | ✅ `?IDs=uuid1,uuid2,...`（約 40 個/請求） | 主力實體 |
| `GET /CreditNotes` | `page=1..N`，每頁 100 | `If-Modified-Since`／`where`（`Date>` 支援，`>=` 不支援） | ❌ 不支援 | 無批量參數，只能逐個或全量；2026-11-17 官方文件核對，程式已啟用 `If-Modified-Since`；月度視窗 `monthFilter=false`，仍走全量 |
| `GET /Payments` | `page=1..N`，每頁 100 | `If-Modified-Since`；`where` 可用 OR 條件 | ❌ 不支援 `IDs`，可用 `where=PaymentID=guid("...") OR ...` | OR 條件有長度限制；2026-11-17 官方文件核對，程式已啟用 `If-Modified-Since`；月度視窗 `monthFilter=false`，仍走全量 |
| `GET /Contacts` | `page=1..N`，每頁 100 | **不支援**日期區間；`If-Modified-Since` 官方文件列支援（2026-11-17），程式已啟用 `If-Modified-Since` | ✅ `?IDs=uuid1,uuid2,...`（約 40 個/請求） | Month 視窗 → 全量 |
| `GET /Accounts` | 不分頁（單次回傳全部） | 全量 | ❌ 不支援 | Month 視窗 → 全量 |

**預設規則**：矩陣中標「未啟用」或尚未核對的能力，一律走全量 fetch + 冪等 upsert，**不得假設** `If-Modified-Since` 可用；核對結果須回寫本矩陣。

**月度回補**：`/Invoices` 使用 `where=Date>=DateTime(YYYY,MM,DD)&&Date<DateTime(YYYY,MM,DD)`（UTC 年月日、半開區間，不送 `from`／`to`）；`/Contacts`、`/Accounts` 等不支援日期篩選的端點退化為全量 fetch + 冪等 upsert。

**批量查詢**：`/Invoices` 與 `/Contacts` 支援 `IDs` 參數，可用於 webhook buffer 聚合後一次抓取多個 entry。`/CreditNotes` 與 `/Payments` 不支援批量 IDs，webhook 觸發時不逐個抓取，只記 log 並交 cron cursor 增量（事件已持久化於 `sync_jobs`，不會遺失）。URL 長度限制約 40 個 IDs，超過需分批。**本服務策略**：矩陣中「只能逐個或全量」「`where` 可用 OR」屬 Xero API 能力描述；webhook 路徑一律不採用逐個／OR 抓取。

本服務自己的端點只允許 `GET`／`POST`，其他方法回 `405`；此限制不適用於呼叫 Xero API（詳見 §7.1）。

## 6. OAuth2 與 token 生命週期

本專案是**服務**：Authorization Code + `offline_access`，**不要**照搬 `xero-cli` 的 PKCE 本機 CLI 流程。首次授權可用一次性 `auth` subcommand，但 token 必須落地 MySQL。授權流程實作在 `pkg/xero/auth.go` 的 `Authorize`（state、授權 URL 輸出、臨時 callback server、token 交換、逐 tenant 加密寫庫），root `commands.go` 的 `runAuth` 只做設定 → migration → 呼叫；等待 callback 的 10 分鐘上限不涵蓋寫庫，寫庫使用獨立 timeout。

| 項目 | 規格 |
|------|------|
| Scope | `openid profile email accounting.transactions accounting.settings accounting.contacts offline_access` |
| Redirect URI | `http://localhost:{port}/callback`（port 3000–9000）；**禁** `127.0.0.1`（註冊不匹配會直接失敗） |
| token 壽命 | access 約 30 分鐘；refresh 約 60 天且**每次 refresh 都輪換**，舊值立即作廢 |
| 寫回 | 新 access + 新 refresh **同一交易**寫回 `xero_tenants`；只更新 access 等於下次必斷線 |
| 併發 | 同 tenant refresh 序列化（process 內 `sync.Mutex` 單飛），避免兩個 token 都失效 |
| 加密 | DB 只存 AES-256-GCM 密文（版本號 + 隨機 nonce + ciphertext）；金鑰 `XERO_TOKEN_ENC_KEY`，**禁**硬編碼／明文存 token／寫進 log |

**單飛 refresh**：取鎖 → 重讀 DB token（避免吃到別人剛換好的值）→ access 未過期（留安全邊界）直接用 → 否則以 refresh 換新 token，同交易寫回 access + refresh + expiry → 解鎖。`invalid_grant` 視為需重新授權：`slog.Error` 並在 `xero_tenants` 標記 `auth_status = auth_required`（**不寫入** entity 級 `sync_state.status`），不無限重試；該 tenant 會因 `xero_tenants.auth_status = auth_required` 被 `ListTenants`（只列 `active`）排除於 cron 與手動同步之外，直到重新授權；`sync_state` 不會實際寫入 `auth_required`。權責與 §4 一致：refresh 只寫 `xero_tenants.auth_status`，任何層不得直接寫 `invalid_grant`。

**單一 tenant 假設**：本服務目前只接一個 Xero organisation。`auth` 寫入多筆 connection 時，各 tenant 會存入同一組 refresh token，但 refresh 輪換只回寫當前 tenant；多 tenant 情境下其餘 tenant 的 refresh token 會失效。若未來要支援多 tenant，需改為 user 級單飛並同步更新所有 tenant（計畫任務 15）。

## 7. Webhook 規則（強制）

| 情境 | 回應 |
|------|------|
| 簽章驗證失敗 | `401` |
| 簽章正確但 body 不合法 | `400` |
| 簽章正確且 body 合法 | `200`（**先回應，再入列處理**） |

驗簽失敗（`401`）時**不寫入** `webhook_events`／`sync_jobs`，不留任何待辦；只有簽章正確且 body 合法才入列。

**驗簽與入列**

1. 讀 **raw body**（`io.ReadAll`）；驗簽前**不得** `json.Unmarshal` 覆蓋或丟棄 raw bytes。
2. `HMAC-SHA256(XERO_WEBHOOK_KEY, rawBody)` → base64，與 `x-xero-signature` 以 **constant-time** 比較（`hmac.Equal`）。
3. 通過後才 `json.Unmarshal`；Xero event 無 `eventId`，去重鍵固定為 `resourceId` + `eventDate`（毫秒，格式 `resourceId_eventDateUnixMilli`），重複事件**不得**重複入列；跨 tenant 不加 tenant 維度（`resourceId` 為 Xero GUID，全域唯一）。`event_id` 長度：GUID 36 + `_` + 13 位毫秒 = 50，安全落在 `varchar(64)`；若日後 `xero_id` 改用非 GUID 格式，須先放寬欄位（新開 migration，不改舊檔）。
4. 立即回 200，再把事件寫入 `webhook_events` 與 `sync_jobs`；處理太慢會被 Xero 停用 webhook。
5. webhook 只決定「重同步什麼」（entity + xero_id），抓取與寫入一律呼叫 `sync.Run` 或 `sync.RunBatch`。
6. 任一 `eventDateUtc` 解析失敗 → **整批**回 `400`，不部分入列。
7. 入列失敗（`webhook_events`／`sync_jobs` 寫入錯誤）→ 回 `500`，刻意讓 Xero 重送以避免遺失；此為第 4 點「先回 200」的明文例外。

### 7.0 Webhook Buffer 與 Batch Get（節省 API 配額）

**問題**：同一個 entry（如 Invoice A）短時間內可能觸發多個 webhook event（建立、更新、付款）。如果每個 event 都立即呼叫 API，會浪費配額。

**解法**：收集多個 entry IDs，去重後批量抓取。

```
收到 event (inv-A) ─┐
收到 event (inv-B) ─┤
收到 event (inv-A) ─┼─► Buffer 收集 {inv-A, inv-B}（去重）
收到 event (inv-C) ─┘    │
                         ▼
                  達到 batch size 或 timer 到期
                         │
                         ▼
                  GET /Invoices?IDs=inv-A,inv-B（1 次 API）
                         │
                         ▼
                  upsert 全部資料
```

**Buffer 設計**

單一背景 goroutine（event loop）串行處理，所有 `pending` 狀態只在該 goroutine 記憶體取，不需額外 mutex 保護；HTTP handler 端只透過帶緩衝的 `events` channel 非阻塞派送。

```go
// pkg/handler/buffer.go
type WebhookBuffer struct {
    runner     *pkgsync.Runner
    batchSize  int
    flushDelay time.Duration

    baseCtx context.Context // 建構期設定後唯讀，loop 所有 flush 皆用它

    events    chan bufferItem // 容量 1024，滿了記警告後丟棄，不阻塞 HTTP handler
    done      chan struct{}
    closeOnce sync.Once
    mu        sync.Mutex
    closed    bool

    // (tenant, entity) → xero_id 集合（自動去重），僅 event loop 存取
    pending map[string]map[pkgsync.Entity]map[string]struct{}
    timer   *time.Timer
}
```

**觸發 flush 條件**（任一滿足即 flush，flush 一律用唯讀的 `baseCtx`）

1. **Timer 到期**：收到第一個 event 後啟動 timer（預設 3 秒），到期 flush
2. **Batch size**：單一 `(tenant, entity)` 群組累積達到 `batchSize`（預設 30），立即 flush 全部 pending
3. **Shutdown**：關閉 `events` channel，event loop 把剩餘 pending 做最後 flush 後結束；`Shutdown(ctx)` 的 ctx 只控制等待上限（逾時回 `ctx.Err()`）

**各端點策略**

| 端點 | 策略 | 說明 |
|------|------|------|
| `/Invoices` | ✅ Buffer + RunBatch | 支援 `IDs`，`GET /Invoices?IDs=uuid1,uuid2,...` |
| `/Contacts` | ✅ Buffer + RunBatch | 支援 `IDs`，`GET /Contacts?IDs=uuid1,uuid2,...` |
| `/CreditNotes` | ❌ 只記 log，交 cron 增量 | 不支援 `IDs`，buffer 不呼叫 API，事件已在 `sync_jobs` |
| `/Payments` | ❌ 只記 log，交 cron 增量 | 同上，不做逐個抓取或 `where=...OR...` |
| `/Accounts` | ❌ 全量 + 冪等 upsert | 不分頁、不支援 IDs，全量重跑由 cron 負責 |

**不支援批量 IDs 的端點**

`/CreditNotes` 與 `/Payments` 不支援 `IDs` 參數，實作採**單一策略：只記 `slog.Info`、不發任何 Xero 請求，交 cron cursor 增量收斂**。webhook 事件在 handler 端已持久化到 `webhook_events` 與 `sync_jobs`，不會因 buffer 不處理而遺失；亦**不做**逐個 `GET /CreditNotes/{id}` 或 `where=...OR...` fallback，避免在 flush 路徑對高頻事件逐個打 API、耗費配額。

**配置參數**

| 環境變數 | 說明 | 預設 |
|---------|------|------|
| `WEBHOOK_BUFFER_SIZE` | 單個 entity 的 batch size | 30 |
| `WEBHOOK_BUFFER_DELAY` | flush delay（毫秒） | 3000 |

### 7.1 HTTP 路由表面

| 方法 | 路徑 | 用途 | 契約 |
|------|------|------|------|
| `POST` | `/webhook` | Xero 回呼 | HMAC-SHA256 驗簽；成功 `200`、簽章錯 `401`、body 不合法 `400` |
| `GET` | `/health` | 健康檢查 | DB ping 正常 `200`（body `{"status":"ok"}`），異常 `503`（`{"status":"unhealthy"}`） |
| `POST` | `/admin/sync` | 手動觸發同步 | 參數 `entity` / `tenant` / `month`（query 或 form）；僅允許 `POST` |
| `GET` | `/` | 介紹頁 | 靜態 `text/html`；非 `/` 的未知路徑回 `404` |
| `GET` | `/docs` | Swagger UI | 靜態 `text/html`，由 CDN 載入 swagger-ui-dist 並讀 `/openapi.json` |
| `GET` | `/openapi.json` | OpenAPI 文件 | `application/json`；OpenAPI 3.0.3，契約與本節一致 |

- 本服務端點**只允許** `GET`／`POST`；其他方法（`PUT`／`PATCH`／`DELETE`）一律回 `405 Method Not Allowed`，並帶 `Allow` header 標示允許的方法；未知路徑回 `404`。
- `/health` 只做 `store.Ping`：成功回 JSON `{"status":"ok"}`，失敗先 `slog.Warn` 再回 `{"status":"unhealthy"}`。
- `/admin/sync` 參數規則：
  - `tenant`、`entity` 必填，缺一或 `entity` 未知 → `400`（JSON `{"error":"..."}`），驗證失敗前不得呼叫 `runner`。
  - `entity` 大小寫不敏感，接受 sync `Entity` 值與別名：`contacts`→`CONTACTS`、`accounts`→`ACCOUNTS`、`invoices`/`invoice`→`INVOICES`、`creditnotes`/`credit_notes`→`CREDIT_NOTES`、`payments`/`payment`→`PAYMENTS`。
  - `month` 選填，格式必須是 `YYYY-MM`，否則 `400`；有值時組該月 00:00 UTC 起、下月 00:00 止的半開月度視窗；省略時讀 `sync_state.cursor_time`，無紀錄（`sql.ErrNoRows`）視為首跑全量（cursor 為 `nil`）。
  - `runner.Run` 失敗回 `500`（`{"error":"同步失敗"}`）並 `slog.Error`；成功回 `{"status":"ok"}`。
- 此限制只針對本服務提供的端點；呼叫 Xero API 照 Xero 要求（例如註冊 webhook 是 `PUT /webhooks`）。
- 路由只做參數解析／驗證／回狀態碼，業務邏輯一律轉 `pkg/sync`。
- **部署邊界**：`/admin/sync` 無內建認證，只可部署於私有網路或反向代理後方（限制來源 IP 或加鑑層），不得直接暴露公網。
- 頁面路由（`/`、`/docs`、`/openapi.json`）一律只允許 `GET`，其他方法回 `405 Method Not Allowed` 並帶 `Allow: GET`。
- `GET /` 是 `ServeMux` 的 catch-all，handler 必須以 `r.URL.Path == "/"` 精確判斷；其餘未知路徑回 `404`，不得回介紹頁。
- 三個頁面路由由 `pkg/handler/page.go` 的 `PageHandler` 提供，資產以 `//go:embed`（`pkg/handler/home.html`、`docs.html`、`openapi.json`）打包進 binary，**不新增 Go 依賴**。
- `PageHandler` 先判斷方法再判斷路徑，故未註冊路徑的非 `GET` 請求回 `405`，`GET` 未註冊路徑回 `404`。
- Tailwind v4（`@tailwindcss/browser@4`）與 Swagger UI（`swagger-ui-dist@5`）皆以 jsDelivr CDN 載入：內網無外網時頁面只缺樣式／工具，`/openapi.json` 與既有 API 端點不受影響；頁面本身不含認證。

## 8. 環境變數

`.env` 已 gitignore，`.env.example` 只放模板不含真值。**必填變數缺一即 `slog.Error` + `os.Exit(1)`，禁止半啟動**；選用變數帶預設值，若已設定則驗格式（正整數或列舉值，如 `LOG_LEVEL`）。

| 變數 | 說明 |
|------|------|
| `XERO_CLIENT_ID` | Xero app client id |
| `XERO_CLIENT_SECRET` | Xero app client secret |
| `XERO_REDIRECT_URI` | 必須 `http://localhost:{port}/callback`，port 3000–9000 |
| `XERO_WEBHOOK_KEY` | webhook signing key（HMAC-SHA256 驗簽） |
| `XERO_TOKEN_ENC_KEY` | 32 bytes base64（AES-256-GCM 金鑰） |
| `MYSQL_DSN` | 例：`user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC` |
| `SYNC_CRON` | cron 表示式，例：`0 */2 * * *` |
| `HTTP_ADDR` | `serve` 監聽位址，例：`:8080` |
| `XERO_DAILY_CALL_LIMIT` | 選用；每 tenant 每日呼叫上限，未設定時採 `pkg/config` 內建預設（**禁**硬編碼在限流器） |
| `WEBHOOK_BUFFER_SIZE` | 選用；webhook buffer 的 batch size，未設定時預設 30 |
| `WEBHOOK_BUFFER_DELAY` | 選用；webhook buffer flush delay（毫秒），未設定時預設 3000 |
| `LOG_LEVEL` | 選用；日誌等級 `debug`／`info`／`warn`／`error`（大小寫不敏感），未設定時預設 `info`；非法值啟動即報錯 |

新增變數固定三步：`pkg/config` 讀取並驗證 → 補 `.env.example` → 更新本文件。

## 9. 指令與固定順序

```bash
go build -o xero-mirror.exe .            # 唯一 binary
go run . sync --month 2026-05            # 回補/重跑單月（不推進 cursor）
go run . sync                            # 依 cursor 增量（全部 active tenant）
go run . sync --entity payments          # 只同步指定實體（contacts|accounts|invoices|creditnotes|payments）
go run . serve                           # cron + webhook 常駐
go run . auth                            # 一次性授權（token 落地 MySQL）
go fmt ./... && go vet ./...             # 提交前必跑
go test ./...                            # 全套（無 DB／憑證時整合測試 SKIP）
go test ./pkg/sync -run TestRunBatch -v  # 單一模組 / 單一測試

# 需要 DB 的整合測試：測試 MySQL（host port 13315，拋棄式）
docker compose -f docker-compose.test.yml up -d
TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
  go test ./... -count=1
docker compose -f docker-compose.test.yml down
```

Migration 已 `//go:embed` 進 binary（stdlib，無額外依賴）：`sync`／`serve`／`auth` 任一 subcommand 啟動、連上 MySQL 後**自動套用**，不需要再手動以 mysql 客戶端載入 SQL 檔。

變更順序固定，不得跳步：**改 `pkg/store/migrations/*.sql` → `go build` → 跑任一 subcommand 觸發 migrate → `go test`**。

- **`sync` 語意**：無 `--month` 與 cron 同走 `Runner.RunAllIncremental`；`--month YYYY-MM` 走 `Runner.RunAllMonth`；`--entity NAME`（別名見 `pkg/sync.ParseEntity`）改走 `Runner.RunEntityIncremental`／`Runner.RunEntityMonth`，只跑指定實體。月度回補**不推進** `sync_state.cursor_time`（`Batch.Cursor = nil`，見 §3 規則 6）。部分 tenant 失敗只 `slog.Warn` 並續跑，最後回 `同步失敗 N 個 tenant`，subcommand 以 exit 1 結束；cron 對同一個錯誤只 Warn，不中斷排程。
- 回滾：人工執行對應 `.down.sql`，並刪掉 `schema_migrations` 裡該版本那一列（程式不自動跑 down）。
- 建置與單元測試可在 host 直接跑；需要 DB 的整合測試走 `docker compose -f docker-compose.test.yml up -d`（測試 MySQL 不保留資料），DSN 以 `TEST_MYSQL_DSN` 傳入。
- migration 與 `pkg/store` 對應的 SQL 修改要同一 commit；commit 前看 `git status`，用 Conventional Commits。
- **graceful shutdown（`serve`）**：收到 `SIGINT`／`SIGTERM`（`main.go` 以 `signal.NotifyContext` 取消 root ctx）後，`commands.go` 的 `serveDeps.shutdown` 依固定順序關閉，單步失敗只 `slog.Warn` 續行、不中斷後續步驟。關機流程自建 `context.WithTimeout(context.Background(), 30s)`（常數 `shutdownTimeout`，**總上限**、各步共用同一預算），不依賴已被信號取消的 root ctx，因此等待必定有上限：
  1. **context 生命週期**：`runServe` 建立兩條獨立於 signal ctx、派生自 `context.Background()` 的 context —— `cronCtx`（cron 同步用）與 `bufCtx`（`WebhookBuffer` 的 `baseCtx`，flush 用）。signal ctx 取消只觸發關機流程，不會連帶讓在飛同步或最後 flush 立刻失效；兩者皆 `defer cancel` 防洩漏，並由 shutdown 路徑掌控實際取消時機。
  2. `cron.Stop()`：停止排新 job。robfig/cron v3 的 `Stop()` 回傳一個 `context.Context`，會在所有執行中 job 結束時 done；服務在 30 秒總預算內 `select` 等待，**逾時才呼叫 `cancelCron()`** 取消 cronCtx，讓仍在飛的 cron／HTTP／DB 呼叫收斂後繼續關機（job 及時結束則不取消）。
  3. `http.Server.Shutdown(shutdownCtx)`：停止接收新連線並讓進行中請求收尾，等待受 30 秒總預算約束。
  4. `WebhookBuffer.Shutdown(shutdownCtx)`：event loop 把剩餘 pending 做最後 flush 後結束；**此時 `bufCtx` 尚未取消**，最後 flush 仍有效，此步不得被前一步的錯誤跳過。flush 完成（或等待逾時）後才呼叫 `cancelBuf()`。
  5. `*sql.DB.Close()`：關閉連線池。
- **cron 重疊防護（M1）**：cron 以 `cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))` 建立；上一輪 `runCronSync` 還在執行時，到點的下一輪直接跳過（記 logger 訊息），避免長同步與下一輪重疊、互相踩到 `sync_state` 水位。
- **cron 同步語意（`runCronSync`）**：cron 回呼只負責 `slog.Info("cron 同步開始")` 後呼叫 `sync.Runner.RunAllIncremental`，不再自行列 tenant 或另寫同步流程；失敗只 `slog.Warn("cron 同步未全部成功", "err", err)`，不讓排程中斷。`RunAllIncremental` 先用 `store.ListTenants` 取 `auth_status = 'active'` 的 tenant（`auth_required` 需重新授權才會再入列），再逐 tenant 呼叫 `sync.RunTenant`；`RunTenant` 依 `Entities()` 固定順序（Invoices、CreditNotes、Payments、Contacts、Accounts）對各實體跑 `RunIncremental`（讀 `sync_state.cursor_time`，無紀錄視為首跑全量）。單一實體失敗只 `slog.Warn` 並續跑，全部實體都失敗 `RunTenant` 才回錯；單一 tenant 失敗同樣 Warn 後續跑下一個 tenant。失敗計數一律以 tenant 為單位（`RunAllMonth` 任一實體失敗即記該 tenant 失敗，錯誤訊息為 `同步失敗 N 個 tenant`）。
- `serve` 的 HTTP server 必須設 `ReadHeaderTimeout`（常數 `readHeaderTimeout`，10 秒），避免無 timeout 的常駐 server；`AddFunc` 回傳 error 視為啟動失敗，不得靜默略過。

## 10. 錯誤處理、日誌與測試

| 主題 | 規則 |
|------|------|
| 錯誤包裝 | 只用 `if err != nil` / `return err` / `fmt.Errorf("同步 invoice: %w", err)`；**禁**自訂 error type／wrapper |
| 非致命錯誤 | `slog.Warn` + 降級續跑；單一 entity 失敗不得讓整輪崩潰；429 以 `slog.Debug`／`Warn` 記錄退避 |
| 致命錯誤 | 缺設定、DB 連不上 → `slog.Error` 後 exit |
| 日誌庫 | 只用 stdlib `log/slog`，**禁** logrus／zap／`log.Printf` |
| 訊息格式 | 繁體中文 + 結構化欄位：`slog.Info("同步完成", "tenant", tenantID, "entity", entity, "count", n)` |
| 敏感資料 | **禁**印 token、client secret、完整 payload；必要時只印 entity／xero_id／筆數 |

命名：縮寫大寫（`tenantID`、`invoiceID`）、環境變數 `UPPER_SNAKE_CASE`、DB 欄位 `snake_case`、常數 PascalCase + iota；註解一律繁體中文，函數註解 ≤ 6 行。

**必測三項**：冪等（同批資料跑兩次結果相同）、續跑（中途失敗後 cursor 可從上次水位續跑）、webhook 簽章錯誤回 `401`。

- Xero 一律 `httptest.Server` mock 並斷言 header（`Authorization`、`Xero-tenant-id`）；`go test ./...` 不得需要真實憑證，需要者 `t.Skip()`；無 DB 時整合測試 `t.Skip()`。
- DB 整合測試用拋棄式測試 MySQL（`docker-compose.test.yml`，host port 13315），DSN 以 `TEST_MYSQL_DSN` 傳入，未設定或連不上即 `t.Skip()`：

  ```bash
  docker compose -f docker-compose.test.yml up -d
  TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
    go test ./... -count=1
  docker compose -f docker-compose.test.yml down
  ```
- 每次變更最多 3–5 個案例，既有測試檔只追加；改模組前讀 `doc/modules/<module>.md`，行為／資料表／環境變數變更時同批更新本文件與模組文件，並同步 `doc/plans/2026-11-16-bootstrap.md` 狀態（阻塞標 🔴 並寫原因）。
