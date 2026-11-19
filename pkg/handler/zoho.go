package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// zohoStateMaxAge 是 Zoho OAuth state 的最大有效期。
	zohoStateMaxAge = 10 * time.Minute
	// zohoStateBytes 是 state 的隨機位元組數。
	zohoStateBytes = 32
	// zohoSessionCookieName 是 session cookie 的名稱。
	zohoSessionCookieName = "xm_session"
	// zohoSessionMaxAge 是 session cookie 的最大有效期（預設 24 小時）。
	zohoSessionMaxAge = 24 * time.Hour
)

// zohoStateEntry 記錄 state 的建立時間。
type zohoStateEntry struct {
	createdAt time.Time
}

// ZohoAuthHandler 處理 Zoho OAuth 身份驗證。
type ZohoAuthHandler struct {
	clientID       string
	clientSecret   string
	accountsDomain string
	allowedDomains map[string]bool
	sessionSecret  []byte
	sessionMaxAge  time.Duration
	states         sync.Map // state -> *zohoStateEntry
}

// ZohoAuthConfig 是 ZohoAuthHandler 的配置。
type ZohoAuthConfig struct {
	ClientID       string
	ClientSecret   string
	AccountsDomain string
	AllowedDomains []string
	SessionSecret  []byte
	SessionMaxAge  time.Duration
}

// NewZohoAuthHandler 建立 ZohoAuthHandler。
func NewZohoAuthHandler(cfg ZohoAuthConfig) *ZohoAuthHandler {
	allowed := make(map[string]bool)
	for _, domain := range cfg.AllowedDomains {
		allowed[domain] = true
	}
	maxAge := cfg.SessionMaxAge
	if maxAge == 0 {
		maxAge = zohoSessionMaxAge
	}
	return &ZohoAuthHandler{
		clientID:       cfg.ClientID,
		clientSecret:   cfg.ClientSecret,
		accountsDomain: cfg.AccountsDomain,
		allowedDomains: allowed,
		sessionSecret:  cfg.SessionSecret,
		sessionMaxAge:  maxAge,
	}
}

// zohoTokenResponse 是 Zoho token endpoint 的回應。
type zohoTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Location     string `json:"location"`
}

// zohoUserInfoResponse 是 Zoho userinfo endpoint 的回應。
type zohoUserInfoResponse struct {
	ZUID        int64  `json:"ZUID"`
	FirstName   string `json:"First_Name"`
	LastName    string `json:"Last_Name"`
	DisplayName string `json:"Display_Name"`
	Email       string `json:"Email"`
}

// HandleLogin 處理 GET /login，啟動 Zoho OAuth 流程。
func (h *ZohoAuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// 清理過期 state
	h.cleanupExpiredStates()

	// 產生 state
	state, err := h.generateState()
	if err != nil {
		slog.Error("generate zoho state failed", "err", err)
		http.Error(w, "產生 state 失敗", http.StatusInternalServerError)
		return
	}

	// 組裝 authorize URL
	baseURL := ResolveBaseURL(r)
	redirectURI := baseURL + "/oauth/zoho/callback"
	authorizeURL := h.buildAuthorizeURL(state, redirectURI)

	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// HandleCallback 處理 GET /oauth/zoho/callback，接收 Zoho 授權回調。
func (h *ZohoAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	errParam := q.Get("error")

	// 檢查 error 參數
	if errParam != "" {
		slog.Warn("zoho auth denied", "error", errParam)
		http.Error(w, "授權被拒絕："+errParam, http.StatusBadRequest)
		return
	}

	// 驗證 state
	if !h.validateState(state) {
		slog.Warn("invalid or expired zoho state", "state", state)
		http.Error(w, "state 無效或已過期", http.StatusBadRequest)
		return
	}

	// 檢查 code
	if code == "" {
		http.Error(w, "缺少 authorization code", http.StatusBadRequest)
		return
	}

	// Exchange code for token
	baseURL := ResolveBaseURL(r)
	redirectURI := baseURL + "/oauth/zoho/callback"
	tokenResp, err := h.exchangeCode(r.Context(), code, redirectURI)
	if err != nil {
		slog.Error("exchange zoho code failed", "err", err)
		http.Error(w, "交換 token 失敗", http.StatusInternalServerError)
		return
	}

	// Get user info
	userInfo, err := h.getUserInfo(r.Context(), tokenResp.AccessToken)
	if err != nil {
		slog.Error("get zoho user info failed", "err", err)
		http.Error(w, "取得使用者資訊失敗", http.StatusInternalServerError)
		return
	}

	// Check email domain allowlist
	if len(h.allowedDomains) > 0 {
		emailDomain := extractEmailDomain(userInfo.Email)
		if emailDomain == "" || !h.allowedDomains[emailDomain] {
			slog.Warn("zoho email domain not allowed", "email", userInfo.Email, "domain", emailDomain)
			http.Error(w, "此 email domain 不在允許清單中", http.StatusForbidden)
			return
		}
	}

	// Set session cookie
	if err := h.setSessionCookie(w, userInfo.Email); err != nil {
		slog.Error("set session cookie failed", "err", err)
		http.Error(w, "設定 session 失敗", http.StatusInternalServerError)
		return
	}

	// Redirect to setup page
	http.Redirect(w, r, "/setup", http.StatusFound)
}

// HandleLogout 處理 GET /logout，清除 session cookie。
func (h *ZohoAuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     zohoSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

// RequireAuth 是 middleware，檢查 session cookie 是否有效。
func (h *ZohoAuthHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, err := h.validateSessionCookie(r)
		if err != nil {
			slog.Debug("require auth failed", "err", err)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未授權"})
			return
		}
		slog.Debug("auth required passed", "email", email)
		next.ServeHTTP(w, r)
	})
}

