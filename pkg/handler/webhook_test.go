package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/stretchr/testify/require"
)

// testWebhookKey 是測試固定 signing key，不對應任何真實環境值。
const testWebhookKey = "test-webhook-key"

// sign 依 Xero 規則對 raw body 計算 HMAC-SHA256 的 base64 簽章。
func sign(body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhookBody 組出一份帶一筆 INVOICE 與一筆被略過的 SUBSCRIPTION 的 payload。
func webhookBody(seq int, tenantID string) []byte {
	return []byte(fmt.Sprintf(`{"lastEventSequence":%d,"entropy":"e","events":[`+
		`{"tenantId":"%s","tenantType":"ORGANISATION","resourceUrl":"https://api.xero.com/api.xro/2.0/Invoices/inv-1",`+
		`"resourceId":"inv-1","eventDateUtc":"2025-12-02T00:44:09.923","eventType":"UPDATE","eventCategory":"INVOICE"},`+
		`{"tenantId":"%s","resourceId":"sub-1","eventDateUtc":"2025-12-02T00:44:09.923",`+
		`"eventType":"UPDATE","eventCategory":"SUBSCRIPTION"}]}`, seq, tenantID, tenantID))
}

// sendWebhook 以指定簽章送出 POST /webhook 並回傳 recorder。
func sendWebhook(h *WebhookHandler, body []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("x-xero-signature", signature)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	return rec
}

// openHandlerStore 開啟測試庫並套用 migration；無 TEST_MYSQL_DSN 或非測試庫時跳過。
func openHandlerStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.Contains(cfg.DBName, "_test") {
		t.Skip("TEST_MYSQL_DSN 未指向測試庫")
	}
	db, err := store.OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return store.NewStore(db)
}

func TestHandleRejectsBadSignature(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey))
	body := webhookBody(1, "t1")

	rec := sendWebhook(h, body, "not-a-valid-signature")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, rec.Body.String(), "401 不得回傳 body")
}

func TestHandleRejectsNonPost(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey))
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandleRejectsInvalidJSON(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey))
	body := []byte("this is not json")

	rec := sendWebhook(h, body, sign(body, testWebhookKey))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Body.String(), "400 不得回傳 body")
}

func TestHandleEnqueuesOncePerEvent(t *testing.T) {
	s := openHandlerStore(t)
	tenantID := "handler-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	body := webhookBody(7, tenantID)
	h := NewWebhookHandler(s, []byte(testWebhookKey))

	require.Equal(t, http.StatusOK, sendWebhook(h, body, sign(body, testWebhookKey)).Code)
	require.Equal(t, http.StatusOK, sendWebhook(h, body, sign(body, testWebhookKey)).Code)

	jobs, err := s.GetPendingSyncJobs(context.Background(), 100)
	require.NoError(t, err)
	var matched []store.SyncJob
	for _, j := range jobs {
		if j.TenantID == tenantID {
			matched = append(matched, j)
		}
	}
	require.Len(t, matched, 1, "重複事件不得重複入列")
	require.Equal(t, "INVOICES", matched[0].Entity)
	require.Equal(t, "webhook:UPDATE", matched[0].Reason)
}
