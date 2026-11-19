package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupAPIHandler_HandleConfig(t *testing.T) {
	h := NewSetupAPIHandler(nil, "test-client-id")
	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	h.HandleConfig(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var resp SetupConfigResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "https://example.com/webhook", resp.WebhookURL)
	require.Equal(t, "https://example.com/callback", resp.RedirectURI)
	require.Equal(t, "test-client-id", resp.ClientID)
}

func TestSetupAPIHandler_HandleConfig_MethodNotAllowed(t *testing.T) {
	h := NewSetupAPIHandler(nil, "test-client-id")
	req := httptest.NewRequest(http.MethodPost, "/api/setup/config", nil)

	rec := httptest.NewRecorder()
	h.HandleConfig(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
}

func TestSetupAPIHandler_HandleConfig_ForwardedHost(t *testing.T) {
	h := NewSetupAPIHandler(nil, "test-client-id")
	req := httptest.NewRequest(http.MethodGet, "/api/setup/config", nil)
	req.Host = "internal.example.com"
	req.Header.Set("X-Forwarded-Host", "public.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	h.HandleConfig(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp SetupConfigResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "https://public.example.com/webhook", resp.WebhookURL)
	require.Equal(t, "https://public.example.com/callback", resp.RedirectURI)
}
