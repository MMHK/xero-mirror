package xero

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
)

const (
	// tokenRefreshMargin 是提前刷新的安全邊界，避免請求剛好跨過到期時間。
	tokenRefreshMargin = 2 * time.Minute
	// defaultTokenTTL 是 Xero 未回 expires_in 時採用的預設有效期。
	defaultTokenTTL = 25 * time.Minute
	// authStatusRequired 是 xero_tenants 需重新授權的狀態值。
	authStatusRequired = "auth_required"
)

// TokenManager 依 tenant 序列化 token 讀取與 refresh，並負責加解密與寫回。
type TokenManager struct {
	store  *store.Store
	cipher *Cipher
	oauth  *OAuth
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

// NewTokenManager 建立管理器；locks 初始化為空 map。
func NewTokenManager(st *store.Store, cipher *Cipher, oauth *OAuth) *TokenManager {
	return &TokenManager{store: st, cipher: cipher, oauth: oauth, locks: make(map[string]*sync.Mutex)}
}

// AccessToken 回傳可用的 access token；過期前 2 分鐘內先單飛 refresh 再寫回。
func (m *TokenManager) AccessToken(ctx context.Context, tenantID string) (string, error) {
	lock := m.tenantLock(tenantID)
	lock.Lock()
	defer lock.Unlock()

	t, err := m.store.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("tenant %s 尚未授權（需重新執行 auth）: %w", tenantID, err)
		}
		return "", fmt.Errorf("讀取 tenant %s 的 token: %w", tenantID, err)
	}

	// 先判 access 有效性：有效就直接回，不必解密 refresh token。
	access := ""
	if t.AccessTokenEnc != nil {
		access, err = m.cipher.Decrypt(*t.AccessTokenEnc)
		if err != nil {
			return "", fmt.Errorf("解密 tenant %s 的 access token（需重新執行 auth）: %w", tenantID, err)
		}
	}
	if access != "" && t.TokenExpiry != nil && t.TokenExpiry.After(time.Now().UTC().Add(tokenRefreshMargin)) {
		return access, nil
	}

	refresh, err := m.decryptRefresh(tenantID, t)
	if err != nil {
		return "", err
	}
	return m.refresh(ctx, tenantID, refresh)
}

// decryptRefresh 解出 refresh token；缺欄位或解密後為空都視為需重新授權。
func (m *TokenManager) decryptRefresh(tenantID string, t store.Tenant) (string, error) {
	if t.RefreshTokenEnc == nil {
		return "", fmt.Errorf("tenant %s 缺少 refresh token，請重新執行 auth", tenantID)
	}
	refresh, err := m.cipher.Decrypt(*t.RefreshTokenEnc)
	if err != nil {
		return "", fmt.Errorf("解密 tenant %s 的 refresh token（需重新執行 auth）: %w", tenantID, err)
	}
	if refresh == "" {
		return "", fmt.Errorf("tenant %s 缺少 refresh token，請重新執行 auth", tenantID)
	}
	return refresh, nil
}

// refresh 以舊 refresh token 換新 token，並在同一交易寫回 access + refresh + expiry。
func (m *TokenManager) refresh(ctx context.Context, tenantID, refreshToken string) (string, error) {
	tok, err := m.oauth.Refresh(ctx, refreshToken)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			slog.Error("tenant authorization expired, re-authorization required", "tenant", tenantID)
			m.markAuthRequired(ctx, tenantID)
			return "", fmt.Errorf("tenant %s 授權已失效，請重新執行 xero-mirror auth", tenantID)
		}
		return "", fmt.Errorf("刷新 tenant %s 的 token: %w", tenantID, err)
	}
	// 新版 refresh token 為空代表回應沒帶、舊值也空，寫回只會存空字串。
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("tenant %s 缺少 refresh token，請重新執行 auth", tenantID)
	}
	accessEnc, err := m.cipher.Encrypt(tok.AccessToken)
	if err != nil {
		return "", fmt.Errorf("加密 tenant %s 的 access token: %w", tenantID, err)
	}
	refreshEnc, err := m.cipher.Encrypt(tok.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("加密 tenant %s 的 refresh token: %w", tenantID, err)
	}
	now := time.Now().UTC()
	expiry := tok.Expiry
	if expiry.IsZero() {
		expiry = now.Add(defaultTokenTTL)
		slog.Warn("token missing expiry time, using default 25 minutes", "tenant", tenantID)
	}
	err = m.store.WithTx(ctx, func(tx *sql.Tx) error {
		return m.store.UpdateTenantTokens(ctx, tx, tenantID, &accessEnc, &refreshEnc, expiry, now)
	})
	if err != nil {
		return "", fmt.Errorf("寫回 tenant %s 的 token: %w", tenantID, err)
	}
	return tok.AccessToken, nil
}

// markAuthRequired 在交易內把 tenant 標記為需重新授權；寫入失敗只告警，不掩蓋授權錯誤。
func (m *TokenManager) markAuthRequired(ctx context.Context, tenantID string) {
	err := m.store.WithTx(ctx, func(tx *sql.Tx) error {
		return m.store.UpdateTenantAuthStatus(ctx, tx, tenantID, authStatusRequired)
	})
	if err != nil {
		slog.Warn("mark tenant authorization status failed", "tenant", tenantID, "err", err)
	}
}

// tenantLock 回傳該 tenant 專屬的鎖；m.mu 只保護 map，不持鎖做網路呼叫。
func (m *TokenManager) tenantLock(tenantID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[tenantID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[tenantID] = l
	}
	return l
}
