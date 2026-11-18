# 路由清理與 Status API 開放

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟢 完成
**優先級**: Medium

---

## 背景與目標

目前 HTTP 路由需要調整：
1. `/docs` 改為更明確的 `/swagger`，並用 env 控制是否開放
2. Swagger UI 頁面去掉 Tailwind CSS，只保留原生 swagger-ui
3. 移除 `/admin/*` 路由（不透過 web handler 觸發同步）
4. 開放 status 查詢 API，供外部監控/除錯使用

---

## 變更總覽

### 1. `/docs` → `/swagger` + env 控制

| 項目 | 說明 |
|------|------|
| 路由變更 | `GET /docs` → `GET /swagger` |
| 新增 env | `SWAGGER_ENABLED`（`"true"` / `"false"`，預設 `"false"`） |
| 行為 | `SWAGGER_ENABLED=false` 時，`GET /swagger` 回 404；`GET /openapi.json` 也一併回 404 |
| 首頁 `/` | 不受影響，永遠可訪問 |

### 2. Swagger UI 頁面簡化

- 移除 `docs.html` 中的 Tailwind CSS CDN（`@tailwindcss/browser@4`）
- 移除 header nav 的自訂樣式
- 只保留 swagger-ui-dist 的 CDN 載入與 `SwaggerUIBundle` 初始化
- 頁面用最小化 inline style 排版即可

### 3. 移除 `/admin/*` 路由

- 刪除 `POST /admin/sync` 路由
- 刪除 `pkg/handler/admin.go`
- 從 `NewMux` 移除 `AdminHandler` 參數
- 從 `commands.go` 的 `buildServeDeps` 移除 `AdminHandler` 建構
- 從 `openapi.json` 移除 `/admin/sync` 的 path 定義

### 4. 新增 Status API

所有 status API 掛在 `/api/status/*` 下，只接受 `GET`。

#### 4.1 `GET /api/status/sync` — 同步狀態

查詢各 tenant × entity 的 `sync_state` 記錄。

**回應範例**：
```json
{
  "items": [
    {
      "tenant_id": "xxx",
      "entity": "invoices",
      "cursor_time": "2026-11-01T00:00:00Z",
      "last_run_at": "2026-11-18T10:30:00Z",
      "status": "ok"
    }
  ]
}
```

**查詢參數**（可選）：
- `tenant` — 過濾特定 tenant
- `entity` — 過濾特定 entity

#### 4.2 `GET /api/status/jobs` — 同步佇列

查詢 `sync_jobs` 的統計。

**回應範例**：
```json
{
  "pending": 3,
  "processing": 1,
  "failed": 0,
  "total": 4
}
```

#### 4.3 `GET /api/status/tenants` — Tenant 狀態（含 Rate Limit 配額）

合併 tenant 狀態與 rate limit 配額，一次回傳。

**資料來源**：
- Tenant 資訊：`xero_tenants` 表
- Rate limit：Xero API 每個 response 都會帶以下 headers：
  - `X-DayLimit-Remaining` — 剩餘每日配額（per tenant）
  - `X-MinLimit-Remaining` — 剩餘每分鐘配額（per tenant）
  - `X-AppMinLimit-Remaining` — 剩餘全 app 每分鐘配額（跨所有 tenant）

**實作方式**：
1. 擴充 `xero_tenants` 表，新增 4 個欄位儲存 rate limit 快照
2. `pkg/xero` client 每次收到 Xero response 時，解析這 3 個 header，非同步更新該 tenant 的欄位
3. `/api/status/tenants` 直接從 `xero_tenants` 讀取

**回應範例**：
```json
{
  "items": [
    {
      "tenant_id": "xxx",
      "tenant_name": "My Org",
      "auth_status": "active",
      "token_expiry": "2026-11-18T11:00:00Z",
      "last_sync_at": "2026-11-18T10:30:00Z",
      "day_limit_remaining": 4958,
      "minute_limit_remaining": 57,
      "app_minute_limit_remaining": 9929,
      "rate_limit_snapshot_at": "2026-11-18T10:30:00Z"
    }
  ]
}
```

**欄位說明**：
- `auth_status` — `active` / `expired` / `revoked`
- `token_expiry` — access token 過期時間（UTC）
- `last_sync_at` — 最後一次成功同步時間（從 `sync_state` 聚合）
- `*_limit_remaining` — Xero API 配額剩餘量
- `rate_limit_snapshot_at` — rate limit 快照時間

#### 4.4 `GET /api/status/webhooks` — Webhook 事件統計

查詢 `webhook_events` 的處理狀態統計。

**回應範例**：
```json
{
  "pending": 0,
  "processed": 150,
  "total": 150
}
```

