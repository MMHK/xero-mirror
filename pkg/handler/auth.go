package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/xero"
)

const (
	// xeroStateMaxAge 是 Xero OAuth state 的最大有效期。
	xeroStateMaxAge = 10 * time.Minute
	// xeroStateBytes 是 state 的隨機位元組數。
	xeroStateBytes = 32
)

// xeroStateEntry 記錄 state 的建立時間，用於過期清理。
type xeroStateEntry struct {
	createdAt time.Time
}

// XeroAuthHandler 處理 Xero OAuth web flow。
type XeroAuthHandler struct {
	store        *store.Store
	cipher       *xero.Cipher
	clientID     string
	clientSecret string
	states       sync.Map // state -> *xeroStateEntry
}

// NewXeroAuthHandler 建立 XeroAuthHandler。
func NewXeroAuthHandler(st *store.Store, cipher *xero.Cipher, clientID, clientSecret string) *XeroAuthHandler {
	return &XeroAuthHandler{
		store:        st,
		cipher:       cipher,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// XeroAuthStartResponse 是 /api/auth/xero/start 的回應格式。
type XeroAuthStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// HandleStart 處理 POST /api/auth/xero/start，產生 state 並回傳 authorize URL。
func (h *XeroAuthHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// 清理過期 state
	h.cleanupExpiredStates()

	// 產生 state
	state, err := h.generateState()
	if err != nil {
		slog.Error("generate state failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "產生 state 失敗"})
		return
	}

	// 組裝 authorize URL
	baseURL := ResolveBaseURL(r)
	redirectURI := baseURL + "/callback"
	authorizeURL := h.buildAuthorizeURL(state, redirectURI)

	writeJSON(w, http.StatusOK, XeroAuthStartResponse{AuthorizeURL: authorizeURL})
}

// HandleCallback 處理 GET /callback，接收 Xero 授權回調。
func (h *XeroAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	errParam := q.Get("error")

	// 檢查 error 參數
	if errParam != "" {
		slog.Warn("xero auth denied by user", "error", errParam)
		http.Error(w, "授權被拒絕："+errParam, http.StatusBadRequest)
		return
	}

	// 驗證 state
	if !h.validateState(state) {
		slog.Warn("invalid or expired state", "state", state)
		http.Error(w, "state 無效或已過期", http.StatusBadRequest)
		return
	}

	// 檢查 code
	if code == "" {
		http.Error(w, "缺少 authorization code", http.StatusBadRequest)
		return
	}

	// 組裝 OAuth config
	baseURL := ResolveBaseURL(r)
	redirectURI := baseURL + "/callback"
	oauth := &xero.OAuth{
		ClientID:     h.clientID,
		ClientSecret: h.clientSecret,
		RedirectURI:  redirectURI,
	}

	// Exchange code for token
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	token, err := oauth.Exchange(ctx, code)
	if err != nil {
		slog.Error("exchange code failed", "err", err)
		http.Error(w, "交換 token 失敗", http.StatusInternalServerError)
		return
	}

	// Store tenants
	if err := xero.StoreAuthTenants(ctx, h.store, h.cipher, oauth, token); err != nil {
		slog.Error("store tenants failed", "err", err)
		http.Error(w, "儲存 tenant 失敗", http.StatusInternalServerError)
		return
	}

	// 重定向到 setup 頁面
	http.Redirect(w, r, "/setup", http.StatusFound)
}

// generateState 產生隨機 state。
func (h *XeroAuthHandler) generateState() (string, error) {
	buf := make([]byte, xeroStateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)
	h.states.Store(state, &xeroStateEntry{createdAt: time.Now()})
	return state, nil
}

// validateState 驗證 state 是否有效（存在且未過期）。
func (h *XeroAuthHandler) validateState(state string) bool {
	if state == "" {
		return false
	}
	val, ok := h.states.Load(state)
	if !ok {
		return false
	}
	entry := val.(*xeroStateEntry)
	if time.Since(entry.createdAt) > xeroStateMaxAge {
		h.states.Delete(state)
		return false
	}
	h.states.Delete(state) // 用完即刪
	return true
}

// cleanupExpiredStates 清理過期的 state。
func (h *XeroAuthHandler) cleanupExpiredStates() {
	now := time.Now()
	h.states.Range(func(key, value any) bool {
		entry := value.(*xeroStateEntry)
		if now.Sub(entry.createdAt) > xeroStateMaxAge {
			h.states.Delete(key)
		}
		return true
	})
}

// buildAuthorizeURL 組裝 Xero authorize URL。
func (h *XeroAuthHandler) buildAuthorizeURL(state, redirectURI string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", h.clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", xero.Scope)
	params.Set("state", state)
	return "https://login.xero.com/identity/connect/authorize?" + params.Encode()
}

// ErrStateInvalid 表示 state 無效或已過期。
var ErrStateInvalid = errors.New("state 無效或已過期")
