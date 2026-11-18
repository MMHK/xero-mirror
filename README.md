# xero-mirror

把 **Xero Accounting** 的資料自動鏡射到本地 **MySQL** 的小型 Go 服務。

Xero 是唯一資料來源，本服務只做**單向同步**，不會寫回 Xero。

## 原理

- 定期從 Xero 抓取五類資料：`Invoices`、`CreditNotes`、`Payments`、`Contacts`、`Accounts`，寫入本地 MySQL。
- 同步有四種觸發方式：**定時排程**、**手動增量**、**指定月份回補**、以及 **Xero webhook 即時通知**；四者共用同一套抓取與寫入流程，結果一致且可重複執行。
- 已抓過的進度會記錄下來，中斷後可從上次位置續跑；重跑同一批資料不會產生重複列。
- Xero 的 access token／refresh token 加密後存放於 MySQL，服務重啟或長時間執行都能自動續用。
- 刪除或封存的資料在 Xero 會標記 `STATUS`，本服務照實鏡射，不會直接刪掉本地資料。

## 為什麼要做本地鏡像：Xero 的 100K 限制與 API 用量方案

本專案的起點就是 Xero 的 **High Volume Threshold（俗稱 100K 限制）**：當組織資料量大到某個程度，直接對 Xero 做報表式查詢會被拒絕。把資料鏡射到本地 MySQL，讓查詢走本地庫，是繞開這個限制最實際的做法。

### High Volume Threshold（100K）

自 **2024-09-04** 起，Xero 對 Accounting API 的部分 `GET` 端點強制執行高量門檻：

- 單次請求若需**檢索、處理、過濾或排序超過 100k 份文件**，會被拒絕並回 **HTTP 400**。
- 門檻看的是「**系統必須掃過的總量**」，**不是**回傳筆數。因此只把 `pageSize` 調小並不能繞過，關鍵在查詢本身是否有效率。
- 受影響端點與上限：`Invoices`、`BankTransactions`、`Payments`、`ManualJournals`、`CreditNotes`、`Contacts`、`PurchaseOrders` 各 **100k**；`Journals` 較嚴格，單次上限 **100 筆**。
- 尚未被拒但效率不佳的請求，會在回應 JSON 的 `Warnings[]` 中預警；若不處理，之後就會變成 400。
- 官方建議的作法：`page` + `pageSize`（上限 1000）、`If-Modified-Since` 增量、使用端點提供的優化過濾參數、必要時 `summaryOnly=true`。

本專案的對策：**日常查詢完全不呼叫 Xero**，只有同步時才以「分頁 + `If-Modified-Since` 增量」抓取，避免全量掃描觸發門檻。

### Rate Limits 與用量方案（Usage Plan）

即使只是同步，也必須遵守 Xero 的呼叫限制（皆以「單一 tenant/app 連線」計算，`X-AppMinLimit-Remaining` 例外，為整個 app 統計）：

| 限制 | 數值 |
|------|------|
| 併發 | 5 個請求同時進行 |
| 每分鐘 | 60 次 |
| 每日 | Starter **1,000** 次；付費方案 **5,000** 次 |
| App 全體每分鐘 | 10,000 次 |

超過限制會回 **HTTP 429**，並以 `X-Rate-Limit-Problem` 指出是哪一種限制、`Retry-After` 告知需等待秒數；每個 tenant 的重置時間不同，不能共用一個全域退避計時器。

**2026-03-02 生效的開發者方案**（費用在開發者帳號扣款，非客戶的 Xero 訂閱）：

| | Starter | Core | Plus | Advanced | Enterprise |
|---|---|---|---|---|---|
| 連線上限 | 5 | 50 | 1,000 | 10,000 | 無上限 |
| 月費（AUD，未稅） | 免費 | $35 | $245 | $1,445 | 另議 |
| 每日呼叫／每 org | 1,000 | 5,000 | 5,000 | 5,000 | 5,000 |
| 每月 egress | 不計量 | 10 GB | 50 GB | 250 GB | 另議 |
| egress 超額 | — | $2.40/GB | $2.40/GB | $2.40/GB | 另議 |

補充規則：

- 只有 **egress（從 Xero 讀出）** 計量，寫入不限制；`Organisation` 端點不計入 egress。
- egress 額度與連線數都**以 app 為單位**，同一開發者的不同 app 不能共用。
- egress 額度於**每月 1 日 UTC** 重置，即帳單週期。
- `Journals` 端點、XPM API、Bulk Connections 需 **Advanced 以上 + 資安評估**；Rapid Sync（新連線前 30 分鐘放寬限制）僅限已認證 app。

### Webhook 不能取代 API 呼叫

Webhook 只會告訴你「哪個資源變了」（`eventId`、`resourceId`），**實際資料仍需另外 `GET`**。因此本專案把 webhook 定位為「**決定要重新同步什麼**」，而不是資料來源：

- 先驗簽並回 200，再把事件寫入待辦佇列。
- 以 `eventId` 去重，重複事件不重複入列。
- 只針對受影響的 tenant／entity 觸發增量同步，不整批重抓。
- 所有觸發方式（排程、手動、月度回補、webhook）共用同一套分頁抓取與冪等 upsert，避免多套邏輯各自消耗配額。

