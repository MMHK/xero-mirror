# HTTP 介面補完：Home 介紹頁 + Swagger UI + OpenAPI JSON

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: 🟢 完成（任務 1–8 完成，review 建議已修正）
**優先級**: Medium

## 背景與目標

`serve` 目前只有三個功能端點（`/webhook`、`/health`、`/admin/sync`），瀏覽器開根路徑會拿到 `404`，API 契約只存在 `doc/spec.md`，沒有可互動的文件。本次在不引入新的 Go 依賴、不改既有 handler 行為的前提下補上三件事：

1. `GET /`：專案介紹頁（GitHub 基礎頁面風格）。
2. `GET /docs`：Swagger UI，直接讀 `GET /openapi.json`。
3. `GET /openapi.json`：手寫 OpenAPI 3.0.3，內容與 `doc/spec.md` §7.1 對齊。

**前端技術約束（使用者已決定）**

- Tailwind 以 **CDN** 引入，採 **v4**：`<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>`；不做 npm／建置流程（本專案禁用 npm，且此頁不進 Node 工具鏈）。
- Swagger UI 以 **CDN**（swagger-ui-dist v5）載入，不把 UI 資產放進 repo。
- OpenAPI 文件以**手寫靜態 JSON**內嵌（`//go:embed`），**新增 0 個 Go 依賴**。

**非目標**

- 不做前端框架、不引入 templ／Vue／React、不建立 `web/` 目錄。
- 不動 `/webhook`、`/health`、`/admin/sync` 的既有邏輯與回應格式。
- 不為文件頁加入認證（維持 spec §7.1「`/admin/sync` 無內建認證、只部署於內網」的既有邊界；頁面只描述 API，不繞過限制）。
- 不改 `serve` 的啟動參數、不新增環境變數。

## 設計

### 路由表面（新增）

| 方法 | 路徑 | 用途 | 回應 |
|------|------|------|------|
| `GET` | `/` | 介紹頁 | `200 text/html` |
| `GET` | `/docs` | Swagger UI | `200 text/html`（內含 CDN 資源） |
| `GET` | `/openapi.json` | OpenAPI 3.0.3 | `200 application/json` |

- 非 `GET`：`405` + `Allow: GET`（沿用既有 handler 風格，不加 middleware）。
- `/` 是 `ServeMux` 的 catch-all：`Handle` 內必須先判斷 `r.URL.Path != "/"` 就 `http.NotFound`，避免未知路徑被首頁吃掉而破壞 spec §7.1 的 `404` 規則。
- `/docs` 只作殼層頁（GitHub 風格導覽列 + `<div id="swagger-ui">`），內容由 CDN 的 `SwaggerUIBundle` 以 `url: "/openapi.json"` 繪製。

### 檔案配置（維持既有模組邊界）

```
pkg/handler/
├── page.go          # PageHandler：embed 資產 + 三個路徑的 method/path 判斷
├── home.html        # 介紹頁（Tailwind v4 CDN、GitHub 風格）
├── docs.html        # Swagger UI 殼層（swagger-ui-dist CDN）
├── openapi.json     # 手寫 OpenAPI 3.0.3（//go:embed）
└── mux.go           # 多一個 ui 參數與三條路由註冊
```

- 資產檔放在 `pkg/handler` 根層（不新增子目錄）：`//go:embed` 只能引用相對於 Go 檔的檔案，放同層最單純。
- `page.go` 只做「選檔 + 寫 Content-Type + 寫 body」，不解析參數、不讀 DB，不加 template 引擎（頁面是靜態內容，無需動態資料）。

### `page.go` 介面

```go
// PageHandler 提供介紹頁、Swagger UI 與 OpenAPI 文件。
type PageHandler struct{}

func NewPageHandler() *PageHandler

// Handle 只允許 GET；/ 是 catch-all，非 "/" 的路徑回 404。
func (h *PageHandler) Handle(w http.ResponseWriter, r *http.Request)
```

內部以 `//go:embed home.html docs.html openapi.json` 打包，再依 `r.URL.Path` 分派 `writeAsset(w, contentType, data)`；函數維持 ≤ 40 行。

