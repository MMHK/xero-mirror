// pkg/handler 的 status API：提供 /api/status/* 的唯讀查詢。
package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
)

// StatusHandler 提供 /api/status/* 的唯讀查詢。
type StatusHandler struct {
	store *store.Store
}

// NewStatusHandler 建立 status handler。
func NewStatusHandler(st *store.Store) *StatusHandler {
	return &StatusHandler{store: st}
}

// HandleSync 處理 GET /api/status/sync：查詢各 tenant × entity 的同步狀態。
func (h *StatusHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tenant := r.URL.Query().Get("tenant")
	entity := r.URL.Query().Get("entity")
	states, err := h.store.ListSyncStates(r.Context(), tenant, entity)
	if err != nil {
		slog.Error("list sync states failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢同步狀態失敗"})
		return
	}
	items := make([]map[string]any, 0, len(states))
	for _, st := range states {
		item := map[string]any{
			"tenant_id": st.TenantID,
			"entity":    st.Entity,
			"status":    st.Status,
		}
		if st.CursorTime != nil {
			item["cursor_time"] = st.CursorTime.UTC().Format(time.RFC3339Nano)
		}
		if st.LastRunAt != nil {
			item["last_run_at"] = st.LastRunAt.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// HandleJobs 處理 GET /api/status/jobs：查詢 sync_jobs 的統計。
func (h *StatusHandler) HandleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	pending, processing, failed, err := h.store.CountSyncJobsByStatus(r.Context())
	if err != nil {
		slog.Error("count sync jobs failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢同步佇列統計失敗"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":    pending,
		"processing": processing,
		"failed":     failed,
		"total":      pending + processing + failed,
	})
}

// HandleTenants 處理 GET /api/status/tenants：查詢全部 tenant 狀態與 rate limit 配額。
func (h *StatusHandler) HandleTenants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	tenants, err := h.store.ListAllTenants(r.Context())
	if err != nil {
		slog.Error("list all tenants failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢 tenant 清單失敗"})
		return
	}
	lastSyncMap, err := h.store.GetLastSyncAtByTenant(r.Context())
	if err != nil {
		slog.Error("get last sync at failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢同步時間失敗"})
		return
	}
	items := make([]map[string]any, 0, len(tenants))
	for _, t := range tenants {
		item := map[string]any{
			"tenant_id":   t.TenantID,
			"tenant_name": t.TenantName,
			"auth_status": t.AuthStatus,
		}
		if t.TokenExpiry != nil {
			item["token_expiry"] = t.TokenExpiry.UTC().Format(time.RFC3339Nano)
		}
		if t.DayLimitRemaining != nil {
			item["day_limit_remaining"] = *t.DayLimitRemaining
		}
		if t.MinuteLimitRemaining != nil {
			item["minute_limit_remaining"] = *t.MinuteLimitRemaining
		}
		if t.AppMinuteLimitRemaining != nil {
			item["app_minute_limit_remaining"] = *t.AppMinuteLimitRemaining
		}
		if t.RateLimitSnapshotAt != nil {
			item["rate_limit_snapshot_at"] = t.RateLimitSnapshotAt.UTC().Format(time.RFC3339Nano)
		}
		if lastRunAt, ok := lastSyncMap[t.TenantID]; ok {
			item["last_sync_at"] = lastRunAt.UTC().Format(time.RFC3339Nano)
		} else {
			item["last_sync_at"] = nil
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// HandleWebhooks 處理 GET /api/status/webhooks：查詢 webhook_events 的處理狀態統計。
func (h *StatusHandler) HandleWebhooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	pending, processed, total, err := h.store.CountWebhookEvents(r.Context())
	if err != nil {
		slog.Error("count webhook events failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢 webhook 事件統計失敗"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":   pending,
		"processed": processed,
		"total":     total,
	})
}
