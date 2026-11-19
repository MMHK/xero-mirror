# Xero App Setup、OAuth Web Flow 與 Zoho Auth Gate

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-19
**狀態**: 🟢 完成
**優先級**: Medium

## 背景與目標

三個相關需求：

1. **Xero App Setup 頁面** — 根據 request 實際 host 顯示 Xero App 配置資訊（webhook URL、OAuth redirect URI）+ 授權狀態
2. **Web-based Xero OAuth Flow** — 讓使用者能透過瀏覽器完成 Xero 授權（而非只用 CLI `auth` subcommand）
3. **Zoho Auth Gate** — 用 Zoho OAuth 保護敏感路由，只有經過 Zoho 身份驗證的使用者才能存取

目前 `auth` subcommand 的流程：
- 啟動臨時 HTTP server 在 `localhost:{port}/callback`
- 使用者在瀏覽器開啟授權 URL → 完成授權 → callback 回到臨時 server
- `storeAuthTenants` 查詢 tenant 清單並寫入 DB

**問題**：部署到正式環境時，臨時 server 無法接收外部 callback。需要把 `/callback` 加入常規 HTTP routes。

---

## 前端架構：REST API + petite-vue

### 為什麼選 petite-vue

- ✅ **CDN 引入**，不需要 bundle（~6KB gzipped）
- ✅ **不需要構建工具**（Vite/Webpack），直接寫 HTML
- ✅ **保留 Vue 語法**，可以實時更新狀態（授權狀態、複製按鈕）
- ✅ **不需要 Go template**，前後端完全分離，後端只提供 JSON API

### 靜態檔案結構

```
web/
├── index.html              # 首頁（petite-vue，從 pkg/handler/home.html 遷移）
├── setup.html              # Xero App Setup（petite-vue，動態載入 API）
├── docs.html               # Swagger UI（純靜態，從 pkg/handler/docs.html 遷移）
└── openapi.json            # 純靜態（從 pkg/handler/openapi.json 遷移）
```

**靜態檔案服務**：
- `pkg/handler/static.go` — 用 `//go:embed web/*` 打包 + `http.FileServer` serve
- 路由：`/` → `index.html`，`/setup` → `setup.html`，`/swagger` → `docs.html`
- 移除 `pkg/handler/page.go`（不再需要 `PageHandler`）
- 移除 `pkg/handler/home.html`、`pkg/handler/docs.html`、`pkg/handler/openapi.json`

### REST API 設計

| Method | Path | 用途 | 受保護 |
|--------|------|------|--------|
| GET | `/api/setup/config` | webhook URL、redirect URI、client ID、Zoho redirect URI | ❌（公開非敏感配置） |
| GET | `/api/setup/status` | 授權狀態（tenants 清單） | ✅ |
| POST | `/api/auth/xero/start` | 啟動 Xero OAuth，返回 authorize URL | ✅ |
| GET | `/callback` | Xero OAuth 回調（Xero 回調，不能是 API） | ❌ |
| GET | `/login` | 啟動 Zoho OAuth（登入入口） | ❌（登入入口） |
| GET | `/oauth/zoho/callback` | Zoho OAuth 回調 | ❌ |
| GET | `/logout` | 清除 session | ❌ |

**API Response 格式**：
```jsonc
// GET /api/setup/config
{
  "webhook_url": "https://example.com/webhook",
  "redirect_uri": "https://example.com/callback",
  "client_id": "xxxxx",
  "zoho_redirect_uri": "https://example.com/oauth/zoho/callback"  // 僅 ZOHO_AUTH_ENABLED=true 時出現
}

// GET /api/setup/status
{
  "tenants": [
    {
      "tenant_id": "tenant-123",
      "tenant_name": "My Company",
      "auth_status": "active",
      "token_expiry": "2026-11-20T10:00:00Z"
    }
  ]
}

// POST /api/auth/xero/start
{
  "authorize_url": "https://login.xero.com/identity/connect/authorize?..."
}
```

### petite-vue 頁面示例

