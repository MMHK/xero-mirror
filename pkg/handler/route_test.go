package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// 案例 1：POST /webhook 經 NewMux 打到 webhook，簽章錯回 401，證明路由接線。
func TestMuxRoutesWebhook(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
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
		mux := NewMux(nil, NewHealthHandler(s), nil, NewPageHandler())
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	})
	t.Run("POST 回 405", func(t *testing.T) {
		mux := NewMux(nil, NewHealthHandler(nil), nil, NewPageHandler())
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
	})
}

// 案例 3：GET /admin/sync 回 405；POST 缺 entity（驗證失敗）回 400 且不碰 runner。
func TestMuxRoutesAdminValidation(t *testing.T) {
	mux := NewMux(nil, nil, NewAdminHandler(nil, nil), NewPageHandler())

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

// 案例 5：GET / 回介紹頁 200 text/html，body 含站名。
func TestPageHome(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), "xero-mirror")
}

// 案例 6：GET /docs 回 Swagger UI 殼層 200 text/html，body 含掛載點。
func TestPageDocs(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), `id="swagger-ui"`)
}

// 案例 7：GET /openapi.json 回 200 application/json，版本為 3.0.3 且路徑齊全。
func TestPageOpenAPISpec(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var spec map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
	require.Equal(t, "3.0.3", spec["openapi"])
	paths, ok := spec["paths"].(map[string]any)
	require.True(t, ok, "paths 應為物件")
	for _, p := range []string{"/health", "/webhook", "/admin/sync"} {
		require.Contains(t, paths, p)
	}
	health, ok := paths["/health"].(map[string]any)
	require.True(t, ok, "/health 應為物件")
	require.Contains(t, health, "get")
}

// 案例 8：頁面路由對非 GET 回 405 + Allow: GET（表驅動）。
func TestPageMethodsNotAllowed(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
	for _, p := range []string{"/", "/docs", "/openapi.json"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, p)
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"), p)
	}
}

// 案例 9：GET /unknown 回 404，不回介紹頁。
func TestPageUnknownPath(t *testing.T) {
	mux := NewMux(NewWebhookHandler(nil, []byte(testWebhookKey), nil), NewHealthHandler(nil), NewAdminHandler(nil, nil), NewPageHandler())
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "<html")
	require.NotContains(t, rec.Body.String(), "xero-mirror")
}
