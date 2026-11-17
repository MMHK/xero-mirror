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
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/tests"
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
// resourceId 由 tenantID 衍生：event_id 去重鍵不含 tenant，固定 resourceId 會在重跑時撞鍵。
func webhookBody(seq int, tenantID string) []byte {
	resourceID := "inv-" + tenantID
	return []byte(fmt.Sprintf(`{"lastEventSequence":%d,"entropy":"e","events":[`+
		`{"tenantId":"%s","tenantType":"ORGANISATION","resourceUrl":"https://api.xero.com/api.xro/2.0/Invoices/%s",`+
		`"resourceId":"%s","eventDateUtc":"2025-12-02T00:44:09.923","eventType":"UPDATE","eventCategory":"INVOICE"},`+
		`{"tenantId":"%s","resourceId":"sub-1","eventDateUtc":"2025-12-02T00:44:09.923",`+
		`"eventType":"UPDATE","eventCategory":"SUBSCRIPTION"}]}`, seq, tenantID, resourceID, resourceID, tenantID))
}

// webhookBodyTwoDates 組出同一 batch 內同 resourceId、不同 eventDateUtc 的兩筆 INVOICE。
// resourceId 同樣由 tenantID 衍生，避免跨測試執行撞 event_id 去重鍵。
func webhookBodyTwoDates(tenantID string) []byte {
	resourceID := "inv-" + tenantID
	return []byte(fmt.Sprintf(`{"lastEventSequence":9,"entropy":"e","events":[`+
		`{"tenantId":"%s","resourceType":"INVOICE","resourceId":"%s",`+
		`"eventDateUtc":"2025-12-02T00:44:09.923","eventType":"UPDATE","eventCategory":"INVOICE"},`+
		`{"tenantId":"%s","resourceType":"INVOICE","resourceId":"%s",`+
		`"eventDateUtc":"2025-12-02T00:45:10.001","eventType":"UPDATE","eventCategory":"INVOICE"}]}`,
		tenantID, resourceID, tenantID, resourceID))
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
	tests.RequireTestDatabase(t, dsn)
	tests.LockTestDatabase(t, dsn)
	db, err := store.OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return store.NewStore(db)
}

func TestHandleRejectsBadSignature(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey), nil)
	body := webhookBody(1, "t1")

	rec := sendWebhook(h, body, "not-a-valid-signature")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Empty(t, rec.Body.String(), "401 不得回傳 body")
}

func TestHandleRejectsNonPost(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey), nil)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandleRejectsInvalidJSON(t *testing.T) {
	h := NewWebhookHandler(nil, []byte(testWebhookKey), nil)
	body := []byte("this is not json")

	rec := sendWebhook(h, body, sign(body, testWebhookKey))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Body.String(), "400 不得回傳 body")
}

func TestHandleEnqueuesOncePerEvent(t *testing.T) {
	s := openHandlerStore(t)
	h := NewWebhookHandler(s, []byte(testWebhookKey), nil)
	ctx := context.Background()

	t.Run("重送同一 payload 只入列一次", func(t *testing.T) {
		tenantID := "handler-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		body := webhookBody(7, tenantID)

		require.Equal(t, http.StatusOK, sendWebhook(h, body, sign(body, testWebhookKey)).Code)
		require.Equal(t, http.StatusOK, sendWebhook(h, body, sign(body, testWebhookKey)).Code)

		jobs, err := s.GetPendingSyncJobsByTenant(ctx, tenantID, 10)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "重複事件不得重複入列")
		require.Equal(t, "INVOICES", jobs[0].Entity)
		require.Equal(t, "webhook:UPDATE", jobs[0].Reason)

		events, err := s.CountWebhookEventsByTenant(ctx, tenantID)
		require.NoError(t, err)
		require.Equal(t, 1, events, "重複事件不得重複寫入")
	})

	t.Run("同 batch 同 resourceId 不同 eventDate 仍各自記錄", func(t *testing.T) {
		tenantID := "handler-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		body := webhookBodyTwoDates(tenantID)

		require.Equal(t, http.StatusOK, sendWebhook(h, body, sign(body, testWebhookKey)).Code)

		jobs, err := s.GetPendingSyncJobsByTenant(ctx, tenantID, 10)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "同資源只入列一個 job")
		events, err := s.CountWebhookEventsByTenant(ctx, tenantID)
		require.NoError(t, err)
		require.Equal(t, 2, events, "不同 eventDate 視為不同事件")
	})
}