**setup.html**：
```html
<!DOCTYPE html>
<html lang="zh-Hant">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Xero App 配置 — xero-mirror</title>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
</head>
<body class="bg-[#f6f8fa] font-sans text-[#1f2328] antialiased">

<!-- Header 同 home.html -->

<main class="mx-auto max-w-5xl space-y-6 px-6 py-10" v-scope="SetupApp()" @vue:mounted="mounted">

  <!-- 授權狀態：動態載入 -->
  <section class="rounded-lg border border-[#d1d9e0] bg-white p-6">
    <h2 class="text-base font-semibold">授權狀態</h2>
    <div v-if="loading" class="mt-3 text-sm text-[#59636e]">載入中...</div>
    <div v-else-if="!tenants.length" class="mt-3 rounded-md border border-[#d1d9e0] bg-[#f6f8fa] p-4">
      <p class="text-sm text-[#59636e]">尚未授權任何 Xero 組織</p>
      <button @click="startXeroAuth" class="mt-3 rounded-md bg-[#1f2328] px-4 py-2 text-sm font-medium text-white hover:bg-[#383f47]">
        開始授權
      </button>
    </div>
    <div v-else class="mt-3 space-y-3">
      <div v-for="t in tenants" :key="t.tenant_id"
           class="flex items-center justify-between rounded-md border border-[#d1d9e0] bg-[#f6f8fa] p-4">
        <div>
          <p class="font-medium">{{ t.tenant_name }}</p>
          <p class="mt-0.5 font-mono text-xs text-[#59636e]">{{ t.tenant_id }}</p>
        </div>
        <div class="flex items-center gap-3">
          <span v-if="t.auth_status === 'active'"
                class="rounded-full bg-[#dafbe1] px-2 py-0.5 text-xs font-medium text-[#1a7f37]">已授權</span>
          <span v-else
                class="rounded-full bg-[#ffd7d5] px-2 py-0.5 text-xs font-medium text-[#cf222e]">需要重新授權</span>
          <span v-if="t.token_expiry" class="text-xs text-[#59636e]">Token 過期：{{ t.token_expiry }}</span>
        </div>
      </div>
    </div>
  </section>

  <!-- Webhook URL / Redirect URI / Client ID：動態載入 -->
  <section v-for="field in fields" :key="field.key"
           class="rounded-lg border border-[#d1d9e0] bg-white p-6">
    <h2 class="text-base font-semibold">{{ field.label }}</h2>
    <p class="mt-1 text-sm text-[#59636e]">{{ field.desc }}</p>
    <div class="mt-3 flex items-center gap-2">
      <code class="flex-1 rounded-md border border-[#d1d9e0] bg-[#f6f8fa] px-3 py-2 font-mono text-sm">
        {{ config[field.key] || '載入中...' }}
      </code>
      <button @click="copy(field.value, $event)"
              class="rounded-md border border-[#d1d9e0] bg-white px-3 py-2 text-sm font-medium hover:bg-[#f6f8fa]">
        複製
      </button>
    </div>
  </section>

  <!-- Webhook Signing Key 提示 -->
  <section class="rounded-lg border border-[#d1d9e0] bg-[#fff8e5] p-6">
    <h2 class="text-base font-semibold">⚠️ Webhook Signing Key</h2>
    <p class="mt-2 text-sm text-[#59636e]">
      請到 Xero App 設定頁複製此值，並設定到環境變數
      <code class="rounded bg-[#f6f8fa] px-1.5 py-0.5 font-mono text-xs">XERO_WEBHOOK_KEY</code>。
    </p>
  </section>

</main>

<script type="module">
import { createApp } from 'https://unpkg.com/petite-vue?module'

function SetupApp() {
  return {
    loading: true,
    tenants: [],
    config: {},
    fields: [
      { key: 'webhook_url', label: 'Webhook URL', desc: 'Xero 事件推送到此 URL' },
      { key: 'redirect_uri', label: 'Redirect URI', desc: 'OAuth 授權完成後回調到此 URL' },
      { key: 'client_id', label: 'Client ID', desc: '你的 Xero App Client ID' },
    ],
    async mounted() {
      const [cfgRes, statusRes] = await Promise.all([
        fetch('/api/setup/config'),
        fetch('/api/setup/status'),
      ])
      this.config = await cfgRes.json()
      if (this.config.zoho_redirect_uri) {
        this.fields.push({
          key: 'zoho_redirect_uri',
          label: 'Zoho Redirect URI',
          desc: 'Zoho OAuth 授權完成後回調到此 URL（需設定到 Zoho App 的 Authorized Redirect URIs）',
        })
      }
      const status = await statusRes.json()
      this.tenants = status.tenants || []
      this.loading = false
    },
    async startXeroAuth() {
      const res = await fetch('/api/auth/xero/start', { method: 'POST' })
      const data = await res.json()
      if (data.authorize_url) window.location.href = data.authorize_url
    },
    copy(text, e) {
      navigator.clipboard.writeText(text).then(() => {
        const btn = e.target
        const orig = btn.textContent
        btn.textContent = '已複製'
        setTimeout(() => btn.textContent = orig, 1500)
      })
    },
  }
}

createApp({ SetupApp }).mount()
</script>
</body>
</html>
```

