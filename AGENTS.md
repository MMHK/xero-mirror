# AGENTS.md — xero-mirror 專案指導原則（AI 必讀）

本文件是專案的行為合約。所有 AI 產生的程式碼必須遵守；違反任一「強制」規則 → 立即改寫。

回覆語言：**繁體中文**（程式碼註解、`doc/` 文件一律繁體中文；log 訊息一律英文；識別字用英文）。

---

## 0. 專案概述

**xero-mirror**：Go 服務，把 Xero Accounting 資料同步到本地 MySQL。

- **同步模式**：按月視窗增量 sync、cursor 增量 sync、內建 cron 排程、Xero webhook 觸發批次同步
- **技術棧**：Go 1.24+、MySQL、`database/sql`（手寫 SQL，不用 ORM / codegen）、stdlib `log/slog`、`net/http`
- **架構**：單一 binary（root `main.go` + subcommand dispatch），業務模組放 `pkg/`

### 目前狀態：建置中（任務 1–8 完成，任務 9 進行中）

repo 已有完整 scaffold、migration、store、xero client、sync 核心、webhook handler 基礎。本機 dev 環境用 `docker compose up -d` 啟動 MySQL 8.4（port 3307）。詳見 `doc/dev-setup.md` 與 `doc/plans/2026-11-16-bootstrap.md`。

可參考的同機專案（唯讀，不要修改）：`F:\TestProject\golang\xero-cli` — 已有可運作的 Xero OAuth2 與 Accounting API client（`auth.go`、`api.go`、`types.go`、`api_test.go`），抄邏輯時注意本文件對「服務 vs CLI」的差異。

---

## 1. 核心原則（最高優先）

1. **不過度設計**
   - 除非需求明講「高擴展性 / 多團隊 / DDD / 微服務」，一律用最簡單、可讀的方案。
   - 禁止提前抽象、無用 interface、無用 middleware、為未來鋪路的分層。
2. **沿用既有模式**
   - 新程式碼必須對齊既有命名、風格、結構。
   - 修改模組前讀 `doc/modules/` 對應文件；開新任務前讀 `doc/spec.md`。
3. **同步邏輯只有一份**
   - cursor sync、月度視窗 sync、webhook 批次同步共用同一套 fetch → upsert 核心。
   - webhook handler 只決定「要重同步什麼」，**不得另寫一套抓取/寫入邏輯**。
4. **工具鏈約束**
   - **禁止 pwsh**：所有 shell 指令用 Node.js 處理字符問題（避免 pwsh 的編碼與轉義陷阱）。
   - **禁止 npm**：統一使用 **yarn v4**（Berry）作為 package manager；`package.json` 必須搭配 `yarn.lock`。

---

## 2. 目錄結構（已定，禁止新增分層目錄）

```
.
├── main.go              # 入口（≤80 行）：讀 config、subcommand 分派、graceful shutdown
├── commands.go          # subcommand 接線（同 package main）：runSync / runServe / runAuth
├── pkg/
│   ├── config/          # .env 載入、驗證、DSN 組裝
│   ├── xero/            # Xero client：OAuth2、tenant header、分頁、429 重試、DTO
│   ├── store/           # repository：手寫 SQL（唯一碰 SQL 的層）+ 內嵌 migration
│   │   └── migrations/  # 內嵌 DDL（NNN_name.up.sql / NNN_name.down.sql，經 //go:embed 打包）
│   ├── sync/            # 實體同步編排：cursor、月度視窗、冪等 upsert
│   └── handler/         # webhook 接收、health、admin 手動觸發
├── scripts/             # Docker 初始化腳本（init-dev.sql 等）
├── tests/               # 測試工具（TestLogger、DB fixture、mock client）
├── doc/
│   ├── spec.md          # 架構與規格（必讀）
│   ├── dev-setup.md     # 本機開發環境設定
│   ├── modules/         # 各模組設計
│   └── plans/           # 實作計畫
├── docker-compose.yml   # 本機 dev MySQL（port 3307）
├── README.md            # 專案入口（使用者指定，快速啟動／指令／環境變數）
├── .env.example
└── go.mod
```

**禁止目錄**：`internal/`、`domain/`、`application/`、`infrastructure/`、`adapter/`、`lib/`、`common/`、`layers/`、`features/`、`cmd/`（本專案只有一個 entrypoint，就是 root `main.go`）。

