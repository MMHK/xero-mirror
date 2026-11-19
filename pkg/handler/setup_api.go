package handler

import (
	"log/slog"
	"net/http"

	"github.com/mmhk/xero-mirror/pkg/store"
)

// SetupAPIHandler 提供 Xero App Setup 頁面的 API endpoints。
type SetupAPIHandler struct {
	store    *store.Store
	clientID string
}

// NewSetupAPIHandler 建立 SetupAPIHandler。
func NewSetupAPIHandler(st *store.Store, clientID string) *SetupAPIHandler {
	return &SetupAPIHandler{store: st, clientID: clientID}
}

// SetupConfigResponse 是 /api/setup/config 的回應格式。
type SetupConfigResponse struct {
	WebhookURL  string `json:"webhook_url"`
	RedirectURI string `json:"redirect_uri"`
	ClientID    string `json:"client_id"`
}

// HandleConfig 處理 GET /api/setup/config，回傳 Xero App 配置資訊。
func (h *SetupAPIHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	baseURL := ResolveBaseURL(r)
	resp := SetupConfigResponse{
		WebhookURL:  baseURL + "/webhook",
		RedirectURI: baseURL + "/callback",
		ClientID:    h.clientID,
	}

	writeJSON(w, http.StatusOK, resp)
}

// TenantStatus 是 /api/setup/status 回應中的單一 tenant 狀態。
type TenantStatus struct {
	TenantID    string `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
	AuthStatus  string `json:"auth_status"`
	TokenExpiry string `json:"token_expiry,omitempty"`
}

// SetupStatusResponse 是 /api/setup/status 的回應格式。
type SetupStatusResponse struct {
	Tenants []TenantStatus `json:"tenants"`
}

// HandleStatus 處理 GET /api/setup/status，回傳 tenant 授權狀態。
func (h *SetupAPIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	tenants, err := h.store.ListAllTenants(r.Context())
	if err != nil {
		slog.Error("list tenants failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢 tenant 清單失敗"})
		return
	}

	statuses := make([]TenantStatus, 0, len(tenants))
	for _, t := range tenants {
		status := TenantStatus{
			TenantID:   t.TenantID,
			TenantName: t.TenantName,
			AuthStatus: t.AuthStatus,
		}
		if t.TokenExpiry != nil {
			status.TokenExpiry = t.TokenExpiry.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		statuses = append(statuses, status)
	}

	writeJSON(w, http.StatusOK, SetupStatusResponse{Tenants: statuses})
}
