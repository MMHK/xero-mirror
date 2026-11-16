// pkg/sync 的 API DTO → store row 映射；純函式，不碰 DB 與網路。
package sync

import (
	"encoding/json"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/xero"
)

// toStoreContacts 把 Xero 聯絡人映射成本地 row；tenantID 與 syncedAt 由呼叫端帶入。
func toStoreContacts(rows []xero.Contact, tenantID string, syncedAt time.Time) []store.Contact {
	out := make([]store.Contact, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Contact{
			TenantID: tenantID, ContactID: r.ContactID,
			Name: strPtr(r.Name), Status: strPtr(r.Status),
			XeroUpdatedAt: timePtr(r.UpdatedDateUTC), SyncedAt: syncedAt,
		})
	}
	return out
}

// toStoreAccounts 把 Xero 科目映射成本地 row。
func toStoreAccounts(rows []xero.Account, tenantID string, syncedAt time.Time) []store.Account {
	out := make([]store.Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Account{
			TenantID: tenantID, AccountID: r.AccountID,
			Code: strPtr(r.Code), Name: strPtr(r.Name),
			Type: strPtr(r.Type), Status: strPtr(r.Status),
			XeroUpdatedAt: timePtr(r.UpdatedDateUTC), SyncedAt: syncedAt,
		})
	}
	return out
}

// toStoreInvoices 把 Xero 發票映射成本地 row；金額保留 Xero 原始十進位字串。
func toStoreInvoices(rows []xero.Invoice, tenantID string, syncedAt time.Time) []store.Invoice {
	out := make([]store.Invoice, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Invoice{
			TenantID: tenantID, InvoiceID: r.InvoiceID,
			Type: strPtr(r.Type), Number: strPtr(r.InvoiceNumber),
			ContactID: strPtr(r.Contact.ContactID), Status: strPtr(r.Status),
			Date: timePtr(r.Date), DueDate: timePtr(r.DueDate),
			Total: amountString(r.Total), AmountDue: amountString(r.AmountDue),
			Currency:      strPtr(r.CurrencyCode),
			XeroUpdatedAt: timePtr(r.UpdatedDateUTC), SyncedAt: syncedAt,
		})
	}
	return out
}

// toStoreCreditNotes 把 Xero 貸項通知映射成本地 row。
func toStoreCreditNotes(rows []xero.CreditNote, tenantID string, syncedAt time.Time) []store.CreditNote {
	out := make([]store.CreditNote, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.CreditNote{
			TenantID: tenantID, CreditNoteID: r.CreditNoteID,
			Type: strPtr(r.Type), Number: strPtr(r.CreditNoteNumber),
			ContactID: strPtr(r.Contact.ContactID), Status: strPtr(r.Status),
			Date: timePtr(r.Date), DueDate: timePtr(r.DueDate),
			Total: amountString(r.Total), AmountDue: amountString(r.AmountDue),
			RemainingCredit: amountString(r.RemainingCredit),
			Currency:        strPtr(r.CurrencyCode),
			XeroUpdatedAt:   timePtr(r.UpdatedDateUTC), SyncedAt: syncedAt,
		})
	}
	return out
}

// toStorePayments 把 Xero 付款映射成本地 row。
func toStorePayments(rows []xero.Payment, tenantID string, syncedAt time.Time) []store.Payment {
	out := make([]store.Payment, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.Payment{
			TenantID: tenantID, PaymentID: r.PaymentID,
			InvoiceNumber: strPtr(r.InvoiceNumber), ContactID: strPtr(r.Contact.ContactID),
			Amount: amountString(r.Amount), Date: timePtr(r.Date),
			Status:        strPtr(r.Status),
			XeroUpdatedAt: timePtr(r.UpdatedDateUTC), SyncedAt: syncedAt,
		})
	}
	return out
}

// strPtr 把空字串視為 NULL，其餘保留原值。
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// amountString 保留 Xero 的十進位字串；缺值給 "0"，避免 DECIMAL 綁空字串失敗。
func amountString(n json.Number) string {
	if n.String() == "" {
		return "0"
	}
	return n.String()
}

// timePtr 把 XeroTime 轉 UTC；零值代表 NULL。
func timePtr(xt xero.XeroTime) *time.Time {
	if xt.IsZero() {
		return nil
	}
	t := xt.UTC()
	return &t
}
