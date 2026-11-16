package xero

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultConnectionsURL 是 tenant 清單端點；掛在 api.xero.com 根路徑，不可加 /api.xro/2.0。
const defaultConnectionsURL = "https://api.xero.com/connections"

// connectionsHTTPClient 是查詢 connections 用的 client，一律帶 timeout。
var connectionsHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Connection 是 Xero /connections 回應的單一 tenant；JSON tag 照 Xero 原樣。
type Connection struct {
	TenantID   string `json:"tenantId"`
	TenantName string `json:"tenantName"`
	TenantType string `json:"tenantType"`
}

// GetConnections 以 access token 查詢授權過的 tenant 清單。
func (o *OAuth) GetConnections(ctx context.Context, accessToken string) ([]Connection, error) {
	endpoint := o.ConnectionsURL
	if endpoint == "" {
		endpoint = defaultConnectionsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("建立 connections 請求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := connectionsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("呼叫 Xero connections: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Xero connections 回應 HTTP %d: %s", resp.StatusCode, body)
	}

	var conns []Connection
	if err := json.NewDecoder(resp.Body).Decode(&conns); err != nil {
		return nil, fmt.Errorf("解析 Xero connections: %w", err)
	}
	return conns, nil
}
