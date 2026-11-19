package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// 案例 1：POST /webhook 經 NewMux 打到 webhook，簽章錯回 401，證明路由接線。
func TestMuxRoutesWebhook(t *testing.T) {
	testFS := fstest.MapFS{}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, true),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	body := webhookBody(1, "mux-tenant")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("x-xero-signature", "bad-signature")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 案例 2：GET /health DB 正常回 200（無 DSN 自動 skip）；POST /health 不需 DB 回 405。
func TestMuxRoutesHealth(t *testing.T) {
	testFS := fstest.MapFS{}
	t.Run("DB 正常回 200", func(t *testing.T) {
		s := openHandlerStore(t)
		mux := NewMux(
			nil,
			NewHealthHandler(s),
			NewStatusHandler(nil),
			NewStaticHandler(testFS, false),
			NewSetupAPIHandler(nil, "test-client-id", false),
			NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
			nil, // zohoAuth
		)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
	})
	t.Run("POST 回 405", func(t *testing.T) {
		mux := NewMux(
			nil,
			NewHealthHandler(nil),
			NewStatusHandler(nil),
			NewStaticHandler(testFS, false),
			NewSetupAPIHandler(nil, "test-client-id", false),
			NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
			nil, // zohoAuth
		)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
	})
}

// 案例 3：GET / 回介紹頁 200 text/html，body 含站名。
func TestPageHome(t *testing.T) {
	testFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<html><body>xero-mirror</body></html>`)},
	}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, false),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), "xero-mirror")
}

// 案例 4：GET /swagger 開啟時回 Swagger UI 殼層 200 text/html，body 含掛載點。
func TestPageSwaggerEnabled(t *testing.T) {
	testFS := fstest.MapFS{
		"docs.html": &fstest.MapFile{Data: []byte(`<html><body id="swagger-ui">swagger</body></html>`)},
	}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, true),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	require.Contains(t, rec.Body.String(), `id="swagger-ui"`)
}

// 案例 5：SWAGGER_ENABLED=false 時，GET /swagger 與 /openapi.json 都回 404。
func TestPageSwaggerDisabled(t *testing.T) {
	testFS := fstest.MapFS{
		"docs.html":    &fstest.MapFile{Data: []byte(`<html><body>swagger</body></html>`)},
		"openapi.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.3"}`)},
	}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, false),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	for _, p := range []string{"/swagger", "/openapi.json"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, p)
	}
}

// 案例 6：GET /openapi.json 開啟時回 200 application/json，版本為 3.0.3 且路徑齊全。
func TestPageOpenAPISpec(t *testing.T) {
	testFS := fstest.MapFS{
		"openapi.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.3","paths":{"/health":{"get":{}},"/webhook":{}}}`)},
	}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, true),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var spec map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
	require.Equal(t, "3.0.3", spec["openapi"])
	paths, ok := spec["paths"].(map[string]any)
	require.True(t, ok, "paths 應為物件")
	for _, p := range []string{"/health", "/webhook"} {
		require.Contains(t, paths, p)
	}
	health, ok := paths["/health"].(map[string]any)
	require.True(t, ok, "/health 應為物件")
	require.Contains(t, health, "get")
}

// 案例 7：頁面路由對非 GET 回 405 + Allow: GET（表驅動）。
func TestPageMethodsNotAllowed(t *testing.T) {
	testFS := fstest.MapFS{
		"index.html":   &fstest.MapFile{Data: []byte(`<html><body>home</body></html>`)},
		"docs.html":    &fstest.MapFile{Data: []byte(`<html><body>swagger</body></html>`)},
		"openapi.json": &fstest.MapFile{Data: []byte(`{"openapi":"3.0.3"}`)},
	}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, true),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	for _, p := range []string{"/", "/swagger", "/openapi.json"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, p)
		require.Equal(t, http.MethodGet, rec.Header().Get("Allow"), p)
	}
}

// 案例 8：GET /unknown 回 404，不回介紹頁。
func TestPageUnknownPath(t *testing.T) {
	testFS := fstest.MapFS{}
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, false),
		NewSetupAPIHandler(nil, "test-client-id", false),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		nil, // zohoAuth
	)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "<html")
	require.NotContains(t, rec.Body.String(), "xero-mirror")
}

// 案例：啟用 Zoho Auth 時，/api/setup/config 公開、/api/setup/status 需登入、/setup 頁面 302 到 /login。
func TestMuxRoutesZohoAuthProtection(t *testing.T) {
	testFS := fstest.MapFS{}
	zoho := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})
	mux := NewMux(
		NewWebhookHandler(nil, []byte(testWebhookKey)),
		NewHealthHandler(nil),
		NewStatusHandler(nil),
		NewStaticHandler(testFS, false),
		NewSetupAPIHandler(nil, "test-client-id", true),
		NewXeroAuthHandler(nil, nil, "test-client-id", "test-client-secret"),
		zoho,
	)

	t.Run("config 公開", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/config", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("status 需登入", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("setup 頁面重導到 login", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
		require.Equal(t, http.StatusFound, rec.Code)
		require.Equal(t, "/login", rec.Header().Get("Location"))
	})
}
