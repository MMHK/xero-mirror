package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestZohoAuthHandler_HandleLogin(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	h.HandleLogin(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	require.Contains(t, location, "https://accounts.zoho.com/oauth/v2/auth")
	require.Contains(t, location, "client_id=test-client-id")
	require.Contains(t, location, "redirect_uri=https%3A%2F%2Fexample.com%2Foauth%2Fzoho%2Fcallback")
	require.Contains(t, location, "scope=AaaServer.profile.Read")
	require.Contains(t, location, "state=")
}

func TestZohoAuthHandler_HandleLogin_MethodNotAllowed(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	h.HandleLogin(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestZohoAuthHandler_HandleLogout(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/", rec.Header().Get("Location"))

	// 檢查 cookie 被清除
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, zohoSessionCookieName, cookies[0].Name)
	require.Equal(t, -1, cookies[0].MaxAge)
}

func TestZohoAuthHandler_HandleLogout_MethodNotAllowed(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	h.HandleLogout(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestZohoAuthHandler_RequireAuth_NoCookie(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	handler := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestZohoAuthHandler_RequireAuth_ValidCookie(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}

	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  secret,
	})

	// 建立有效的 session cookie
	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	expiry := time.Now().Add(1 * time.Hour)
	cookieValue := h.signSession("test@example.com", expiry)
	req.AddCookie(&http.Cookie{
		Name:  zohoSessionCookieName,
		Value: cookieValue,
	})

	handlerCalled := false
	handler := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.True(t, handlerCalled)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestZohoAuthHandler_RequireAuth_ExpiredCookie(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}

	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  secret,
	})

	// 建立過期的 session cookie
	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	expiry := time.Now().Add(-1 * time.Hour)
	cookieValue := h.signSession("test@example.com", expiry)
	req.AddCookie(&http.Cookie{
		Name:  zohoSessionCookieName,
		Value: cookieValue,
	})

	handler := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestZohoAuthHandler_RequireAuth_InvalidSignature(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}

	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  secret,
	})

	// 建立簽名無效的 cookie
	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	req.AddCookie(&http.Cookie{
		Name:  zohoSessionCookieName,
		Value: "invalid-signature",
	})

	handler := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestZohoAuthHandler_StateValidation(t *testing.T) {
	h := NewZohoAuthHandler(ZohoAuthConfig{
		ClientID:       "test-client-id",
		ClientSecret:   "test-client-secret",
		AccountsDomain: "accounts.zoho.com",
		SessionSecret:  make([]byte, 32),
	})

	// 產生 state
	state, err := h.generateState()
	require.NoError(t, err)
	require.NotEmpty(t, state)

	// 驗證 state 有效
	require.True(t, h.validateState(state))

	// 再次驗證應該失敗（用完即刪）
	require.False(t, h.validateState(state))
}
