// pkg/handler 的靜態檔案服務：使用 fs.FS 讀取 web/ 目錄下的 HTML 與 JSON 資產。
package handler

import (
	"io/fs"
	"log/slog"
	"net/http"
)

// StaticHandler 提供靜態頁面與 API 文件服務。
type StaticHandler struct {
	// assets 由外部注入（os.DirFS 或 embed.FS），handler 不依賴具體實作。
	assets fs.FS
	// swaggerEnabled 控制 /swagger 與 /openapi.json 是否對外提供；false 時回 404。
	swaggerEnabled bool
}

// NewStaticHandler 建立靜態檔案 handler；assets 為 web/ 目錄的 fs.FS，swaggerEnabled 為 false 時 Swagger 端點回 404。
func NewStaticHandler(assets fs.FS, swaggerEnabled bool) *StaticHandler {
	return &StaticHandler{
		assets:         assets,
		swaggerEnabled: swaggerEnabled,
	}
}

// Handle 只允許 GET；依路徑對應到對應的靜態檔案，非白名單路徑回 404。
func (h *StaticHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var name, contentType string
	switch r.URL.Path {
	case "/":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/setup":
		name, contentType = "setup.html", "text/html; charset=utf-8"
	case "/swagger":
		if !h.swaggerEnabled {
			http.NotFound(w, r)
			return
		}
		name, contentType = "docs.html", "text/html; charset=utf-8"
	case "/openapi.json":
		if !h.swaggerEnabled {
			http.NotFound(w, r)
			return
		}
		name, contentType = "openapi.json", "application/json; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(h.assets, name)
	if err != nil {
		slog.Error("read static asset failed", "asset", name, "err", err)
		http.Error(w, "讀取頁面失敗", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
