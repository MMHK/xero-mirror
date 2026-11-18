// pkg/handler 的健康檢查：只 ping 資料庫，不做業務邏輯。
package handler

import (
	"log/slog"
	"net/http"

	"github.com/mmhk/xero-mirror/pkg/store"
)

// HealthHandler 處理 GET /health：DB 可連回 200，否則回 503。
type HealthHandler struct {
	store *store.Store
}

// NewHealthHandler 建立健康檢查 handler。
func NewHealthHandler(s *store.Store) *HealthHandler {
	return &HealthHandler{store: s}
}

// Handle 只允許 GET；非 GET 回 405 並帶 Allow header。
func (h *HealthHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := h.store.Ping(r.Context()); err != nil {
		slog.Warn("health check failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
