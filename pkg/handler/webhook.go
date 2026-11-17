// pkg/handler 接收 Xero webhook：只做驗簽、解析與入列，抓取與寫入一律轉 pkg/sync。
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
)

const (
	// maxWebhookBody 是 raw body 上限（1MB），超過直接回 400。
	maxWebhookBody = 1 << 20
	// eventDateLayout 對應 Xero eventDateUtc（無時區）格式。
	eventDateLayout = "2006-01-02T15:04:05.999"
)

// webhookEntity 把 eventCategory 對映為 pkg/sync 的 Entity 值；
// 未列出的類別（含 SUBSCRIPTION）一律略過，不寫 DB。
var webhookEntity = map[string]string{
	"INVOICE":    string(pkgsync.EntityInvoices),
	"CONTACT":    string(pkgsync.EntityContacts),
	"CREDITNOTE": string(pkgsync.EntityCreditNotes),
	"PAYMENT":    string(pkgsync.EntityPayments),
}

// webhookPayload 是 Xero webhook payload；JSON tag 照 Xero 原樣 camelCase。
type webhookPayload struct {
	FirstEventSequence int            `json:"firstEventSequence"`
	LastEventSequence  int            `json:"lastEventSequence"`
	Entropy            string         `json:"entropy"`
	Events             []webhookEvent `json:"events"`
}

// webhookEvent 是 payload 內的單一事件。
type webhookEvent struct {
	TenantID      string `json:"tenantId"`
	TenantType    string `json:"tenantType"`
	ResourceURL   string `json:"resourceUrl"`
	ResourceID    string `json:"resourceId"`
	EventDateUTC  string `json:"eventDateUtc"`
	EventType     string `json:"eventType"`
	EventCategory string `json:"eventCategory"`
}

// webhookTarget 是解析後待入列的單一事件。
type webhookTarget struct {
	tenantID  string
	entity    string
	xeroID    string
	eventDate time.Time
	eventType string
}

// WebhookHandler 處理 POST /webhook：驗簽、解析、去重入列。
type WebhookHandler struct {
	store      *store.Store
	signingKey []byte
}

// NewWebhookHandler 建立 webhook handler；signingKey 為 Xero webhook signing key。
func NewWebhookHandler(s *store.Store, signingKey []byte) *WebhookHandler {
	return &WebhookHandler{store: s, signingKey: signingKey}
}

// Handle 依 Xero 規則回應：非 POST 405、驗簽失敗 401、body 不合法 400、成功 200。
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := readBody(r)
	if err != nil {
		slog.Warn("讀取 webhook body 失敗", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !h.verifySignature(raw, r.Header.Get("x-xero-signature")) {
		slog.Warn("webhook 驗簽失敗", "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var p webhookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	targets, err := parseTargets(p)
	if err != nil {
		slog.Warn("webhook 事件解析失敗", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := h.enqueue(r.Context(), p.LastEventSequence, targets, time.Now().UTC()); err != nil {
		slog.Error("webhook 入列失敗", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// readBody 讀取 raw body；超過上限視為 body 不合法（驗簽前不得丟棄 raw bytes）。
func readBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		return nil, fmt.Errorf("讀取 webhook body: %w", err)
	}
	if len(raw) > maxWebhookBody {
		return nil, fmt.Errorf("webhook body 超過 %d bytes", maxWebhookBody)
	}
	return raw, nil
}

// verifySignature 以 HMAC-SHA256 對 raw body 計算後 base64，constant-time 比對 header。
func (h *WebhookHandler) verifySignature(raw []byte, signature string) bool {
	mac := hmac.New(sha256.New, h.signingKey)
	mac.Write(raw)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// parseTargets 略過未知 entity 並解析事件時間；時間格式錯誤直接回錯（對應 400）。
func parseTargets(p webhookPayload) ([]webhookTarget, error) {
	targets := make([]webhookTarget, 0, len(p.Events))
	for _, e := range p.Events {
		entity, ok := webhookEntity[e.EventCategory]
		if !ok {
			continue
		}
		d, err := time.Parse(eventDateLayout, e.EventDateUTC)
		if err != nil {
			return nil, fmt.Errorf("解析 webhook eventDateUtc %q: %w", e.EventDateUTC, err)
		}
		targets = append(targets, webhookTarget{
			tenantID:  e.TenantID,
			entity:    entity,
			xeroID:    e.ResourceID,
			eventDate: d.UTC(),
			eventType: e.EventType,
		})
	}
	return targets, nil
}

// enqueue 在同一個交易內逐筆去重寫入 webhook_events，非重複才入列 sync_jobs。
func (h *WebhookHandler) enqueue(ctx context.Context, seq int, targets []webhookTarget, now time.Time) error {
	return h.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, t := range targets {
			date := t.eventDate
			dup, err := h.store.InsertWebhookEvent(ctx, tx, store.WebhookEvent{
				EventID:    eventID(seq, t.xeroID),
				TenantID:   t.tenantID,
				Entity:     t.entity,
				XeroID:     t.xeroID,
				EventDate:  &date,
				ReceivedAt: now,
			})
			if err != nil {
				return err
			}
			if dup {
				continue
			}
			if err := h.store.EnqueueSyncJob(ctx, tx, store.SyncJob{
				TenantID:  t.tenantID,
				Entity:    t.entity,
				XeroID:    t.xeroID,
				Reason:    "webhook:" + t.eventType,
				CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// eventID 以 lastEventSequence 與 resourceId 組合去重：Xero 官方 event 沒有 eventId 欄位，
// 同一事件重送時 seq 與 resourceId 相同，藉此達到去重（取捨：batch 內不同事件共用 seq）。
func eventID(seq int, xeroID string) string {
	return strconv.Itoa(seq) + "_" + xeroID
}
