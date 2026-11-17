package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// 案例 1：POST /webhook 經 NewMux 打到 webhook，簽章錯回 401，證明路由接線。
func TestMuxRoutesWebhook(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil))
	body := webhookBody(1, "mux-tenant")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("x-xero-signature", "bad-signature")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 案例 2：GET /health DB 正常回 200（無 DSN 自動 skip）；POST /health 不需 DB 回 405。
func TestMuxRoutesHealth(t *testing.T) {
	t.Run("DB 正常回 200", func(t *testing.T) {
		s := openHandlerStore(t)
		mux := NewMux(nil, NewHealthHandler(s), nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	})
	t.Run("POST 回 405", func(t *testing.T) {
		mux := NewMux(nil, NewHealthHandler(nil), nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
	})
}

// 案例 3：GET /admin/sync 回 405；POST 缺 entity（驗證失敗）回 400 且不碰 runner。
func TestMuxRoutesAdminValidation(t *testing.T) {
	mux := NewMux(nil, nil, NewAdminHandler(nil, nil))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/sync", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/sync?tenant=t1", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"error":"tenant 與 entity 為必填且 entity 必須合法"}`, rec.Body.String())
}

// 案例 4：未知 entity 回 400；runner 傳 nil，若驗證沒擋住會在 runner.Run 處 panic 被測試抓到。
func TestAdminRejectsUnknownEntityWithoutRunner(t *testing.T) {
	h := NewAdminHandler(nil, nil)
	rec := httptest.NewRecorder()

	h.Handle(rec, httptest.NewRequest(http.MethodPost, "/admin/sync?tenant=t1&entity=bogus", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
