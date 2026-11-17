package xero

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
)

const (
	// authTimeout 是一次性授權從印出網址到 callback 完成的等待上限。
	authTimeout = 10 * time.Minute
	// authWriteTimeout 是授權完成後寫入 tenant 的等待上限，不與等待授權的時間共用。
	authWriteTimeout = 30 * time.Second
	// authCallbackPath 是臨時回呼 server 唯一接受的路徑，必須與 XERO_REDIRECT_URI 一致。
	authCallbackPath = "/callback"
	// authStateBytes 是 OAuth2 state 的隨機位元組數。
	authStateBytes = 32
	// authReadHeaderTimeout 是臨時回呼 server 讀請求 header 的上限。
	authReadHeaderTimeout = 10 * time.Second
	// authShutdownTimeout 是臨時回呼 server 關閉的等待上限。
	authShutdownTimeout = 5 * time.Second
	// authStatusActive 代表 tenant 已完成授權、可正常同步。
	authStatusActive = "active"
)

// Authorize 執行一次性 OAuth2 授權：產生 state、把授權網址寫到 out、以臨時 server
// 接收 callback，再把 token 加密後逐 tenant 寫入 MySQL。out 為 nil 時寫到 stdout。
// 等待授權受 authTimeout 限制；寫入資料庫改用獨立的 authWriteTimeout。
func Authorize(ctx context.Context, st *store.Store, oauth *OAuth, cipher *Cipher, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	state, err := newAuthState()
	if err != nil {
		return err
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, authTimeout)
	defer cancelWait()
	token, err := awaitAuthorization(waitCtx, oauth, state, out)
	if err != nil {
		return err
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, authWriteTimeout)
	defer cancelWrite()
	if err := storeAuthTenants(writeCtx, st, cipher, oauth, token); err != nil {
		return err
	}
	slog.Info("授權完成，token 已加密寫入 MySQL")
	return nil
}

// newAuthState 以 crypto/rand 產生 OAuth2 state，編成 hex 供 callback 比對。
func newAuthState() (string, error) {
	buf := make([]byte, authStateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("產生 OAuth state: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// awaitAuthorization 把授權網址寫到 out 並啟動臨時 HTTP server 等 callback；
// 收到授權碼後關閉 server 並換取 token，ctx 取消或逾時即回錯。
func awaitAuthorization(ctx context.Context, oauth *OAuth, state string, out io.Writer) (*Token, error) {
	addr, err := callbackAddr(oauth.RedirectURI)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "請在瀏覽器開啟以下網址完成 Xero 授權：\n\n%s\n\n", oauth.AuthCodeURL(state))
	slog.Info("等待授權回呼", "addr", addr)
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handleOAuthCallback(state, codeCh, errCh),
		ReadHeaderTimeout: authReadHeaderTimeout,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sendErr(errCh, err)
		}
	}()
	defer shutdownCallbackServer(srv)
	select {
	case code := <-codeCh:
		return oauth.Exchange(ctx, code)
	case err := <-errCh:
		return nil, fmt.Errorf("授權回呼失敗: %w", err)
	case <-ctx.Done():
		return nil, fmt.Errorf("等待授權回呼逾時: %w", ctx.Err())
	}
}

// callbackAddr 由 XERO_REDIRECT_URI 取出 host:port 作為臨時 server 的監聽位址。
func callbackAddr(redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("XERO_REDIRECT_URI 無法取得監聽位址: %q", redirectURI)
	}
	return u.Host, nil
}

// shutdownCallbackServer 以獨立 timeout 關閉臨時回呼 server，避免關閉流程無限等待。
func shutdownCallbackServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), authShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("關閉授權回呼 server 失敗", "err", err)
	}
}

// handleOAuthCallback 只處理 GET /callback：state 不符回 400 並繼續等待；
// 使用者拒絕或缺 code 回 400 並把錯誤送進 errCh，成功則把 code 送進 codeCh。
func handleOAuthCallback(state string, codeCh chan<- string, errCh chan<- error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(authCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "只允許 GET", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state 不符，拒絕此回呼", http.StatusBadRequest)
			return
		}
		if reason := q.Get("error"); reason != "" {
			http.Error(w, "授權未完成: "+reason, http.StatusBadRequest)
			sendErr(errCh, fmt.Errorf("使用者未完成授權: %s", reason))
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "回呼缺少 code", http.StatusBadRequest)
			sendErr(errCh, errors.New("授權回呼缺少 code"))
			return
		}
		fmt.Fprint(w, "授權完成，請回到終端機確認結果")
		select {
		case codeCh <- code:
		default:
		}
	})
	return mux
}

// sendErr 非阻塞送出 callback 錯誤，避免重複回呼卡住 handler 與關機流程。
func sendErr(errCh chan<- error, err error) {
	select {
	case errCh <- err:
	default:
	}
}

// storeAuthTenants 以 access token 查詢授權的 tenant，逐 tenant 加密 token 並寫入 MySQL；
// 未取得任何 tenant 或缺少 refresh token 都視為授權失敗。
// 寫入失敗時回報已完成數量，方便判斷哪些 tenant 已落地。
func storeAuthTenants(ctx context.Context, st *store.Store, cipher *Cipher,
	oauth *OAuth, tok *Token) error {
	if tok.RefreshToken == "" {
		return errors.New("授權回應未帶 refresh token，無法離線存取；請確認已要求 offline_access")
	}
	conns, err := oauth.GetConnections(ctx, tok.AccessToken)
	if err != nil {
		return fmt.Errorf("查詢 Xero tenant 清單: %w", err)
	}
	if len(conns) == 0 {
		return errors.New("未取得任何 tenant，請確認授權帳號已連結 Xero 組織")
	}
	now := time.Now().UTC()
	for i, c := range conns {
		t, err := newAuthTenant(c, tok, cipher, now)
		if err != nil {
			return fmt.Errorf("準備 tenant %s（已完成 %d/%d 個 tenant）: %w", c.TenantID, i, len(conns), err)
		}
		if err := st.WithTx(ctx, func(tx *sql.Tx) error { return st.UpsertTenant(ctx, tx, t) }); err != nil {
			return fmt.Errorf("寫入 tenant %s（已完成 %d/%d 個 tenant）: %w", c.TenantID, i, len(conns), err)
		}
		slog.Info("tenant 授權完成", "tenant", c.TenantID, "name", c.TenantName)
	}
	return nil
}

// newAuthTenant 組出要寫入的 tenant：token 一律以 AES-256-GCM 密文落地，
// 回應未帶 expires_in／scope 時採預設有效期與本次要求的 scope。
func newAuthTenant(c Connection, tok *Token, cipher *Cipher, now time.Time) (store.Tenant, error) {
	accessEnc, err := cipher.Encrypt(tok.AccessToken)
	if err != nil {
		return store.Tenant{}, fmt.Errorf("加密 access token: %w", err)
	}
	refreshEnc, err := cipher.Encrypt(tok.RefreshToken)
	if err != nil {
		return store.Tenant{}, fmt.Errorf("加密 refresh token: %w", err)
	}
	expiry := tok.Expiry
	if expiry.IsZero() {
		expiry = now.Add(defaultTokenTTL)
	}
	scopes := tok.Scopes
	if scopes == "" {
		scopes = Scope
	}
	return store.Tenant{
		TenantID: c.TenantID, TenantName: c.TenantName,
		AccessTokenEnc: &accessEnc, RefreshTokenEnc: &refreshEnc,
		TokenExpiry: &expiry, Scopes: &scopes, AuthStatus: authStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}