// generateState 產生隨機 state。
func (h *ZohoAuthHandler) generateState() (string, error) {
	buf := make([]byte, zohoStateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)
	h.states.Store(state, &zohoStateEntry{createdAt: time.Now()})
	return state, nil
}

// validateState 驗證 state 是否有效。
func (h *ZohoAuthHandler) validateState(state string) bool {
	if state == "" {
		return false
	}
	val, ok := h.states.Load(state)
	if !ok {
		return false
	}
	entry := val.(*zohoStateEntry)
	if time.Since(entry.createdAt) > zohoStateMaxAge {
		h.states.Delete(state)
		return false
	}
	h.states.Delete(state)
	return true
}

// cleanupExpiredStates 清理過期的 state。
func (h *ZohoAuthHandler) cleanupExpiredStates() {
	now := time.Now()
	h.states.Range(func(key, value any) bool {
		entry := value.(*zohoStateEntry)
		if now.Sub(entry.createdAt) > zohoStateMaxAge {
			h.states.Delete(key)
		}
		return true
	})
}

// buildAuthorizeURL 組裝 Zoho authorize URL。
func (h *ZohoAuthHandler) buildAuthorizeURL(state, redirectURI string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", h.clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "AaaServer.profile.Read")
	params.Set("state", state)
	params.Set("access_type", "online")
	return fmt.Sprintf("https://%s/oauth/v2/auth?%s", h.accountsDomain, params.Encode())
}

// exchangeCode 以 code 換取 token。
func (h *ZohoAuthHandler) exchangeCode(ctx context.Context, code, redirectURI string) (*zohoTokenResponse, error) {
	params := url.Values{}
	params.Set("code", code)
	params.Set("client_id", h.clientID)
	params.Set("client_secret", h.clientSecret)
	params.Set("redirect_uri", redirectURI)
	params.Set("grant_type", "authorization_code")

	tokenURL := fmt.Sprintf("https://%s/oauth/v2/token", h.accountsDomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tokenResp zohoTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// getUserInfo 以 access token 取得使用者資訊。
func (h *ZohoAuthHandler) getUserInfo(ctx context.Context, accessToken string) (*zohoUserInfoResponse, error) {
	userInfoURL := fmt.Sprintf("https://%s/oauth/user/info", h.accountsDomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo endpoint returned %d: %s", resp.StatusCode, body)
	}

	var userInfo zohoUserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

// setSessionCookie 設定 session cookie。
func (h *ZohoAuthHandler) setSessionCookie(w http.ResponseWriter, email string) error {
	expiry := time.Now().Add(h.sessionMaxAge)
	value := h.signSession(email, expiry)

	http.SetCookie(w, &http.Cookie{
		Name:     zohoSessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(h.sessionMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return nil
}

// validateSessionCookie 驗證 session cookie。
func (h *ZohoAuthHandler) validateSessionCookie(r *http.Request) (string, error) {
	cookie, err := r.Cookie(zohoSessionCookieName)
	if err != nil {
		return "", err
	}

	email, expiry, err := h.parseSession(cookie.Value)
	if err != nil {
		return "", err
	}

	if time.Now().After(expiry) {
		return "", errors.New("session expired")
	}

	return email, nil
}

// signSession 簽名 session。
func (h *ZohoAuthHandler) signSession(email string, expiry time.Time) string {
	payload := email + "|" + fmt.Sprintf("%d", expiry.Unix())
	mac := hmac.New(sha256.New, h.sessionSecret)
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))
	return base64.StdEncoding.EncodeToString([]byte(payload+"|"+signature))
}

// parseSession 解析 session。
func (h *ZohoAuthHandler) parseSession(value string) (string, time.Time, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", time.Time{}, err
	}

	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return "", time.Time{}, errors.New("invalid session format")
	}

	email := parts[0]
	expiryUnix := parts[1]
	signature := parts[2]

	// Verify signature
	payload := email + "|" + expiryUnix
	mac := hmac.New(sha256.New, h.sessionSecret)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return "", time.Time{}, errors.New("invalid signature")
	}

	var expiryUnixInt int64
	if _, err := fmt.Sscanf(expiryUnix, "%d", &expiryUnixInt); err != nil {
		return "", time.Time{}, err
	}

	return email, time.Unix(expiryUnixInt, 0), nil
}

// extractEmailDomain 從 email 地址中提取 domain 部分。
// 例如：user@example.com -> example.com
func extractEmailDomain(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}
