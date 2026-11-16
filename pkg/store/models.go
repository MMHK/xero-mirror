package store

import "time"

// DB row: xero_tenants；token 只存 AES-256-GCM 密文。
type Tenant struct {
	TenantID        string
	TenantName      string
	AccessTokenEnc  *string
	RefreshTokenEnc *string
	TokenExpiry     *time.Time
	Scopes          *string
	AuthStatus      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// DB row: sync_state；cursor_time 是增量水位，與資料 upsert 同交易提交。
type SyncState struct {
	TenantID   string
	Entity     string
	CursorTime *time.Time
	LastRunAt  *time.Time
	Status     string
	LastError  *string
}

// DB row: webhook_events；event_id 為主鍵，用來去重。
type WebhookEvent struct {
	EventID     string
	TenantID    string
	Entity      string
	XeroID      string
	EventDate   *time.Time
	ReceivedAt  time.Time
	ProcessedAt *time.Time
}

// DB row: sync_jobs；唯一鍵是 (tenant_id, entity, xero_id)。
type SyncJob struct {
	ID         int64
	TenantID   string
	Entity     string
	XeroID     string
	Reason     string
	Status     string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// DB row: xero_contacts。
type Contact struct {
	TenantID      string
	ContactID     string
	Name          *string
	Status        *string
	XeroUpdatedAt *time.Time
	SyncedAt      time.Time
}

// DB row: xero_accounts。
type Account struct {
	TenantID      string
	AccountID     string
	Code          *string
	Name          *string
	Type          *string
	Status        *string
	XeroUpdatedAt *time.Time
	SyncedAt      time.Time
}

// DB row: xero_invoices；金額以字串進出 DECIMAL(18,4)。
type Invoice struct {
	TenantID      string
	InvoiceID     string
	Type          *string
	Number        *string
	ContactID     *string
	Status        *string
	Date          *time.Time
	DueDate       *time.Time
	Total         string
	AmountDue     string
	Currency      *string
	XeroUpdatedAt *time.Time
	SyncedAt      time.Time
}

// DB row: xero_credit_notes；結構同 invoice 另加 remaining_credit。
type CreditNote struct {
	TenantID        string
	CreditNoteID    string
	Type            *string
	Number          *string
	ContactID       *string
	Status          *string
	Date            *time.Time
	DueDate         *time.Time
	Total           string
	AmountDue       string
	RemainingCredit string
	Currency        *string
	XeroUpdatedAt   *time.Time
	SyncedAt        time.Time
}

// DB row: xero_payments；金額以字串進出 DECIMAL(18,4)。
type Payment struct {
	TenantID      string
	PaymentID     string
	InvoiceNumber *string
	ContactID     *string
	Amount        string
	Date          *time.Time
	Status        *string
	XeroUpdatedAt *time.Time
	SyncedAt      time.Time
}
