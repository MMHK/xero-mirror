package handler

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBaseURL_ForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-Host", "example.com")
	req.Header.Set("X-Forwarded-Proto", "https")

	result := ResolveBaseURL(req)
	require.Equal(t, "https://example.com", result)
}

func TestResolveBaseURL_ForwardedHostOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-Host", "proxy.example.com")
	// 沒有 X-Forwarded-Proto，使用 r.TLS 判斷

	result := ResolveBaseURL(req)
	require.Equal(t, "http://proxy.example.com", result)
}

func TestResolveBaseURL_NoForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Host = "localhost:8080"

	result := ResolveBaseURL(req)
	require.Equal(t, "http://localhost:8080", result)
}

func TestResolveBaseURL_TLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Host = "secure.example.com"
	req.TLS = &tls.ConnectionState{}

	result := ResolveBaseURL(req)
	require.Equal(t, "https://secure.example.com", result)
}

func TestResolveBaseURL_SchemeLowerCase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-Host", "example.com")
	req.Header.Set("X-Forwarded-Proto", "HTTPS")

	result := ResolveBaseURL(req)
	require.Equal(t, "https://example.com", result)
}
