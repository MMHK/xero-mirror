// Xero Accounting API 的最小 DTO 集合；JSON tag 照 Xero 原樣 PascalCase。
// 金額一律 json.Number（Xero 回數字），轉字串用 .String()，禁止 float64；
// 日期用 XeroTime，零值代表 null，由映射層決定是否落 NULL。
package xero

import "encoding/json"

// ContactRef 是巢狀聯絡人；同步只取 ContactID。
type ContactRef struct {
	ContactID string `json:"ContactID"`
}

// ContactsResponse 是 GET /Contacts 的回應。
type ContactsResponse struct {
	Contacts []Contact `json:"Contacts"`
}

// Contact 是 /Contacts 的單筆資料。
type Contact struct {
	ContactID      string   `json:"ContactID"`
	Name           string   `json:"Name"`
	Status         string   `json:"Status"`
	UpdatedDateUTC XeroTime `json:"UpdatedDateUTC"`
}

// AccountsResponse 是 GET /Accounts 的回應。
type AccountsResponse struct {
	Accounts []Account `json:"Accounts"`
}

// Account 是 /Accounts 的單筆資料。
type Account struct {
	AccountID      string   `json:"AccountID"`
	Code           string   `json:"Code"`
	Name           string   `json:"Name"`
	Type           string   `json:"Type"`
	Status         string   `json:"Status"`
	UpdatedDateUTC XeroTime `json:"UpdatedDateUTC"`
}

// InvoicesResponse 是 GET /Invoices 的回應。
type InvoicesResponse struct {
	Invoices []Invoice `json:"Invoices"`
}

// Invoice 是 /Invoices 的單筆資料。
type Invoice struct {
	InvoiceID      string      `json:"InvoiceID"`
	Type           string      `json:"Type"`
	InvoiceNumber  string      `json:"InvoiceNumber"`
	Contact        ContactRef  `json:"Contact"`
	Status         string      `json:"Status"`
	Date           XeroTime    `json:"Date"`
	DueDate        XeroTime    `json:"DueDate"`
	Total          json.Number `json:"Total"`
	AmountDue      json.Number `json:"AmountDue"`
	CurrencyCode   string      `json:"CurrencyCode"`
	UpdatedDateUTC XeroTime    `json:"UpdatedDateUTC"`
}

// CreditNotesResponse 是 GET /CreditNotes 的回應。
type CreditNotesResponse struct {
	CreditNotes []CreditNote `json:"CreditNotes"`
}

// CreditNote 是 /CreditNotes 的單筆資料。
type CreditNote struct {
	CreditNoteID     string      `json:"CreditNoteID"`
	Type             string      `json:"Type"`
	CreditNoteNumber string      `json:"CreditNoteNumber"`
	Contact          ContactRef  `json:"Contact"`
	Status           string      `json:"Status"`
	Date             XeroTime    `json:"Date"`
	DueDate          XeroTime    `json:"DueDate"`
	Total            json.Number `json:"Total"`
	AmountDue        json.Number `json:"AmountDue"`
	RemainingCredit  json.Number `json:"RemainingCredit"`
	CurrencyCode     string      `json:"CurrencyCode"`
	UpdatedDateUTC   XeroTime    `json:"UpdatedDateUTC"`
}

// PaymentsResponse 是 GET /Payments 的回應。
type PaymentsResponse struct {
	Payments []Payment `json:"Payments"`
}

// Payment 是 /Payments 的單筆資料。
type Payment struct {
	PaymentID      string      `json:"PaymentID"`
	InvoiceNumber  string      `json:"InvoiceNumber"`
	Contact        ContactRef  `json:"Contact"`
	Amount         json.Number `json:"Amount"`
	Date           XeroTime    `json:"Date"`
	Status         string      `json:"Status"`
	UpdatedDateUTC XeroTime    `json:"UpdatedDateUTC"`
}