**備註**：`webhook_events` 表目前只有 `processed_at`（NULL = pending，NOT NULL = processed），沒有 `failed` 狀態的語義。

---

## 任務列表

| # | 任務 | 狀態 | 備註 |
|---|------|------|------|
| 1 | `pkg/config` 新增 `SwaggerEnabled` 欄位，讀取 `SWAGGER_ENABLED` env | ✅ 完成 | 預設 `false` |
| 2 | 簡化 `docs.html`：移除 Tailwind CSS，只保留 swagger-ui-dist | ✅ 完成 | |
| 3 | 路由 `/docs` → `/swagger`，根據 `SWAGGER_ENABLED` 決定 200 或 404 | ✅ 完成 | `/openapi.json` 同步受控 |
| 4 | 移除 `admin.go`、`/admin/sync` 路由、`NewMux` 的 `AdminHandler` 參數 | ✅ 完成 | |
| 5 | 新增 migration：擴充 `xero_tenants` 加 4 個 rate limit 欄位 | ✅ 完成 | `day_limit_remaining`、`minute_limit_remaining`、`app_minute_limit_remaining`、`rate_limit_snapshot_at` |
| 6 | `pkg/xero` client：每次 response 解析 rate limit headers 並回傳給呼叫方 | ✅ 完成 | `RateLimitInfo` + callback 非同步通知 |
| 7 | `pkg/store`：新增 `xero_tenants` rate limit 欄位的 update + 查詢方法 | ✅ 完成 | `UpdateTenantRateLimit` + Tenant struct 擴充 |
| 8 | 更新 `openapi.json`：移除 `/admin/sync`，新增 4 個 status API，更新 `/swagger` | ✅ 完成 | |
| 9 | 新增 `pkg/handler/status.go`：實作 4 個 status handler | ✅ 完成 | 需要注入 store |
| 10 | 更新 `pkg/handler/mux.go`：註冊 `/api/status/*` 路由 | ✅ 完成 | |
| 11 | 更新 `commands.go`：調整 `buildServeDeps` 的 handler 建構 | ✅ 完成 | |
| 12 | 更新 `.env.example`：新增 `SWAGGER_ENABLED` | ✅ 完成 | |
| 13 | 更新 `doc/spec.md` 路由相關段落 | ✅ 完成 | |
| 14 | Code Review | ✅ 完成 | 已修正 `last_sync_at`、`CountWebhookEvents` NULL scan、`ListAllTenants` 敏感欄位 |

---

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ⏳ 等待 Review | |
| 2026-11-18 | ✅ 已批准 | 開始實作 |

---

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

---

## 參考來源

| 主題 | 來源 | 連結 |
|------|------|------|
| Xero API Rate Limits（response headers 規格） | Xero Developer Docs | https://developer.xero.com/documentation/best-practices/api-call-efficiencies/rate-limits |
| Xero API Limits FAQ（限額數字） | Xero Developer FAQ | https://developer.xero.com/faq/limits |
| Swagger UI CDN 使用方式 | swagger-ui-dist (jsDelivr) | https://www.jsdelivr.com/package/npm/swagger-ui-dist |
| 現有路由註冊 | `pkg/handler/mux.go` | 專案內 |
| 現有 Swagger UI 頁面 | `pkg/handler/docs.html` | 專案內 |
| 現有 OpenAPI spec | `pkg/handler/openapi.json` | 專案內 |
| 現有 Admin handler | `pkg/handler/admin.go` | 專案內 |
| 現有 HTTP 接線 | `commands.go` `buildServeDeps()` | 專案內 |
| 現有 `xero_tenants` schema | `pkg/store/migrations/0001_create_xero_tenants.up.sql` | 專案內 |

---

## 備註

### 待確認事項

1. **Status API 是否需要認證？** 目前規劃為無認證（與 `/health` 一致），假設部署在內網或 reverse proxy 後。如果需要認證請告知。

### `xero_tenants` 擴充欄位

```sql
ALTER TABLE xero_tenants
  ADD COLUMN day_limit_remaining INT NULL,
  ADD COLUMN minute_limit_remaining INT NULL,
  ADD COLUMN app_minute_limit_remaining INT NULL,
  ADD COLUMN rate_limit_snapshot_at DATETIME(6) NULL;
```

- 不新建表，直接擴充現有 `xero_tenants`
- 每次 `pkg/xero` client 收到 Xero response 時，非同步更新該 tenant 的 4 個欄位
- `/api/status/tenants` 從 `xero_tenants` 讀取，一次回傳 tenant 資訊 + rate limit 剩餘量

### 不變更的部分

- `/health` 路由維持不變
- `/webhook` 路由維持不變
- `GET /` 首頁維持不變
- 所有 cron / webhook 驅動的同步邏輯不受影響
