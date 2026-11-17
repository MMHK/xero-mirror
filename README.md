# xero-mirror

把 **Xero Accounting** 的資料自動鏡射到本地 **MySQL** 的小型 Go 服務。

Xero 是唯一資料來源，本服務只做**單向同步**，不會寫回 Xero。

## 原理

- 定期從 Xero 抓取五類資料：`Invoices`、`CreditNotes`、`Payments`、`Contacts`、`Accounts`，寫入本地 MySQL。
- 同步有四種觸發方式：**定時排程**、**手動增量**、**指定月份回補**、以及 **Xero webhook 即時通知**；四者共用同一套抓取與寫入流程，結果一致且可重複執行。
- 已抓過的進度會記錄下來，中斷後可從上次位置續跑；重跑同一批資料不會產生重複列。
- Xero 的 access token／refresh token 加密後存放於 MySQL，服務重啟或長時間執行都能自動續用。
- 刪除或封存的資料在 Xero 會標記 `STATUS`，本服務照實鏡射，不會直接刪掉本地資料。

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

## Web 介面

`serve` 啟動後可直接在瀏覽器使用。網址以 `.env` 的 `HTTP_ADDR` 為準（例：`HTTP_ADDR=:8080` → `http://localhost:8080/`）：

- `/` — 專案介紹頁
- `/docs` — Swagger UI
- `/openapi.json` — OpenAPI 3.0.3 文件
- `/health` — 健康檢查

頁面樣式（Tailwind v4）與 Swagger UI 皆由 CDN 載入，無需 npm 或任何建置流程；離線內網只影響樣式，API 端點照常運作。

`/admin/sync` 無內建認證，僅供內網或反向代理後方使用，請勿直接暴露公網。

## 更多文件

- [`doc/spec.md`](doc/spec.md) — 完整架構與規格
- [`doc/dev-setup.md`](doc/dev-setup.md) — 開發環境與疑難排解
- [`doc/modules/`](doc/modules) — 各模組設計
- [`AGENTS.md`](AGENTS.md) — 專案行為合約

## 授權

[Apache License 2.0](LICENSE)
