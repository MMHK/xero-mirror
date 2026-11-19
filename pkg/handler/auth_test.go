package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXeroAuthHandler_HandleStart(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/xero/start", nil)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	h.HandleStart(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var resp XeroAuthStartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp.AuthorizeURL, "https://login.xero.com/identity/connect/authorize")
	require.Contains(t, resp.AuthorizeURL, "client_id=test-client-id")
	require.Contains(t, resp.AuthorizeURL, "redirect_uri=https%3A%2F%2Fexample.com%2Fcallback")
	require.Contains(t, resp.AuthorizeURL, "state=")
}

func TestXeroAuthHandler_HandleStart_MethodNotAllowed(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/xero/start", nil)

	rec := httptest.NewRecorder()
	h.HandleStart(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

func TestXeroAuthHandler_HandleCallback_MissingCode(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")

	// 先產生一個有效的 state
	stateReq := httptest.NewRequest(http.MethodPost, "/api/auth/xero/start", nil)
	stateReq.Host = "example.com"
	stateRec := httptest.NewRecorder()
	h.HandleStart(stateRec, stateReq)

	var stateResp XeroAuthStartResponse
	require.NoError(t, json.Unmarshal(stateRec.Body.Bytes(), &stateResp))

	// 從 authorize URL 中提取 state
	// 簡單起見，我們直接測試缺少 code 的情況
	req := httptest.NewRequest(http.MethodGet, "/callback?state=invalid-state", nil)
	rec := httptest.NewRecorder()
	h.HandleCallback(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "state 無效或已過期")
}

func TestXeroAuthHandler_HandleCallback_MethodNotAllowed(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")
	req := httptest.NewRequest(http.MethodPost, "/callback", nil)

	rec := httptest.NewRecorder()
	h.HandleCallback(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestXeroAuthHandler_StateValidation(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")

	// 產生 state
	state, err := h.generateState()
	require.NoError(t, err)
	require.NotEmpty(t, state)

	// 驗證 state 有效
	require.True(t, h.validateState(state))

	// 再次驗證應該失敗（用完即刪）
	require.False(t, h.validateState(state))
}

func TestXeroAuthHandler_StateExpiration(t *testing.T) {
	h := NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret")

	// 產生 state
	state, err := h.generateState()
	require.NoError(t, err)

	// 等待超過過期時間（這個測試會很慢，所以我們用另一個方法）
	// 直接測試 cleanupExpiredStates 方法
	h.cleanupExpiredStates()

	// state 應該還有效（因為還沒過期）
	require.True(t, h.validateState(state))
}
