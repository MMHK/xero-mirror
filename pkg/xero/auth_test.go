package xero

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/tests"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const (
	authTestTokenBody = `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":1800,"token_type":"Bearer"}`
	authTestConnsOne  = `[{"tenantId":"tenant-1","tenantName":"測試公司","tenantType":"ORGANISATION"}]`
	authTestConnsTwo  = `[{"tenantId":"tenant-1","tenantName":"測試公司一","tenantType":"ORGANISATION"},` +
		`{"tenantId":"tenant-2","tenantName":"測試公司二","tenantType":"ORGANISATION"}]`
)

// TestHandleOAuthCallback 涵蓋 state 檢核、使用者拒絕與缺少 code 三種回呼。
func TestHandleOAuthCallback(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"state 不符", "/callback?state=bad&code=code-1", false},
		{"使用者拒絕", "/callback?state=state-1&error=access_denied", true},
		{"缺少 code", "/callback?state=state-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codeCh := make(chan string, 1)
			errCh := make(chan error, 1)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)

			handleOAuthCallback("state-1", codeCh, errCh).ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Empty(t, codeCh, "失敗回呼不得送出授權碼")
			if tc.wantErr {
				require.Error(t, <-errCh)
				return
			}
			require.Empty(t, errCh)
		})
	}
}

// TestStoreAuthTenantsStoresTenant 驗證 callback → Exchange → 寫庫的完整授權流程。
func TestStoreAuthTenantsStoresTenant(t *testing.T) {
	st := openAuthTestStore(t)
	ctx := context.Background()
	cleanAuthTenants(t, st, "tenant-1")
	oauth := newAuthTestOAuth(t, authTestTokenBody, authTestConnsOne, "access-1")
	cipher, err := NewCipher(make([]byte, 32))
	require.NoError(t, err)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/callback?state=state-1&code=code-1", nil)
	handleOAuthCallback("state-1", codeCh, errCh).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	tok, err := oauth.Exchange(ctx, <-codeCh)
	require.NoError(t, err)
	require.NoError(t, storeAuthTenants(ctx, st, cipher, oauth, tok))

	tenant, err := st.GetTenant(ctx, "tenant-1")
	require.NoError(t, err)
	require.Equal(t, authStatusActive, tenant.AuthStatus)
	require.NotNil(t, tenant.AccessTokenEnc)
	require.NotEqual(t, "access-1", *tenant.AccessTokenEnc, "token 必須以密文落地")
}

// TestStoreAuthTenantsRejectsMissingRefresh 驗證回應缺 refresh_token 時回錯且不寫 DB。
func TestStoreAuthTenantsRejectsMissingRefresh(t *testing.T) {
	st := openAuthTestStore(t)
	ctx := context.Background()
	cleanAuthTenants(t, st, "tenant-1")
	oauth := newAuthTestOAuth(t, `{"access_token":"access-1","expires_in":1800}`, authTestConnsOne, "access-1")
	tok, err := oauth.Exchange(ctx, "code-1")
	require.NoError(t, err)
	require.Empty(t, tok.RefreshToken)
	cipher, err := NewCipher(make([]byte, 32))
	require.NoError(t, err)

	require.Error(t, storeAuthTenants(ctx, st, cipher, oauth, tok))
	_, err = st.GetTenant(ctx, "tenant-1")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestStoreAuthTenantsWritesMultipleTenants 驗證多 tenant 全部寫入且 token 為密文。
func TestStoreAuthTenantsWritesMultipleTenants(t *testing.T) {
	st := openAuthTestStore(t)
	ctx := context.Background()
	cleanAuthTenants(t, st, "tenant-1", "tenant-2")
	oauth := newAuthTestOAuth(t, authTestTokenBody, authTestConnsTwo, "access-1")
	tok, err := oauth.Exchange(ctx, "code-1")
	require.NoError(t, err)
	cipher, err := NewCipher(make([]byte, 32))
	require.NoError(t, err)

	require.NoError(t, storeAuthTenants(ctx, st, cipher, oauth, tok))

	for _, id := range []string{"tenant-1", "tenant-2"} {
		tenant, err := st.GetTenant(ctx, id)
		require.NoError(t, err)
		require.Equal(t, authStatusActive, tenant.AuthStatus)
		require.NotNil(t, tenant.AccessTokenEnc)
		require.True(t, strings.HasPrefix(*tenant.AccessTokenEnc, "v1:"), "token 必須以密文落地")
	}
}

// TestAwaitAuthorizationCancelClosesServer 驗證等待中取消會回錯且回呼 server 已釋放同一 port。
func TestAwaitAuthorizationCancelClosesServer(t *testing.T) {
	addr := freeLocalAddr(t)
	oauth := &OAuth{ClientID: "cid", RedirectURI: "http://" + addr + "/callback"}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := awaitAuthorization(ctx, oauth, "state-1", io.Discard)
		errCh <- err
	}()

	waitForServer(t, addr)
	cancel()
	require.Error(t, <-errCh)

	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err, "callback server 應已關閉")
	require.NoError(t, ln.Close())
}

// newAuthTestOAuth 回傳指向 mock token／connections 端點的 OAuth；server 於測試結束關閉。
func newAuthTestOAuth(t *testing.T, tokenBody, connsBody, accessToken string) *OAuth {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenBody)
	}))
	t.Cleanup(tokenSrv.Close)
	connSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+accessToken, r.Header.Get("Authorization"))
		fmt.Fprint(w, connsBody)
	}))
	t.Cleanup(connSrv.Close)
	return &OAuth{ClientID: "cid", ClientSecret: "secret", RedirectURI: "http://localhost:3999/callback",
		Endpoint:       oauth2.Endpoint{TokenURL: tokenSrv.URL, AuthStyle: oauth2.AuthStyleInHeader},
		ConnectionsURL: connSrv.URL}
}

// freeLocalAddr 取得 localhost 上一個可用的 host:port，供臨時回呼 server 綁定。
func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// waitForServer 輪詢直到指定位址可連線，確保回呼 server 已開始監聽。
func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("回呼 server 未於期限內啟動: %s", addr)
}

// cleanAuthTenants 先刪除指定 tenant 並註冊結束清理，避免殘留影響斷言。
func cleanAuthTenants(t *testing.T, st *store.Store, ids ...string) {
	t.Helper()
	del := func() {
		for _, id := range ids {
			require.NoError(t, st.DeleteTenant(context.Background(), id))
		}
	}
	del()
	t.Cleanup(del)
}

// openAuthTestStore 開啟測試庫並套用 migration；無 TEST_MYSQL_DSN 或非測試庫時跳過。
func openAuthTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	tests.RequireTestDatabase(t, dsn)
	tests.LockTestDatabase(t, dsn)
	db, err := store.OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return store.NewStore(db)
}
