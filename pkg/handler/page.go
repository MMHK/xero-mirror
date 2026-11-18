// pkg/handler 的頁面入口：介紹頁、Swagger UI 與 OpenAPI 文件皆為內嵌靜態資產。
package handler

import (
	"embed"
	"log/slog"
	"net/http"
)

//go:embed home.html docs.html openapi.json
var pageAssets embed.FS

// PageHandler 提供介紹頁、Swagger UI 與 OpenAPI 文件。
type PageHandler struct {
	// swaggerEnabled 控制 /swagger 與 /openapi.json 是否對外提供；false 時回 404。
	swaggerEnabled bool
}

// NewPageHandler 建立頁面 handler；swaggerEnabled 為 false 時 Swagger 相關端點回 404。
func NewPageHandler(swaggerEnabled bool) *PageHandler {
	return &PageHandler{swaggerEnabled: swaggerEnabled}
}

// Handle 只允許 GET；/ 是 catch-all，非 "/" 的路徑回 404。
func (h *PageHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var name, contentType string
	switch r.URL.Path {
	case "/":
		name, contentType = "home.html", "text/html; charset=utf-8"
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
	data, err := pageAssets.ReadFile(name)
	if err != nil {
		slog.Error("read page asset failed", "asset", name, "err", err)
		http.Error(w, "讀取頁面失敗", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