### 頁面內容（GitHub 基礎風格）

只借用視覺語言，不抄 GitHub 的 logo／文案／圖片，全部內容自寫：

| 元素 | 規格 |
|------|------|
| 底色／卡片 | 頁面 `#f6f8fa`、卡片 `#ffffff`、邊框 `#d1d9e0` |
| 文字 | 主文 `#1f2328`、次要 `#59636e`、連結 `#0969da` |
| 字型 | GitHub 系系統字型堆疊（`-apple-system, Segoe UI, ...`），code 用 `ui-monospace, SFMono-Regular, ...` |
| 導覽列 | 左：`xero-mirror` 字標 ＋「Xero → MySQL」副標；右：`首頁`／`API 文件`／`OpenAPI`／`健康檢查` |
| Hero | 一句定位（Xero → MySQL 單向鏡射）、三～四個 badge（Go 1.24、MySQL 8.4、Apache-2.0、單向同步） |
| 特點區 | 4 張卡片：四種觸發同步、冪等 upsert、憑證加密、軟刪除鏡射 |
| 端點表 | 方法／路徑／說明，`/docs` 與 `/openapi.json` 可點連結 |
| 快速開始 | 灰色 code block：`docker compose up -d` / `xero-mirror auth` / `xero-mirror serve` |
| footer | 註明僅供內網部署、文件在 repo 的 `doc/` |

- 頁面不載入 GitHub 任何外部資產，樣式全靠 Tailwind utility class。
- 不寫「GitHub 出品」或任何商標暗示；純視覺近似。

### `docs.html` 結構

- 頂部同款導覽列（回首頁），下方 `<div id="swagger-ui">`。
- CDN：
  - `https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css`
  - `https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js`
- 初始化：`SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui", deepLinking: true, tryItOutEnabled: false })`（預設不自動送出請求，避免誤觸同步）。

### OpenAPI 內容（與 spec §7.1 對齊）

| 項目 | 內容 |
|------|------|
| 版本 | `openapi: "3.0.3"`（Swagger UI 與多數工具相容性最好） |
| `info` | title `xero-mirror API`、version `0.1.0`、description 說明單向同步與內網部署邊界 |
| `GET /health` | 200 `{"status":"ok"}`、503 `{"status":"unhealthy"}`；schema 為 `status` enum |
| `POST /webhook` | header `x-xero-signature`（必填）；200／400／401／500 皆附說明；requestBody 為 Xero event payload（`events[]` 概要 schema） |
| `POST /admin/sync` | form／query 參數 `tenant`（必填）、`entity`（必填，列舉別名）、`month`（選填 `YYYY-MM`）；200 `{"status":"ok"}`、400／500 `{"error":...}` |
| 不含 | `/docs`、`/openapi.json` 自身（工具頁不入 API 契約） |

## 任務清單

| # | 任務 | 內容 | 狀態 | Sub Agent | 備註 |
|---|------|------|------|-----------|------|
| 1 | 新增 `pkg/handler/openapi.json` | 手寫 OpenAPI 3.0.3，三個端點、錯誤碼、schema 與 spec §7.1 一致 | ✅ 完成 | general | 純資料檔，需 `node -e "JSON.parse(...)"` 驗證 |
| 2 | 新增 `pkg/handler/page.go` | `PageHandler` + `//go:embed`，GET-only、`/` catch-all 的 404 判斷 | ✅ 完成 | general | 依賴任務 1 的資產檔名 |
| 3 | 新增 `pkg/handler/home.html` | Tailwind v4 CDN、GitHub 風格介紹頁（導覽／hero／特點／端點表／快速開始） | ✅ 完成 | general | 內容照「頁面內容」表 |
| 4 | 新增 `pkg/handler/docs.html` | swagger-ui-dist CDN 殼層 + 同款導覽列 | ✅ 完成 | general | `url` 指向 `/openapi.json` |
| 5 | 接線 | `mux.go` 增 `ui *PageHandler` 參數與三條路由；`commands.go` 傳 `handler.NewPageHandler()` | ✅ 完成 | general | 同步修 `route_test.go` 既有 `NewMux` 呼叫 |
| 6 | 測試 | `pkg/handler/route_test.go` 追加 5 個案例：`GET /` 200 html、`GET /docs`、`GET /openapi.json` 合法 JSON 與版本／method 斷言、三條頁面路徑非 GET 405、未知路徑 404 | ✅ 完成 | general | 沿用 testify、無 flaky 風險 |
| 7 | 文件 | `doc/spec.md` §7.1 補三條路由、方法優先序與 UI 說明；`README.md` 補「Web 介面」；本計畫狀態回填 | ✅ 完成 | general | 未改 AGENTS.md 目錄樹 |
| 8 | 審查 | 派 `code-reviewer` 對照本計畫審查；修 M1–M13 後複驗 `go build`／`go vet`／`go test ./...`／`gofmt` | ✅ 完成 | code-reviewer、general | 無 Blocker／Major；修正 11 項 Minor（M8 保留、M3 僅補說明） |

