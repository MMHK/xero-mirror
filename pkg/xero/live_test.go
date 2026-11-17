// Live Xero 整合測試：opt-in，需設定 TEST_XERO_ACCESS_TOKEN／TEST_XERO_TENANT_ID。
// 未設定時一律 t.Skip，預設 go test ./... 不打真實 API、不消耗配額。
// 只做 GET 唯讀呼叫，每個 run 最多 3 次 API call；錯誤訊息一律不得含 token。
package xero

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireLiveXero 讀取 live 測試憑證；任一為空（含全空白）即 skip，回傳 trim 後的值且不得印進 log。
func requireLiveXero(t *testing.T) (token, tenantID string) {
	t.Helper()
	token = strings.TrimSpace(os.Getenv("TEST_XERO_ACCESS_TOKEN"))
	tenantID = strings.TrimSpace(os.Getenv("TEST_XERO_TENANT_ID"))
	if token == "" || tenantID == "" {
		t.Skip("未設定 TEST_XERO_ACCESS_TOKEN／TEST_XERO_TENANT_ID")
	}
	return token, tenantID
}

// newLiveClient 以固定 token 閉包建立指向正式端點的 client；
// 不走 TokenManager，避免測試碰 DB 與 refresh token 輪換。
func newLiveClient(t *testing.T, token string) *Client {
	t.Helper()
	c, err := NewClientWithTokenSource(defaultBaseURL, func(context.Context, string) (string, error) {
		return token, nil
	})
	require.NoError(t, err)
	return c
}

// TestLiveConnections 驗證 token 有效，且 /connections 回傳含目標 tenant。
func TestLiveConnections(t *testing.T) {
	token, tenantID := requireLiveXero(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conns, err := (&OAuth{}).GetConnections(ctx, token)
	require.NoError(t, err, "呼叫 /connections 失敗，token 可能已過期（效期約 30 分鐘）")

	for _, c := range conns {
		if c.TenantID == tenantID {
			return
		}
	}
	require.Fail(t, "connections 未包含 TEST_XERO_TENANT_ID，token 可能過期或 tenant 不符")
}

// TestLiveInvoicesPage 驗證 /Invoices 第一頁可解析、筆數 ≤100 且每筆有 InvoiceID。
func TestLiveInvoicesPage(t *testing.T) {
	token, tenantID := requireLiveXero(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	body, err := newLiveClient(t, token).Get(ctx, tenantID, "/Invoices", url.Values{"page": {"1"}}, nil)
	require.NoError(t, err, "呼叫 /Invoices 失敗，token 可能已過期")

	var resp InvoicesResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.LessOrEqual(t, len(resp.Invoices), 100)
	for _, inv := range resp.Invoices {
		require.NotEmpty(t, inv.InvoiceID)
	}
}

// TestLiveContactsPage 驗證 /Contacts 第一頁可解析、筆數 ≤100 且每筆有 ContactID。
func TestLiveContactsPage(t *testing.T) {
	token, tenantID := requireLiveXero(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	body, err := newLiveClient(t, token).Get(ctx, tenantID, "/Contacts", url.Values{"page": {"1"}}, nil)
	require.NoError(t, err, "呼叫 /Contacts 失敗，token 可能已過期")

	var resp ContactsResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.LessOrEqual(t, len(resp.Contacts), 100)
	for _, c := range resp.Contacts {
		require.NotEmpty(t, c.ContactID)
	}
}