---

## 後端設計

### 新增 Routes

| Route | Method | 用途 | Zoho 保護 |
|-------|--------|------|-----------|
| `/api/setup/config` | GET | 回傳 webhook URL、redirect URI、client ID、Zoho redirect URI | ❌（公開非敏感配置） |
| `/api/setup/status` | GET | 回傳 tenant 授權狀態 | ✅ |
| `/api/auth/xero/start` | POST | 產生 state、回傳 authorize URL（動態 redirect URI） | ✅ |
| `/callback` | GET | 接收 Xero 授權回調、驗證 state、exchange code、storeAuthTenants → 302 到 `/setup` | ❌（Xero 回調） |
| `/login` | GET | 產生 state、302 重導到 Zoho authorize URL | ❌（登入入口） |
| `/oauth/zoho/callback` | GET | Zoho 授權回調，驗證身份、設 session cookie、302 到 `/setup` | ❌（Zoho 回調） |
| `/logout` | GET | 清除 session cookie，302 到 `/` | ❌ |

### Host 推演邏輯（`/api/setup/config` 與 `/api/auth/xero/start` 共用）

優先順序（與主流反向代理行為一致）：
1. `X-Forwarded-Host` → scheme 取 `X-Forwarded-Proto`（預設 `https`）
2. 無 forwarded header 時 → `r.Host` + scheme 從 `r.TLS` 判斷

### State 管理（Xero OAuth `/api/auth/xero/start` → `/callback`）

- 使用 `sync.Map` 存 `state → createdAt`
- State 有效期 10 分鐘（與 `authTimeout` 一致）
- Lazy cleanup：每次 `/api/auth/xero/start` 時清理過期 state
- State 用完即刪（`/callback` 驗證成功後刪除）

---

### 架構歸屬

#### 新增檔案

| 檔案 | 說明 |
|------|------|
| `web/index.html` | 首頁（從 `pkg/handler/home.html` 遷移） |
| `web/setup.html` | Xero App Setup（petite-vue，動態載入 API） |
| `web/docs.html` | Swagger UI（從 `pkg/handler/docs.html` 遷移） |
| `web/openapi.json` | 純靜態（從 `pkg/handler/openapi.json` 遷移） |
| `pkg/handler/static.go` | 靜態檔案服務（`//go:embed web/*` + `http.FileServer`） |
| `pkg/handler/setup_api.go` | `SetupAPIHandler` — `/api/setup/config` + `/api/setup/status` |
| `pkg/handler/auth.go` | `XeroAuthHandler` — `/api/auth/xero/start` + `/callback` |
| `pkg/handler/zoho.go` | `ZohoAuthHandler` — `/login` + `/oauth/zoho/callback` + `/logout` + middleware |
| `pkg/handler/host.go` | `ResolveBaseURL(r *http.Request) string` — host 推演共用函數 |
| `pkg/handler/static_test.go` | 測試靜態檔案 serve |
| `pkg/handler/setup_api_test.go` | 測試 API response |
| `pkg/handler/auth_test.go` | 測試 Xero state 管理、callback 流程 |
| `pkg/handler/zoho_test.go` | 測試 Zoho OAuth flow、session cookie、email allowlist |
| `pkg/handler/host_test.go` | 測試 host 推演 |

#### 修改檔案