**依賴順序**：1 → 2 → (3、4) → 5 → 6 → 7 → 8。資產檔可與 `page.go` 同批實作，但一次只派一個邏輯任務。

## 驗收方式

```bash
go fmt ./... && go vet ./...
go test ./...                          # 含新增路由案例，不需要網路
go run . serve                         # 需 .env 與 MySQL
# 瀏覽器：http://localhost:8080/       介紹頁
#         http://localhost:8080/docs   Swagger UI（需外網載 CDN）
#         http://localhost:8080/openapi.json
```

- `curl -X POST http://localhost:8080/` 應回 `405` 且 `Allow: GET`。
- `curl http://localhost:8080/nope` 應回 `404`（不是首頁 HTML）。
- 頁面在**無外網**環境會缺樣式（Tailwind／Swagger UI 皆為 CDN），屬已知取捨；不在本次加本地 fallback。

## 風險與取捨

| 風險 | 影響 | 處置 |
|------|------|------|
| Tailwind v4 browser CDN 標示 experimental | 未來 CDN 版本可能變動 | 固定主版本 `@4`；樣式退回醜但可用的原生 HTML |
| 內網無外網時頁面無樣式 | 只影響美觀，功能頁仍可讀 | 文件註明；不加依賴、不內嵌資產 |
| `/` 為 catch-all 吃掉未知路徑 | 破壞 `404` 契約 | `Handle` 明確檢查 `Path`，並由任務 6 的測試守住 |
| 文件頁暴露 `/admin/sync` 參數 | 提高被探測機會 | 沿用既有內網部署邊界；不新增繞過限制的入口 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准 | 使用者批准進入實作；依任務順序 1 → 2 → 3/4 → 5 → 6 → 7 → 8 |
| 2026-11-17 | ✅ 已批准（完成） | 任務 1–8 完成；`go build`／`go vet`／`go test ./...`／`gofmt` 全綠 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer | 無 Blocker／Major；M1–M13 為 Minor（Swagger UI 參數 no-op、spec 外送驗證、entity enum 擋合法輸入、測試斷言不足、spec 方法優先序未定義、首頁 heading 階層等） | 修正 M1、M2、M4、M5、M6、M7、M9、M10、M11、M13，M3 移除 entity enum 並補 eventType 說明；M8 保留（與 README 的 `.exe` 風格一致）；複驗全綠 |
| 2026-11-17 | 使用者 | 首頁應只介紹專案，隱藏 Swagger 說明與其他 API 端點 | 已移除 `home.html` 的導覽列 API 連結、Hero 的 API／OpenAPI 按鈕、整段「HTTP 端點」表格與 footer 的 API 契約字樣；`docs.html` 仍可直開 `/docs`；`go test ./pkg/handler -run TestPage` 全綠 |

## 備註

- 使用者已確認：Tailwind **v4** CDN、OpenAPI **手寫靜態 JSON 內嵌**、Swagger UI **CDN（swagger-ui-dist）**。
- 新增依賴：**無**（`//go:embed` 與 `net/http` 皆 stdlib）。