- `pkg/` 下只允許上列 5 個模組；新檔案優先放進既有模組，不要塞 root。
- root 只允許 `main.go` 與 `commands.go`。`main.go` 的 ≤80 行只約束入口檔；subcommand 實作放 `commands.go`，且**只做接線**（config → DB → 呼叫 pkg），業務邏輯一律在 `pkg/`。
- 禁止引入 cobra/viper 做 subcommand；用 stdlib `flag` + `switch`。

---

## 3. 外部依賴（白名單）

| Package | 用途 | 備註 |
|---------|------|------|
| `github.com/go-sql-driver/mysql` | MySQL driver | 唯一 driver |
| `github.com/joho/godotenv` | `.env` 載入 | |
| `golang.org/x/oauth2` | OAuth2 token 交換 / refresh | |
| `golang.org/x/time/rate` | per-tenant 限流 | |
| `github.com/robfig/cron/v3` | 內建 cron | cron 排程 |
| `github.com/stretchr/testify` | 測試斷言 | 僅 `*_test.go` |

**規則**
- 新依賴需使用者明確同意；先問「既有依賴是否已能覆蓋」。
- 優先 stdlib（`log/slog`、`encoding/json`、`net/http`、`context`、`crypto/hmac`）。
- 禁止：GORM / `AutoMigrate`、logrus、zap、`shopspring/decimal`（金額用 `string`）、任何 ORM 或 codegen 框架。
- SQL 一律手寫在 `pkg/store`；schema 以 `pkg/store/migrations/*.sql` 內嵌（`//go:embed`）並在啟動時自動套用，不引入 migration 工具或 ORM。

---

## 4. 程式碼風格

### 函數與結構
- 新函數 ≤ 40 行；DTO ↔ DB 映射函數可到 80 行，但不得再長。
- 參數 ≤ 6、回傳值 ≤ 5。
- 一般 struct 欄位 ≤ 10；Xero API DTO **不受此限**（欄位是 API 契約，不得為了湊數而拆 struct）。
- DB row 對映型別（欄位即 schema 契約，如 invoice 的 12 欄）同樣不受 10 欄限制，但檔案內要用 `// DB row:` 註解標明；不得拿它當 API 模型輸出。

### 錯誤處理
- 只用基本模式：`if err != nil` / `return err` / `return fmt.Errorf("同步 invoice: %w", err)`。
- 禁止自訂多層 error type / error wrapper。
- 非致命錯誤：`slog.Warn()` + 降級續跑（單一 entity 失敗不得讓整個 sync run 崩潰）。

### 日誌
- 一律 stdlib `log/slog`，禁止 logrus/zap/`log.Printf`。
- log 訊息用英文，帶結構化欄位：`slog.Info("sync completed", "tenant", tenantID, "entity", entity, "count", n)`。
- 不得把 token、client secret、完整 payload 寫進 log。

### 命名
- 縮寫大寫：`userID`、`invoiceID`、`tenantID`（**不要** `userId`）。
- 環境變數 `UPPER_SNAKE_CASE`；DB 欄位 `snake_case`。
- 常數用 PascalCase + iota。

### JSON tag
- 一般 JSON 用 `snake_case`。
- **Xero DTO 例外**：tag 必須照 Xero 原樣 PascalCase（`json:"InvoiceID"`、`json:"LineItems"`）。不要為了統一風格去改 Xero DTO。
- DB 模型與 API 模型分開定義，不要同一個 struct 兩用。

### 金額與時間（強制）
- 金額禁止 `float64`；保留 Xero 回傳的字串，DB 用 `DECIMAL`。加總/比較用十進位字串或整數最小單位。
- 時間一律 `time.Time` + UTC 入庫；Xero 部分端點回 `/Date(1234567890000+0000)/`，必須顯式解析（毫秒 + 時區偏移），不可丟給 RFC3339。
- 所有對外 HTTP 請求帶 `context.Context` 與 timeout；禁止無 timeout 的 client。

### HTTP 端點（webhook / admin）
- 只允許 `GET`、`POST`；`PUT/PATCH/DELETE` 禁止。此限制只針對**本服務自己提供**的端點；呼叫 Xero API 時照 Xero 要求（例如註冊 webhook 是 `PUT /webhooks`）。
- 變更用 `POST /xxx/update`、刪除用 `POST /xxx/delete` 動作路徑。
- Handler 只做參數解析、驗證、回 200/4xx，業務邏輯放 `pkg/sync`。