| 檔案 | 說明 |
|------|------|
| `pkg/handler/mux.go` | 註冊新 routes，受保護 routes 包 Zoho middleware；移除 `PageHandler` |
| `commands.go` | `buildServeDeps` 建立新 handlers |
| `pkg/config/config.go` | 新增 Zoho 相關環境變數 |
| `pkg/xero/auth.go` | 提取 `StoreAuthTenants` 為公開函數 |
| `doc/spec.md` | 記錄新 endpoints 與環境變數 |
| `.env.example` | 新增 Zoho 環境變數範本 |

#### 刪除檔案

| 檔案 | 說明 |
|------|------|
| `pkg/handler/page.go` | 被 `static.go` 取代 |
| `pkg/handler/home.html` | 遷移到 `web/index.html` |
| `pkg/handler/docs.html` | 遷移到 `web/docs.html` |
| `pkg/handler/openapi.json` | 遷移到 `web/openapi.json` |
| `pkg/handler/route_test.go` | 配合 `page.go` 刪除，改用 `static_test.go` |

---

### 關鍵設計：`storeAuthTenants` 提取

現有 `storeAuthTenants` 是 `pkg/xero/auth.go` 內的私有函數。需要提取為公開函數：

```go
// pkg/xero/auth.go
func StoreAuthTenants(ctx context.Context, st *store.Store, cipher *Cipher,
    oauth *OAuth, tok *Token) error {
    // 既有邏輯不變
}
```

`XeroAuthHandler` 在 `/callback` 內呼叫此函數。

### 關鍵設計：動態 Redirect URI

現有 `xero.OAuth.RedirectURI` 是靜態的（從 config 來的 `http://localhost:{port}/callback`）。

在 web flow 中，redirect URI 應該是 `{scheme}://{host}/callback`（從 request 推演）。

**方案**：在 `XeroAuthHandler` 內直接組裝 OAuth URL（用 `url.Values` + `xero.Scope`），不依賴 `xero.OAuth.AuthCodeURL`。這樣：
- 不改動 `xero.OAuth` struct
- 不影響既有 `auth` subcommand
- `/callback` 在 exchange 時也使用動態 redirect URI

### 關鍵設計：Zoho Auth Gate

用 Zoho OAuth 做身份驗證（不存取 Zoho 資料），保護敏感路由。

**流程**：
1. 使用者存取受保護頁面 `/setup` → `RequireAuthRedirect` 檢查 session cookie → 無 cookie → 302 到 `/login`；存取受保護 API → `RequireAuth` → 無 cookie → 回 401
2. 瀏覽器被 302 帶到 `/login`
3. `/login` → 產生 state → 302 重導到 Zoho authorize URL
4. 瀏覽器自動前往 Zoho 登入頁
5. 使用者在 Zoho 登入 → Zoho 回調到 `/oauth/zoho/callback`
6. `/oauth/zoho/callback` → exchange code → 取得 access token → 呼叫 userinfo → 驗證 email domain → 設 signed cookie → 302 到 `/setup`
7. 後續請求帶有效 cookie → middleware 放行

**Session 管理**：
- Signed cookie（HMAC-SHA256），無 server-side state
- Cookie 內容：`email|expiry|signature`
- 有效期：24 小時（可配置）
- Cookie name：`xm_session`

**Email 驗證**：
- `ZOHO_ALLOWED_DOMAINS` 環境變數（逗號分隔）
- 若未設定：允許任何已認證的 Zoho 使用者
- 若設定：只允許列表中的 email

**Zoho OAuth 細節**：
- Auth URL：`https://{accounts_domain}/oauth/v2/auth`
- Token URL：`https://{accounts_domain}/oauth/v2/token`
- Userinfo：`GET https://{accounts_domain}/oauth/user/info`（header: `Authorization: Bearer {token}`）
- Scope：`AaaServer.profile.Read`（最小權限，只取 email）
- Multi-DC：`accounts_domain` 可配置（預設 `accounts.zoho.com`，支援 `.com.cn`/`.eu`/`.in`/`.com.au`）

**不需要新依賴**：全部用 stdlib（`net/http`、`encoding/json`、`crypto/hmac`、`crypto/rand`）

