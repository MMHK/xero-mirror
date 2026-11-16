package xero

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestOAuthRefreshRotatesToken(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAccess string
		wantFresh  string
	}{
		{"回應帶新 refresh token", `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":1800}`, "new-access", "new-refresh"},
		{"回應缺 refresh token 時保留舊值", `{"access_token":"new-access-2","expires_in":1800}`, "new-access-2", "old-refresh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Xero 要求 client_secret_basic：憑證放 Authorization 標頭。
				require.True(t, len(r.Header.Get("Authorization")) >= 6 &&
					r.Header.Get("Authorization")[:6] == "Basic ")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			o := &OAuth{ClientID: "cid", ClientSecret: "secret",
				Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInHeader}}
			tok, err := o.Refresh(context.Background(), "old-refresh")
			require.NoError(t, err)
			require.Equal(t, tc.wantAccess, tok.AccessToken)
			require.Equal(t, tc.wantFresh, tok.RefreshToken)
		})
	}
}

func TestAuthCodeURL(t *testing.T) {
	u, err := url.Parse((&OAuth{ClientID: "cid", RedirectURI: "http://localhost:3000/callback"}).AuthCodeURL("state-1"))
	require.NoError(t, err)
	require.Equal(t, "login.xero.com", u.Host)
	q := u.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "cid", q.Get("client_id"))
	require.Equal(t, "state-1", q.Get("state"))
	require.Contains(t, q.Get("scope"), "offline_access")
}
