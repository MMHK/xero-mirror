// pkg/handler 的路由接線：只註冊路徑，方法判斷留在各自 Handler 以回帶 Allow 的 405。
package handler

import (
	"encoding/json"
	"net/http"
)

// NewMux 註冊 webhook／health／status／setup API／auth 與靜態檔案；
// 非允許方法由各 Handler 回 405，未知路徑由 StaticHandler 的 catch-all 回 404。
// zohoAuth 可為 nil，表示不啟用 Zoho Auth Gate。
func NewMux(
	webhook *WebhookHandler,
	health *HealthHandler,
	status *StatusHandler,
	static *StaticHandler,
	setupAPI *SetupAPIHandler,
	xeroAuth *XeroAuthHandler,
	zohoAuth *ZohoAuthHandler,
) *http.ServeMux {
	mux := http.NewServeMux()

	// 公開 routes（不受 Zoho Auth 保護）
	// - /webhook: Xero 伺服器打進來的，不應該有 auth
	// - /health: 健康檢查，監控系統需要存取
	mux.HandleFunc("/webhook", webhook.Handle)
	mux.HandleFunc("/health", health.Handle)

	// 靜態檔案（公開）
	mux.HandleFunc("/", static.Handle)
	mux.HandleFunc("/swagger", static.Handle)
	mux.HandleFunc("/openapi.json", static.Handle)

	// /setup 頁面：啟用 Zoho Auth 時需登入才能存取
	if zohoAuth != nil {
		mux.Handle("/setup", zohoAuth.RequireAuthRedirect(http.HandlerFunc(static.Handle)))
	} else {
		mux.HandleFunc("/setup", static.Handle)
	}

	// Setup 配置 API（公開）：只回傳 webhook URL／redirect URI／client ID 等非敏感配置，不需要登入
	mux.HandleFunc("/api/setup/config", setupAPI.HandleConfig)

	// Xero OAuth routes（不受 Zoho Auth 保護，因為是 Xero 回調）
	mux.HandleFunc("/callback", xeroAuth.HandleCallback)

	// Zoho OAuth routes（不受 Zoho Auth 保護，因為是登入入口和回調）
	if zohoAuth != nil {
		mux.HandleFunc("/login", zohoAuth.HandleLogin)
		mux.HandleFunc("/oauth/zoho/callback", zohoAuth.HandleCallback)
		mux.HandleFunc("/logout", zohoAuth.HandleLogout)
	}

	// 受保護的 routes
	if zohoAuth != nil {
		// Status API（敏感資訊）
		mux.Handle("/api/status/sync", zohoAuth.RequireAuth(http.HandlerFunc(status.HandleSync)))
		mux.Handle("/api/status/jobs", zohoAuth.RequireAuth(http.HandlerFunc(status.HandleJobs)))
		mux.Handle("/api/status/tenants", zohoAuth.RequireAuth(http.HandlerFunc(status.HandleTenants)))
		mux.Handle("/api/status/webhooks", zohoAuth.RequireAuth(http.HandlerFunc(status.HandleWebhooks)))

		// Setup API（授權狀態與觸發授權流程）
		mux.Handle("/api/setup/status", zohoAuth.RequireAuth(http.HandlerFunc(setupAPI.HandleStatus)))
		mux.Handle("/api/auth/xero/start", zohoAuth.RequireAuth(http.HandlerFunc(xeroAuth.HandleStart)))
	} else {
		// 未啟用 Zoho Auth 時，直接註冊
		mux.HandleFunc("/api/status/sync", status.HandleSync)
		mux.HandleFunc("/api/status/jobs", status.HandleJobs)
		mux.HandleFunc("/api/status/tenants", status.HandleTenants)
		mux.HandleFunc("/api/status/webhooks", status.HandleWebhooks)
		mux.HandleFunc("/api/setup/status", setupAPI.HandleStatus)
		mux.HandleFunc("/api/auth/xero/start", xeroAuth.HandleStart)
	}

	return mux
}

// writeJSON 寫入 JSON 回應；v 為可序列化物件，編碼失敗只記錄不中斷。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