**`ZohoAuthHandler` 結構**：
```go
type ZohoAuthHandler struct {
    clientID       string
    clientSecret   string
    accountsDomain string  // 預設 "accounts.zoho.com"
    allowedDomains map[string]bool  // 空 map = 允許全部
    sessionSecret  []byte  // 32 bytes HMAC key
    sessionMaxAge  time.Duration  // 預設 24h
}
```

**Middleware**：
```go
func (h *ZohoAuthHandler) RequireAuth(next http.Handler) http.Handler
func (h *ZohoAuthHandler) RequireAuthRedirect(next http.Handler) http.Handler
```
- 檢查 `xm_session` cookie
- 驗證 HMAC 簽名 + 未過期
- 驗證 email domain 在 allowlist（若設定）
- `RequireAuth`（API）：驗證失敗 → 回 401 JSON（前端處理跳轉）
- `RequireAuthRedirect`（頁面）：驗證失敗 → 302 重導到 `/login`

**Session Cookie 格式**：
```
xm_session = base64( email + "|" + expiry_unix + "|" + hmac_signature )
```
- Signature = hex(HMAC-SHA256(email + "|" + expiry_unix, sessionSecret))
- 驗證時重新計算 HMAC 比對

### `/callback` 行為（Xero OAuth 回調）

1. 驗證 `state` 存在且未過期（10 分鐘內）
2. 從 `sync.Map` 刪除 state（用完即刪）
3. 若有 `error` query param → 顯示錯誤頁面（靜態 HTML）
4. 用 `code` 呼叫 `oauth.Exchange`
5. 呼叫 `xero.StoreAuthTenants` 寫入 DB
6. 302 跳轉到 `/setup`（前端會自動載入最新狀態）

---

### 安全考量

- `/api/setup/config`：僅回傳非敏感配置（webhook URL／redirect URI／client ID／Zoho redirect URI），公開不需登入；不洩漏 `ClientSecret`、`WebhookKey`、`TokenEncKey`
- `/api/setup/status` 與 `/api/auth/xero/start`：受 Zoho Auth 保護
- `/api/auth/xero/start`：state 用 `crypto/rand` 產生，不可預測；受 Zoho Auth 保護
- `/callback`：驗證 state 存在且未過期；不洩漏 token 細節
- `/login`、`/oauth/zoho/callback`：Zoho OAuth 標準流程，驗證 state 防止 CSRF
- Session cookie：HMAC-SHA256 簽名，防篡改；24 小時過期
- Email allowlist → 改為 email domain allowlist：可限制只有特定網域的 Zoho 帳號能登入

### Zoho 環境變數

| 變數 | 必填 | 說明 |
|------|------|------|
| `ZOHO_AUTH_ENABLED` | 否 | `true` 啟用 Zoho Auth Gate；`false` 或未設定則停用（受保護路由直接放行） |
| `ZOHO_CLIENT_ID` | 啟用時必填 | Zoho OAuth Client ID |
| `ZOHO_CLIENT_SECRET` | 啟用時必填 | Zoho OAuth Client Secret |
| `ZOHO_ACCOUNTS_DOMAIN` | 否 | Zoho Accounts domain（預設 `accounts.zoho.com`） |
| `ZOHO_ALLOWED_DOMAINS` | 否 | 允許登入的 email domain（逗號分隔，空=允許全部已認證使用者） |
| `ZOHO_SESSION_SECRET` | 啟用時必填 | Session cookie 簽名金鑰（32 bytes base64） |

**開關邏輯**：
- `ZOHO_AUTH_ENABLED=true` → 啟動 Auth Gate，缺少必填變數時 `config.Load()` 回錯
- `ZOHO_AUTH_ENABLED=false` 或未設定 → 停用 Auth Gate，受保護路由直接放行（方便本機開發）

