// pkg/handler 的路由接線：只註冊路徑，方法判斷留在各自 Handler 以回帶 Allow 的 405。
package handler

import (
	"encoding/json"
	"net/http"
)

// NewMux 註冊三條路徑；非允許方法由各 Handler 回 405，未知路徑由 ServeMux 回 404。
func NewMux(webhook *WebhookHandler, health *HealthHandler, admin *AdminHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhook.Handle)
	mux.HandleFunc("/health", health.Handle)
	mux.HandleFunc("/admin/sync", admin.Handle)
	return mux
}

// writeJSON 寫入 JSON 回應；輸出皆為 map[string]string，編碼不會失敗。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
