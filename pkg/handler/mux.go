// pkg/handler 的路由接線：只註冊路徑，方法判斷留在各自 Handler 以回帶 Allow 的 405。
package handler

import (
	"encoding/json"
	"net/http"
)

// NewMux 註冊 webhook／health／status 與介紹頁／Swagger UI／OpenAPI；
// 非允許方法由各 Handler 回 405，未知路徑由 PageHandler 的 catch-all 回 404。
func NewMux(webhook *WebhookHandler, health *HealthHandler, status *StatusHandler, page *PageHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhook.Handle)
	mux.HandleFunc("/health", health.Handle)
	mux.HandleFunc("/api/status/sync", status.HandleSync)
	mux.HandleFunc("/api/status/jobs", status.HandleJobs)
	mux.HandleFunc("/api/status/tenants", status.HandleTenants)
	mux.HandleFunc("/api/status/webhooks", status.HandleWebhooks)
	mux.HandleFunc("/", page.Handle)
	mux.HandleFunc("/swagger", page.Handle)
	mux.HandleFunc("/openapi.json", page.Handle)
	return mux
}

// writeJSON 寫入 JSON 回應；v 為可序列化物件，編碼失敗只記錄不中斷。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