也因此，**頻繁的 webhook 事件仍會累積 API 用量與 egress**，需要搭配用量方案評估：大量異動時應合併成批次處理，而不是每個事件都立刻打一次 Xero。

### 引用來源

- [Efficient data retrieval for large organisations（High Volume Threshold）](https://developer.xero.com/documentation/api/efficient-data-retrieval)
- [Accounting API High Threshold Update](https://developer.xero.com/documentation/api/changelog/accountingapi-high-threshold-update)
- [Paging](https://developer.xero.com/documentation/best-practices/api-call-efficiencies/paging)
- [Rate Limits](https://developer.xero.com/documentation/best-practices/api-call-efficiencies/rate-limits)
- [OAuth 2.0 API limits](https://developer.xero.com/documentation/guides/oauth2/limits)
- [Xero Developer Pricing（方案與 egress）](https://developer.xero.com/pricing)
- [Pricing and Policy Updates FAQ](https://developer.xero.com/faq/pricing-and-policy-updates)
- [Custom Connections（M2M 單一組織連線）](https://developer.xero.com/documentation/guides/oauth2/custom-connections)

## 使用場景

| 場景 | 說明 |
|------|------|
| 本地查詢與報表 | 資料留在自己的 MySQL，可直接寫 SQL、接 BI 工具或內部系統，不必每次打 Xero API。 |
| 降低 API 依賴 | 日常查詢走本地庫，只有同步時才呼叫 Xero，避免配額與限流問題。 |
| 歷史資料保存 | 持續累積 Xero 資料，即使日後不再授權也能保留既有紀錄。 |
| 系統整合 | 會計資料可餵給 ERP、對帳、資料倉儲等下游流程。 |
| 即時性需求 | 透過 webhook，Xero 一有異動就觸發同步，減少等待排程的時間差。 |

## 快速開始

```bash
docker compose up -d          # 啟動本機 MySQL（port 3307）
cp .env.example .env          # 填入 Xero 憑證與 MySQL 連線
go build -o xero-mirror.exe .

./xero-mirror.exe auth        # 一次性授權，完成後 token 存入 MySQL
./xero-mirror.exe serve       # 常駐：定時同步 + webhook
```

其他用法：

```bash
./xero-mirror.exe sync                  # 依上次進度增量同步
./xero-mirror.exe sync --month 2026-05  # 回補指定月份
./xero-mirror.exe sync --entity payments # 只同步指定實體
```

資料庫結構會在啟動時自動建立，不需手動匯入 SQL。

## Docker 建構

### 本機建構

```bash
# 建構 image
docker compose build                    # 透過 compose 建構（含 MySQL）
docker build -t mmhk/xero-mirror .      # 單獨建構 image

# 執行（搭配 compose，MySQL 自動接線）
docker compose up -d                    # 同時啟動 MySQL + app

# 單獨執行 image（需自行處理 MySQL 連線）
docker run --rm mmhk/xero-mirror auth
docker run --rm mmhk/xero-mirror serve
docker run --rm mmhk/xero-mirror sync --month 2026-05
```

Compose 啟動時，app 會自動等待 MySQL 健康後才啟動，並使用 Docker 內部網路連線（`mysql:3306`）。

### 自動建構（CI/CD）

Push semver tag 時，GitHub Actions 會自動建構 multi-arch image 並推送 Docker Hub：

```bash
git tag v1.0.0
git push origin v1.0.0
```

產出：
- `mmhk/xero-mirror:v1.0.0`（multi-arch manifest）
- `mmhk/xero-mirror:v1.0.0-amd64`
- `mmhk/xero-mirror:v1.0.0-arm64`
- `mmhk/xero-mirror:latest`（multi-arch manifest）

支援 `linux/amd64` 與 `linux/arm64`。需要設定 GitHub Secrets：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`。

## Web 介面

`serve` 啟動後可直接在瀏覽器使用。網址以 `.env` 的 `HTTP_ADDR` 為準（例：`HTTP_ADDR=:8080` → `http://localhost:8080/`）：

- `/` — 專案介紹頁
- `/swagger` — Swagger UI（受 `SWAGGER_ENABLED` 控制，預設關閉回 `404`）
- `/openapi.json` — OpenAPI 3.0.3 文件（同樣受 `SWAGGER_ENABLED` 控制）
- `/health` — 健康檢查

Swagger UI 由 CDN 載入 swagger-ui-dist，無需 npm 或任何建置流程；離線內網會影響頁面載入，API 端點照常運作。若要開放文件頁，設定 `SWAGGER_ENABLED=true`。

唯讀狀態查詢 API（只接受 `GET`，無內建認證，請部署於內網或反向代理後方）：

- `GET /api/status/sync` — 各 tenant × entity 的同步狀態（可選 `tenant`／`entity` 過濾）
- `GET /api/status/jobs` — 同步佇列統計
- `GET /api/status/tenants` — tenant 狀態與 Xero API 配額剩餘量
- `GET /api/status/webhooks` — webhook 事件統計

## 更多文件

- [`doc/spec.md`](doc/spec.md) — 完整架構與規格
- [`doc/dev-setup.md`](doc/dev-setup.md) — 開發環境與疑難排解
- [`doc/modules/`](doc/modules) — 各模組設計
- [`AGENTS.md`](AGENTS.md) — 專案行為合約

## 授權

[Apache License 2.0](LICENSE)