---

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 遷移靜態檔案到 `web/`：`home.html` → `web/index.html`、`docs.html` → `web/docs.html`、`openapi.json` → `web/openapi.json` | ✅ 完成 | — | 2026-11-19 |
| 2 | 建立 `web/setup.html`：petite-vue 頁面（動態載入 API） | ✅ 完成 | — | 2026-11-19 |
| 3 | 建立 `pkg/handler/static.go`：`//go:embed web/*` + 靜態檔案 serve | ✅ 完成 | — | 使用 fs.FS 依賴注入 |
| 4 | 刪除 `pkg/handler/page.go` 及相關測試 | ✅ 完成 | — | 2026-11-19 同時更新 mux.go 和 route_test.go |
| 5 | 建立 `pkg/handler/host.go`：`ResolveBaseURL` 共用函數 | ✅ 完成 | — | 2026-11-19 |
| 6 | 建立 `pkg/handler/host_test.go`：測試 host 推演 | ✅ 完成 | — | 2026-11-19 |
| 7 | 提取 `StoreAuthTenants` 為 `xero` package 公開函數 | ✅ 完成 | — | 2026-11-19 |
| 8 | 建立 `pkg/handler/setup_api.go`：`SetupAPIHandler` — `/api/setup/config` + `/api/setup/status` | ✅ 完成 | — | 2026-11-19 |
| 9 | 建立 `pkg/handler/auth.go`：`XeroAuthHandler` — `/api/auth/xero/start` + `/callback` | ✅ 完成 | — | 2026-11-19 |
| 10 | 建立 `pkg/handler/zoho.go`：`ZohoAuthHandler` + middleware | ✅ 完成 | — | 2026-11-19 |
| 11 | 修改 `pkg/config/config.go`：新增 Zoho 環境變數 | ✅ 完成 | — | 2026-11-19 |
| 12 | 建立 `pkg/handler/setup_api_test.go` | ✅ 完成 | — | 2026-11-19 |
| 13 | 建立 `pkg/handler/auth_test.go` | ✅ 完成 | — | 2026-11-19 |
| 14 | 建立 `pkg/handler/zoho_test.go` | ✅ 完成 | — | 2026-11-19 |
| 15 | 建立 `pkg/handler/static_test.go` | ✅ 完成 | — | 2026-11-19 |
| 16 | 修改 `pkg/handler/mux.go`：註冊新 routes，移除 `PageHandler` | ✅ 完成 | — | 2026-11-19 |
| 17 | 修改 `commands.go`：`buildServeDeps` 建立新 handlers | ✅ 完成 | — | 2026-11-19 |
| 18 | 更新 `doc/spec.md`：記錄新 endpoints 與環境變數 | ✅ 完成 | — | 2026-11-19 |
| 19 | 更新 `.env.example`：新增 Zoho 環境變數範本 | ✅ 完成 | — | 2026-11-19 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-19 | ✅ 已批准 | 開始實作 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 參考來源

| 主題 | 來源 | 連結 |
|------|------|------|
| Xero OAuth2 流程 | Xero Developer Docs | https://developer.xero.com/documentation/xero-app-store/oauth2-flow |
| Xero Webhook 設定 | Xero Developer Docs | https://developer.xero.com/documentation/guides/webhooks/configuring-your-server |
| X-Forwarded-Host 慣例 | MDN | https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/X-Forwarded-Host |
| Zoho OAuth 2.0 | Zoho Developer Docs | https://www.zoho.com/developer/oauth/web-server-apps/get-authorization-code.html |
| Zoho Userinfo endpoint | Stack Overflow | https://stackoverflow.com/a/54449818 |
| Zoho Multi-DC | Zoho Developer Docs | https://www.zoho.com/accounts/protocol/oauth/multi-dc/client-authorization.html |
| petite-vue | GitHub | https://github.com/vuejs/petite-vue |
| 專案 auth flow | `pkg/xero/auth.go` | 專案內檔案 |

## 備註

- `/callback` 與 `/api/auth/xero/start` 是**常規 HTTP routes**，與 `auth` subcommand 的臨時 server 並存
- `auth` subcommand 仍可獨立使用（本機開發、CI 環境）
- Web flow 適合正式環境（反向代理後）
- Xero state 管理用 in-memory `sync.Map`，不引入 Redis 或 DB（低流量場景足夠）
- 若後續需要多實例部署，Xero state 需改為 DB 或 Redis 儲存
- **Zoho Auth Gate 為選用**：`ZOHO_AUTH_ENABLED=false` 時，受保護路由直接放行（方便本機開發）
- Zoho OAuth 只用來驗證身份（scope: `AaaServer.profile.Read`），不存取 Zoho 資料
- Session 用 signed cookie，無 server-side state，支援多實例部署
- **前端不需要構建工具**：petite-vue 透過 CDN 引入，HTML 直接部署