---

## 5. Xero API 與認證（強制）

- Base URL：`https://api.xero.com/api.xro/2.0`；`/connections` 例外，掛在 `https://api.xero.com`（不可加 `/api.xro/2.0`）。
- 每個 tenant 呼叫都要 `Authorization: Bearer <accessToken>` + `Xero-tenant-id: <tenantID>`；header 缺失時先懷疑 tenant 綁定，不要直接當成 token 過期。
- 這支是 **服務**，不是 CLI：用 OAuth2 Authorization Code（含 `offline_access`），**不要**照搬 `xero-cli` 的 PKCE 本機 callback 流程。首次授權可以另做一次性 `auth` subcommand，但 token 必須落地 MySQL 供服務使用。
- Token 生命週期（極易踩雷）：
  - access token 約 30 分鐘；refresh token 約 60 天，**每次 refresh 都會輪換**，舊值作廢。
  - 新 refresh token 必須與新 access token **同一交易寫回 DB**；只更新 access token 等於下次必然斷線。
  - 同一 tenant 的 token refresh 必須序列化（單飛），避免併發 refresh 造成兩個 token 都失效。
- Token 靜態加密：DB 內存 AES-256-GCM 密文，金鑰來自 env（不得硬編碼），nonce 每次隨機，密文包含版本號。禁止明文存 token。
- Scope 至少：`openid profile email accounting.transactions accounting.settings accounting.contacts offline_access`。
- Redirect URI 開發期用 `http://localhost:{port}/callback`（port 3000-9000），**禁止** `127.0.0.1`（Xero 註冊不匹配會直接失敗）。
- Webhook（強制）：
  - 用 `crypto/hmac` + `sha256`，key 為 webhook signing key，對 **raw body** 計算後 base64，與 `x-xero-signature` 比對；必須 constant-time 比較。
  - 簽章驗證前不得 `json.Unmarshal` 覆蓋 raw body。
  - 先回 200，再把事件寫入 DB 待辦（`sync_jobs` / `webhook_events`）；Xero 逾時會重送，處理太慢會被停用 webhook。
  - 用 `eventId`（或 `resourceId` + `eventDate`）去重，重複事件不得重複入列。
  - 驗證失敗回 401，body 不合法回 400；只有簽章正確才回 200。

### 抓取與限流
- 分頁：`page=1..N`，每頁 100 筆，直到回傳筆數 < 100 或空陣列；不要假設總頁數。
- 限流（per tenant）：5 併發、約 60 次/分、每日上限。用 `golang.org/x/time/rate` 做 per-tenant limiter，**不要**全域 fan-out。
- `429` 與 `Retry-After` 是預期狀況：讀 header 退避重試，不是錯誤告警。
- 增量：優先用端點支援的 `If-Modified-Since`（或 `where=UpdatedAtUTC>...`），把實際使用的水位記進 `sync_state`；不支援增量的端點才退回全量 + 冪等 upsert。
- 軟刪除：Xero 用 `STATUS`（`DELETED`/`ARCHIVED`）標記，**不要**因為 API 沒回傳就刪本地列；照實鏡射 status。

---

## 6. 資料庫與 Migration

- Schema 唯一來源是 `pkg/store/migrations/` 的編號 DDL（`NNN_name.up.sql` 與成對的 `.down.sql`），以 `//go:embed` 編進 binary；不引入 migration 工具。
- 啟動時自動套用：`sync`、`serve`、`auth` 在建立 MySQL 連線後、做任何業務前呼叫 `store.Migrate`；連不上 DB 就 `slog.Error` + exit，**不得半啟動**。
- `Migrate` 必須冪等：以 `schema_migrations(version varchar(64) PK, applied_at DATETIME(6))` 記錄已套用版本；同一版本不重跑。
- 併發保護：套用前取 MySQL advisory lock（`SELECT GET_LOCK('xero-mirror:migrate', n)`），結束後 `RELEASE_LOCK`，避免多實例同時 migrate。
- MySQL DDL 隱式提交、無法交易回滾：每支 migration 只做一件事（一張表或一組索引），失敗時不寫版本紀錄，修好後重跑；`.down.sql` 供人工回滾，程式不自動執行。
- 變更順序固定：改 migration → `go build` → 啟動時自動套用 → `go test`。
- SQL 語句全部寫在 `pkg/store`；其他套件不得出現 SQL 字串。`?` placeholder，禁止字串拼接組 SQL。
- 所有表：`utf8mb4_unicode_ci`、`DATETIME(6)` 存 UTC、PK 用 Xero ID（`varchar(64)`）或複合鍵 `(tenant_id, xero_id)`。
- `sync_state` 至少包含：`tenant_id`、`entity`、`cursor_time`、`last_run_at`、`status`；cursor 更新必須在資料 upsert 的同一個交易內提交，否則失敗後會漏資料。
- 金額欄位 `DECIMAL(18,4)`（Xero 可到 4 位小數）；禁止 `FLOAT`/`DOUBLE`。
- 每個同步表都要有 `synced_at` 與 `xero_updated_at`，方便追查落後進度。

