package xero

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Scope 是本服務要求的最小權限集合（含 offline_access 以取得 refresh token）。
const Scope = "openid profile email accounting.transactions accounting.settings accounting.contacts offline_access"

const (
	// authURL 與 tokenURL 是 Xero 身分服務的固定端點。
	authURL  = "https://login.xero.com/identity/connect/authorize"
	tokenURL = "https://identity.xero.com/connect/token"
)

// OAuth 是 Xero OAuth2 Authorization Code 流程的客戶端設定。
// ConnectionsURL 未填時採 Xero 正式端點（測試可指向 httptest.Server）。
type OAuth struct {
	ClientID       string
	ClientSecret   string
	RedirectURI    string
	ConnectionsURL string
	Endpoint       oauth2.Endpoint
}

// Token 是本服務自己用的 token 模型，避免把 oauth2.Token 洩漏到其他層。
type Token struct {
	AccessToken  string
	RefreshToken string
	Scopes       string
	Expiry       time.Time
}

// AuthCodeURL 產生授權頁網址；state 由呼叫端產生並於回呼時比對。
func (o *OAuth) AuthCodeURL(state string) string {
	return o.config().AuthCodeURL(state)
}

// Exchange 以授權碼換取 token；首次授權時使用。
func (o *OAuth) Exchange(ctx context.Context, code string) (*Token, error) {
	t, err := o.config().Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("以授權碼換取 token: %w", err)
	}
	return o.fromOAuth2Token(t, ""), nil
}

// Refresh 以 refresh token 換新 token；Xero 每次輪換 refresh token，
// 若回應未帶新 refresh token 則保留傳入的舊值。
func (o *OAuth) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	src := o.config().TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	t, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("以 refresh token 換取 token: %w", err)
	}
	return o.fromOAuth2Token(t, refreshToken), nil
}

// fromOAuth2Token 轉換成內建 Token；fallbackRefresh 用於回應缺少 refresh_token 時。
// 舊 refresh token 的保留靠此轉換層，因此 oauth2 版本維持 v0.30.0 即可。
func (o *OAuth) fromOAuth2Token(t *oauth2.Token, fallbackRefresh string) *Token {
	refresh := t.RefreshToken
	if refresh == "" {
		refresh = fallbackRefresh
	}
	scopes, _ := t.Extra("scope").(string)
	return &Token{
		AccessToken:  t.AccessToken,
		RefreshToken: refresh,
		Scopes:       scopes,
		Expiry:       t.Expiry.UTC(),
	}
}

// config 組出 oauth2.Config；Endpoint 未填時採 Xero 預設值。
func (o *OAuth) config() *oauth2.Config {
	ep := o.Endpoint
	if ep.AuthURL == "" {
		ep.AuthURL = authURL
	}
	if ep.TokenURL == "" {
		ep.TokenURL = tokenURL
	}
	if ep.AuthStyle == oauth2.AuthStyleAutoDetect {
		ep.AuthStyle = oauth2.AuthStyleInHeader
	}
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		RedirectURL:  o.RedirectURI,
		Scopes:       strings.Fields(Scope),
		Endpoint:     ep,
	}
}
