// pkg/handler 的 admin 手動觸發：只解析與驗證參數、組 Window，同步一律轉 pkg/sync。
package handler

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
)

// errBadRequest 代表參數驗證失敗，Handle 據此回 400；不帶 wrapper type。
var errBadRequest = errors.New("請求參數不合法")

// adminEntityAliases 是大小寫不敏感的 entity 名稱對照（含單數別名）。
var adminEntityAliases = map[string]pkgsync.Entity{
	"contacts":     pkgsync.EntityContacts,
	"accounts":     pkgsync.EntityAccounts,
	"invoices":     pkgsync.EntityInvoices,
	"invoice":      pkgsync.EntityInvoices,
	"creditnotes":  pkgsync.EntityCreditNotes,
	"credit_notes": pkgsync.EntityCreditNotes,
	"payments":     pkgsync.EntityPayments,
	"payment":      pkgsync.EntityPayments,
}

// AdminHandler 處理 POST /admin/sync：手動觸發單一 tenant／entity 的同步。
type AdminHandler struct {
	runner *pkgsync.Runner
	store  *store.Store
}

// NewAdminHandler 建立 admin handler。
func NewAdminHandler(r *pkgsync.Runner, s *store.Store) *AdminHandler {
	return &AdminHandler{runner: r, store: s}
}

// Handle 只允許 POST；驗證失敗回 400 且不呼叫 runner，同步失敗回 500。
func (h *AdminHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "解析表單失敗"})
		return
	}
	tenant := r.Form.Get("tenant")
	entity, err := parseAdminEntity(r.Form.Get("entity"))
	if tenant == "" || err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tenant 與 entity 為必填且 entity 必須合法"})
		return
	}
	month := r.Form.Get("month")
	if month != "" {
		if _, perr := time.Parse("2006-01", month); perr != nil || len(month) != 7 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "month 格式必須是 YYYY-MM"})
			return
		}
	}
	window, err := h.buildWindow(r.Context(), tenant, string(entity), month)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "讀取同步狀態失敗"})
		return
	}
	if err := h.runner.Run(r.Context(), tenant, entity, window); err != nil {
		slog.Error("admin 手動同步失敗", "tenant", tenant, "entity", entity, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "同步失敗"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// parseAdminEntity 以大小寫不敏感方式對照 entity 名稱；未知值回 errBadRequest。
func parseAdminEntity(raw string) (pkgsync.Entity, error) {
	if e, ok := adminEntityAliases[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return e, nil
	}
	return "", errBadRequest
}

// buildWindow 組同步範圍：month 有值走月度視窗，否則讀 sync_state 取 cursor（首跑為 nil 全量）。
func (h *AdminHandler) buildWindow(ctx context.Context, tenant, entity, month string) (pkgsync.Window, error) {
	if month != "" {
		return monthWindow(month), nil
	}
	st, err := h.store.GetSyncState(ctx, tenant, entity)
	if errors.Is(err, sql.ErrNoRows) {
		return pkgsync.Window{Cursor: nil}, nil
	}
	if err != nil {
		slog.Error("讀取 sync_state 失敗", "tenant", tenant, "entity", entity, "err", err)
		return pkgsync.Window{}, err
	}
	return pkgsync.Window{Cursor: st.CursorTime}, nil
}

// monthWindow 把 YYYY-MM 轉成該月 00:00 UTC 起、下月 00:00 止的半開區間。
func monthWindow(month string) pkgsync.Window {
	start, _ := time.Parse("2006-01", month)
	end := start.AddDate(0, 1, 0)
	return pkgsync.Window{MonthStart: &start, MonthEnd: &end}
}