---

## 7. 設定與環境變數

- `.env` 已 gitignore；`.env.example` 只放模板，不含真值。
- 新增環境變數三步：`pkg/config` 讀取並驗證 → 補 `.env.example` → 需要時更新 `doc/spec.md`。
- 預期變數：`XERO_CLIENT_ID`、`XERO_CLIENT_SECRET`、`XERO_REDIRECT_URI`、`XERO_WEBHOOK_KEY`、`XERO_TOKEN_ENC_KEY`（32 bytes base64）、`MYSQL_DSN`、`SYNC_CRON`、`HTTP_ADDR`。
- 啟動時驗證必填變數，缺一個就 `slog.Error` 後 exit，不要半啟動。

---

## 8. 測試規則

### 原則
- **不批量生成測試**：每次變更最多 3-5 個案例，除非需求明講「完整測試」或「覆蓋率 ≥90%」。
- 增量修改順序：已有 `*_test.go` → 只在檔尾**追加**或修失敗的測試；已有 happy path → 只補邊界/錯誤；完全沒測試才新建檔案。
- 每個測試 ≤ 30 行，只驗一個主要行為。表驅動測試允許。
- 用 `testify/require` + `testify/assert`；工具函數放 `tests/testing.go`。
- Xero 呼叫一律用 `httptest.Server` mock（參考 `xero-cli/api_test.go` 的 header 斷言），`go test ./...` 不得需要真實 Xero 憑證；需要真憑證的測試用 `t.Skip()`。
- 必測：同一批資料跑兩次結果相同（冪等）、中途失敗後 cursor 能續跑、webhook 簽章錯誤回 401。
- DB 整合測試需要 MySQL：用 `docker compose -f docker-compose.test.yml up -d mysql`（compose 檔尚未建立，建立時照此命名）；本機無 DB 時用 `t.Skip()`，不要讓 `go test ./...` 直接紅。

---

## 9. 文件規則

- `doc/` 是唯一允許的文件目錄；禁止新增 `docs/`、`architecture.md`、`README` 新版、`CHANGELOG.md`、`CONTRIBUTING.md`（除非使用者要求）。
- 開工前必讀 `doc/spec.md`；改模組前讀 `doc/modules/<module>.md`；行為/介面變更後同步更新對應文件。
- 函數註解 ≤ 6 行（用途 + 參數 + 回傳）；不要逐行註解廢話。

---

## 10. 指令（scaffold 建立後適用）

```bash
# 本機 dev 環境
docker compose up -d                   # 啟動 MySQL（port 3307）
docker compose down                    # 停止（保留資料）
docker compose down -v                 # 停止並清除資料

# Go 指令
go build -o xero-mirror.exe .          # 唯一 binary
go run . sync --month 2026-05          # 回補/重跑單月
go run . sync                          # 依 cursor 增量
go run . serve                         # cron + webhook 常駐
go fmt ./... && go vet ./...           # 提交前必跑
go test ./...                          # 全套
go test ./pkg/sync -run TestCursor -v  # 單一模組 / 單一測試
```

- **本機 dev 流程**：`docker compose up -d` → `cp .env.example .env`（填入 Xero 憑證）→ `go run . auth` → `go run . serve`
- migration 已 `//go:embed` 進 binary：`sync`／`serve`／`auth` 啟動連上 MySQL 後自動套用，不需要手動 `mysql < 檔案`。
- 順序：改 `pkg/store/migrations/*.sql` → `go build` → 跑任一 subcommand 觸發 migrate → `go test`。
- 回滾：人工執行對應 `.down.sql`，並刪掉 `schema_migrations` 裡該版本那一列。
- 建置與單元測試可在 host 直接跑；**需要 DB 的整合測試走 docker compose**，不要在 host 裝 MySQL。

---

## 11. Git 規則

- Conventional Commits（`feat:`, `fix:`, `refactor:`, `docs:`, `chore:`），訊息可用繁體中文；commit 前必看 `git status`。
- 禁止提交：`*.exe`、`*.dll`、`*.so`、`*.dylib`、`.env`、`vendor/`、`.idea/`、`.vscode/`、`.env` 內的任何 token。
- migration 變更與 `pkg/store` 對應的 SQL 修改要放同一 commit，不要把 schema 與查詢拆散。

---

## 12. Agent 協作流程（強制）

### 12.1 角色
- **Main Agent**（主 session）：只做編排，**不直接寫程式碼**。
- **Sub Agent**（用 `subagent` 工具，不是 `task`）：
  - `explore`：看碼、找檔案、回答架構問題。
  - `general`：實作、改檔、跑測試。
  - `code-reviewer`：對照計畫做 code review。
  - `requirement-clarity`：需求釐清。

### 12.2 每個任務的順序

```
1. PLAN       → 在 doc/plans/YYYY-MM-DD-<slug>.md 寫計畫（繁體中文）
                 slug 用 kebab-case（小寫 + 連字號），例如 2026-11-17-webhook-handler.md
2. USER REVIEW → 用 question 工具呈現計畫並等待明確批准
3. DELEGATE   → 派 sub agent 實作（一次一個邏輯任務）
4. REVIEW     → 派 code-reviewer 對照計畫審查
5. UPDATE     → 更新計畫狀態
6. LOOP       → 檢查進度，繼續下一個任務直到全部 ✅ / ⏭️
```

- **未經使用者明確批准，不得進入 DELEGATE。**
- 每個實作任務完成後必須有 `code-reviewer` 審查；review 前不得標記 ✅。
- 派工 prompt 要帶：檔案路徑、預期行為、本文件相關約束、驗收方式。

### 12.3 計畫文件必備結構

```markdown
# <任務標題>

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: YYYY-MM-DD
**狀態**: 🟡 等待 Review | 🟢 完成 | 🔴 阻塞 | ⚪ 待處理
**優先級**: High | Medium | Low

## 背景與目標
## 任務列表
| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 任務描述 | ⬜ 待處理 | — | |

## 人類 Review 記錄
| 日期 | 動作 | 備註 |
|------|------|------|
| YYYY-MM-DD | ⏳ 等待 Review / ✅ 已批准 / 🔄 要求修改 / ❌ 已拒絕 | |

## Review 日誌
| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 參考來源
| 主題 | 來源 | 連結 |
|------|------|------|
| 主題描述 | 來源名稱 | URL 或專案內檔案路徑 |

## 備註
```

- 狀態圖示：⬜ 待處理、🔄 進行中、✅ 完成、🔴 阻塞、⏭️ 跳過（附原因）。
- 阻塞時必須更新計畫為 🔴 並寫原因，不得默默放棄。
- **參考來源（強制）**：計畫中涉及的技術決策、API 規格、外部行為必須附上來源連結（官方文件、FAQ、或專案內檔案路徑）。不可憑印象描述外部 API 行為。

---

## 13. 產出前自我檢查

- 是否為了這個需求多建立了 2 倍以上的檔案/結構？
- 是否留下沒用到的 interface / 抽象 / middleware？
- 已有測試時是否又新建測試檔？bug fix 是否加了超過 5 個案例？
- 是否新增了未經同意的依賴、`internal/` 之類禁止目錄、或 GORM？
- 金額是否誤用 `float64`？時間是否沒轉 UTC？
- webhook 是否在驗簽前就解析 body？refresh token 是否漏寫回？
- 新程式碼是否用了 `log/slog`、JSON tag 是否符合 Xero DTO 例外規則？
- 有介面/行為變更時，`doc/` 與 `doc/plans/` 是否同步更新？

→ 任一項「是」→ 立刻簡化/修正，並在回覆中說明原因。

---

## 14. 輸出格式

1. 先給「最小變更」版本：只列改動的 diff 或完整小檔、追加的測試、必要的註解。
2. 再說明是否需要更新 `doc/` 或計畫文件狀態。
3. 最後列出已執行的驗證指令與結果（例如 `go test ./pkg/sync` 通過）。
