package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const upsertTenantSQL = `
INSERT INTO xero_tenants (
	tenant_id, tenant_name, access_token_enc, refresh_token_enc, token_expiry,
	scopes, auth_status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	tenant_name = VALUES(tenant_name),
	access_token_enc = VALUES(access_token_enc),
	refresh_token_enc = VALUES(refresh_token_enc),
	token_expiry = VALUES(token_expiry),
	scopes = VALUES(scopes),
	auth_status = VALUES(auth_status),
	updated_at = VALUES(updated_at)`

const updateTenantTokensSQL = `
UPDATE xero_tenants
SET access_token_enc = ?, refresh_token_enc = ?, token_expiry = ?, updated_at = ?
WHERE tenant_id = ?`

const updateTenantAuthStatusSQL = `
UPDATE xero_tenants SET auth_status = ?, updated_at = ? WHERE tenant_id = ?`

const selectTenantSQL = `
SELECT tenant_id, tenant_name, access_token_enc, refresh_token_enc, token_expiry,
	scopes, auth_status, created_at, updated_at
FROM xero_tenants WHERE tenant_id = ?`

const listTenantsSQL = `
SELECT tenant_id, tenant_name, access_token_enc, refresh_token_enc, token_expiry,
	scopes, auth_status, created_at, updated_at
FROM xero_tenants WHERE auth_status = 'active' ORDER BY tenant_id`

const deleteTenantSQL = `DELETE FROM xero_tenants WHERE tenant_id = ?`

// upsertSyncStateSQL 保護既有水位：cursor 不得被 NULL 清空，也不得被較舊的值覆蓋。
// GREATEST 遇 NULL 會回 NULL，因此先以 IF 判 NULL 再取較大值；
// 既有為 NULL（如先跑月度回補）時直接採用新值，否則 GREATEST 仍會回 NULL 而永遠寫不進水位。
const upsertSyncStateSQL = `
INSERT INTO sync_state (
	tenant_id, entity, cursor_time, last_run_at, status, last_error
) VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	cursor_time = IF(
		VALUES(cursor_time) IS NULL,
		cursor_time,
		GREATEST(VALUES(cursor_time), IFNULL(cursor_time, VALUES(cursor_time)))
	),
	last_run_at = VALUES(last_run_at),
	status = VALUES(status),
	last_error = VALUES(last_error)`

const selectSyncStateSQL = `
SELECT tenant_id, entity, cursor_time, last_run_at, status, last_error
FROM sync_state WHERE tenant_id = ? AND entity = ?`

const insertWebhookEventSQL = `
INSERT INTO webhook_events (
	event_id, tenant_id, entity, xero_id, event_date, received_at, processed_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE event_id = event_id`

const enqueueSyncJobSQL = `
INSERT INTO sync_jobs (
	tenant_id, entity, xero_id, reason, status, created_at, finished_at
) VALUES (?, ?, ?, ?, 'pending', ?, NULL)
ON DUPLICATE KEY UPDATE
	reason = VALUES(reason),
	status = 'pending',
	finished_at = NULL`

const selectPendingSyncJobsSQL = `
SELECT id, tenant_id, entity, xero_id, reason, status, created_at, finished_at
FROM sync_jobs WHERE status = 'pending'
ORDER BY created_at, id LIMIT ?`

const selectPendingSyncJobsByTenantSQL = `
SELECT id, tenant_id, entity, xero_id, reason, status, created_at, finished_at
FROM sync_jobs WHERE status = 'pending' AND tenant_id = ?
ORDER BY created_at, id LIMIT ?`

const countWebhookEventsByTenantSQL = `
SELECT COUNT(*) FROM webhook_events WHERE tenant_id = ?`

const countInvoicesByTenantSQL = `
SELECT COUNT(*) FROM xero_invoices WHERE tenant_id = ?`

const markSyncJobDoneSQL = `
UPDATE sync_jobs SET status = 'done', finished_at = ? WHERE id = ?`

const upsertContactSQL = `
INSERT INTO xero_contacts (
	tenant_id, contact_id, name, status, xero_updated_at, synced_at
) VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	name = VALUES(name),
	status = VALUES(status),
	xero_updated_at = VALUES(xero_updated_at),
	synced_at = VALUES(synced_at)`

const upsertAccountSQL = `
INSERT INTO xero_accounts (
	tenant_id, account_id, code, name, type, status, xero_updated_at, synced_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	code = VALUES(code),
	name = VALUES(name),
	type = VALUES(type),
	status = VALUES(status),
	xero_updated_at = VALUES(xero_updated_at),
	synced_at = VALUES(synced_at)`

const upsertInvoiceSQL = `
INSERT INTO xero_invoices (
	tenant_id, invoice_id, type, number, contact_id, status, date, due_date,
	total, amount_due, currency, xero_updated_at, synced_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	type = VALUES(type),
	number = VALUES(number),
	contact_id = VALUES(contact_id),
	status = VALUES(status),
	date = VALUES(date),
	due_date = VALUES(due_date),
	total = VALUES(total),
	amount_due = VALUES(amount_due),
	currency = VALUES(currency),
	xero_updated_at = VALUES(xero_updated_at),
	synced_at = VALUES(synced_at)`

const upsertCreditNoteSQL = `
INSERT INTO xero_credit_notes (
	tenant_id, credit_note_id, type, number, contact_id, status, date, due_date,
	total, amount_due, remaining_credit, currency, xero_updated_at, synced_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	type = VALUES(type),
	number = VALUES(number),
	contact_id = VALUES(contact_id),
	status = VALUES(status),
	date = VALUES(date),
	due_date = VALUES(due_date),
	total = VALUES(total),
	amount_due = VALUES(amount_due),
	remaining_credit = VALUES(remaining_credit),
	currency = VALUES(currency),
	xero_updated_at = VALUES(xero_updated_at),
	synced_at = VALUES(synced_at)`

const upsertPaymentSQL = `
INSERT INTO xero_payments (
	tenant_id, payment_id, invoice_number, contact_id, amount, date, status,
	xero_updated_at, synced_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	invoice_number = VALUES(invoice_number),
	contact_id = VALUES(contact_id),
	amount = VALUES(amount),
	date = VALUES(date),
	status = VALUES(status),
	xero_updated_at = VALUES(xero_updated_at),
	synced_at = VALUES(synced_at)`

// UpsertTenant 全欄 upsert；token 輪換請用 UpdateTenantTokens，或先 GetTenant 再整筆寫回，
// 避免零值覆寫 tenant_name／scopes／auth_status／created_at。
func (s *Store) UpsertTenant(ctx context.Context, tx *sql.Tx, t Tenant) error {
	_, err := tx.ExecContext(ctx, upsertTenantSQL,
		t.TenantID, t.TenantName, t.AccessTokenEnc, t.RefreshTokenEnc, t.TokenExpiry,
		t.Scopes, t.AuthStatus, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert tenant %s: %w", t.TenantID, err)
	}
	return nil
}

// UpdateTenantTokens 只更新 token 四欄，避免零值覆寫 tenant_name／scopes／auth_status／created_at。
// 找不到 tenant 時回 sql.ErrNoRows，呼叫端可用 errors.Is 判定。
func (s *Store) UpdateTenantTokens(ctx context.Context, tx *sql.Tx, tenantID string,
	accessEnc, refreshEnc *string, expiry, updatedAt time.Time) error {
	res, err := tx.ExecContext(ctx, updateTenantTokensSQL,
		accessEnc, refreshEnc, expiry, updatedAt, tenantID)
	if err != nil {
		return fmt.Errorf("更新 tenant %s 的 token: %w", tenantID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("讀取 tenant %s 的影響列數: %w", tenantID, err)
	}
	if n == 0 {
		return fmt.Errorf("更新 tenant %s 的 token: %w", tenantID, sql.ErrNoRows)
	}
	return nil
}

// UpdateTenantAuthStatus 只更新 auth_status 與 updated_at；refresh 失效時標記 auth_required。
// 找不到 tenant 時回 sql.ErrNoRows，呼叫端可用 errors.Is 判定。
func (s *Store) UpdateTenantAuthStatus(ctx context.Context, tx *sql.Tx, tenantID, status string) error {
	res, err := tx.ExecContext(ctx, updateTenantAuthStatusSQL, status, time.Now().UTC(), tenantID)
	if err != nil {
		return fmt.Errorf("更新 tenant %s 的授權狀態: %w", tenantID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("讀取 tenant %s 的影響列數: %w", tenantID, err)
	}
	if n == 0 {
		return fmt.Errorf("更新 tenant %s 的授權狀態: %w", tenantID, sql.ErrNoRows)
	}
	return nil
}

// GetTenant 讀取單一 tenant；找不到回 sql.ErrNoRows。
func (s *Store) GetTenant(ctx context.Context, tenantID string) (Tenant, error) {
	var t Tenant
	err := s.db.QueryRowContext(ctx, selectTenantSQL, tenantID).Scan(
		&t.TenantID, &t.TenantName, &t.AccessTokenEnc, &t.RefreshTokenEnc, &t.TokenExpiry,
		&t.Scopes, &t.AuthStatus, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Tenant{}, fmt.Errorf("查詢 tenant %s: %w", tenantID, err)
	}
	return t, nil
}

// ListTenants 列出 auth_status = 'active' 的 tenant（依 tenant_id 排序）；auth_required 需重新
// 授權才會再入列。無資料回空 slice，供 cron 與整 tenant 同步使用。
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx, listTenantsSQL)
	if err != nil {
		return nil, fmt.Errorf("查詢 tenant 清單: %w", err)
	}
	defer rows.Close()

	tenants := make([]Tenant, 0)
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(
			&t.TenantID, &t.TenantName, &t.AccessTokenEnc, &t.RefreshTokenEnc, &t.TokenExpiry,
			&t.Scopes, &t.AuthStatus, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("讀取 tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取 tenant 清單: %w", err)
	}
	return tenants, nil
}

// DeleteTenant 刪除指定 tenant；供測試清理使用，tenant 不存在不算錯誤。
func (s *Store) DeleteTenant(ctx context.Context, tenantID string) error {
	if _, err := s.db.ExecContext(ctx, deleteTenantSQL, tenantID); err != nil {
		return fmt.Errorf("刪除 tenant %s: %w", tenantID, err)
	}
	return nil
}

// UpsertSyncState 寫入同步水位；呼叫端負責與資料 upsert 放同一交易。
func (s *Store) UpsertSyncState(ctx context.Context, tx *sql.Tx, st SyncState) error {
	_, err := tx.ExecContext(ctx, upsertSyncStateSQL,
		st.TenantID, st.Entity, st.CursorTime, st.LastRunAt, st.Status, st.LastError)
	if err != nil {
		return fmt.Errorf("upsert sync_state %s/%s: %w", st.TenantID, st.Entity, err)
	}
	return nil
}

// GetSyncState 讀取單一實體的同步狀態；找不到回 sql.ErrNoRows。
func (s *Store) GetSyncState(ctx context.Context, tenantID, entity string) (SyncState, error) {
	var st SyncState
	err := s.db.QueryRowContext(ctx, selectSyncStateSQL, tenantID, entity).Scan(
		&st.TenantID, &st.Entity, &st.CursorTime, &st.LastRunAt, &st.Status, &st.LastError)
	if err != nil {
		return SyncState{}, fmt.Errorf("查詢 sync_state %s/%s: %w", tenantID, entity, err)
	}
	return st, nil
}

// InsertWebhookEvent 以 event_id 去重寫入；duplicate 表示該事件已存在。
func (s *Store) InsertWebhookEvent(ctx context.Context, tx *sql.Tx, e WebhookEvent) (bool, error) {
	res, err := tx.ExecContext(ctx, insertWebhookEventSQL,
		e.EventID, e.TenantID, e.Entity, e.XeroID, e.EventDate, e.ReceivedAt, e.ProcessedAt)
	if err != nil {
		return false, fmt.Errorf("insert webhook_event %s: %w", e.EventID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("讀取 webhook_event 影響列數: %w", err)
	}
	return n == 0, nil
}

// EnqueueSyncJob 依 (tenant_id, entity, xero_id) 去重入列，重送時刷新為 pending。
// done/failed 後再收到同資源新事件屬正常重排，故刻意復活為 pending；重複執行由冪等 upsert 吸收。
func (s *Store) EnqueueSyncJob(ctx context.Context, tx *sql.Tx, j SyncJob) error {
	_, err := tx.ExecContext(ctx, enqueueSyncJobSQL,
		j.TenantID, j.Entity, j.XeroID, j.Reason, j.CreatedAt)
	if err != nil {
		return fmt.Errorf("enqueue sync_job %s/%s/%s: %w", j.TenantID, j.Entity, j.XeroID, err)
	}
	return nil
}

// UpsertContact 冪等寫入 contact，重複時更新全部非鍵欄位。
func (s *Store) UpsertContact(ctx context.Context, tx *sql.Tx, c Contact) error {
	_, err := tx.ExecContext(ctx, upsertContactSQL,
		c.TenantID, c.ContactID, c.Name, c.Status, c.XeroUpdatedAt, c.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert contact %s: %w", c.ContactID, err)
	}
	return nil
}

// UpsertAccount 冪等寫入 account，重複時更新全部非鍵欄位。
func (s *Store) UpsertAccount(ctx context.Context, tx *sql.Tx, a Account) error {
	_, err := tx.ExecContext(ctx, upsertAccountSQL,
		a.TenantID, a.AccountID, a.Code, a.Name, a.Type, a.Status, a.XeroUpdatedAt, a.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert account %s: %w", a.AccountID, err)
	}
	return nil
}

// UpsertInvoice 冪等寫入 invoice；金額字串直接綁 DECIMAL。
func (s *Store) UpsertInvoice(ctx context.Context, tx *sql.Tx, inv Invoice) error {
	_, err := tx.ExecContext(ctx, upsertInvoiceSQL,
		inv.TenantID, inv.InvoiceID, inv.Type, inv.Number, inv.ContactID, inv.Status,
		inv.Date, inv.DueDate, amountOrZero(inv.Total), amountOrZero(inv.AmountDue), inv.Currency,
		inv.XeroUpdatedAt, inv.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert invoice %s: %w", inv.InvoiceID, err)
	}
	return nil
}

// UpsertCreditNote 冪等寫入 credit note；金額字串直接綁 DECIMAL。
func (s *Store) UpsertCreditNote(ctx context.Context, tx *sql.Tx, cn CreditNote) error {
	_, err := tx.ExecContext(ctx, upsertCreditNoteSQL,
		cn.TenantID, cn.CreditNoteID, cn.Type, cn.Number, cn.ContactID, cn.Status,
		cn.Date, cn.DueDate, amountOrZero(cn.Total), amountOrZero(cn.AmountDue),
		amountOrZero(cn.RemainingCredit), cn.Currency, cn.XeroUpdatedAt, cn.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert credit_note %s: %w", cn.CreditNoteID, err)
	}
	return nil
}

// UpsertPayment 冪等寫入 payment；金額字串直接綁 DECIMAL。
func (s *Store) UpsertPayment(ctx context.Context, tx *sql.Tx, p Payment) error {
	_, err := tx.ExecContext(ctx, upsertPaymentSQL,
		p.TenantID, p.PaymentID, p.InvoiceNumber, p.ContactID, amountOrZero(p.Amount), p.Date,
		p.Status, p.XeroUpdatedAt, p.SyncedAt)
	if err != nil {
		return fmt.Errorf("upsert payment %s: %w", p.PaymentID, err)
	}
	return nil
}

// GetPendingSyncJobs 取出待處理 job（依建立時間排序）；limit <= 0 時採預設上限 100，需要更多請明確指定。
func (s *Store) GetPendingSyncJobs(ctx context.Context, limit int) ([]SyncJob, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectPendingSyncJobsSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("查詢待處理 sync_jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]SyncJob, 0, limit)
	for rows.Next() {
		var j SyncJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.Entity, &j.XeroID,
			&j.Reason, &j.Status, &j.CreatedAt, &j.FinishedAt); err != nil {
			return nil, fmt.Errorf("讀取 sync_job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取 sync_job: %w", err)
	}
	return jobs, nil
}

// GetPendingSyncJobsByTenant 取出指定 tenant 的待處理 job（依建立時間排序）；
// limit <= 0 時採預設上限 100。
func (s *Store) GetPendingSyncJobsByTenant(ctx context.Context, tenantID string, limit int) ([]SyncJob, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectPendingSyncJobsByTenantSQL, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("查詢 tenant %s 的待處理 sync_jobs: %w", tenantID, err)
	}
	defer rows.Close()

	jobs := make([]SyncJob, 0, limit)
	for rows.Next() {
		var j SyncJob
		if err := rows.Scan(&j.ID, &j.TenantID, &j.Entity, &j.XeroID,
			&j.Reason, &j.Status, &j.CreatedAt, &j.FinishedAt); err != nil {
			return nil, fmt.Errorf("讀取 sync_job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取 sync_job: %w", err)
	}
	return jobs, nil
}

// CountWebhookEventsByTenant 回傳指定 tenant 已記錄的 webhook 事件筆數。
func (s *Store) CountWebhookEventsByTenant(ctx context.Context, tenantID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, countWebhookEventsByTenantSQL, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("統計 tenant %s 的 webhook_events: %w", tenantID, err)
	}
	return n, nil
}

// CountInvoicesByTenant 回傳指定 tenant 已寫入的 invoice 筆數，供同步測試確認寫入結果。
func (s *Store) CountInvoicesByTenant(ctx context.Context, tenantID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, countInvoicesByTenantSQL, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("統計 tenant %s 的 xero_invoices: %w", tenantID, err)
	}
	return n, nil
}

// MarkSyncJobDone 把 job 標為 done 並記錄完成時間。
func (s *Store) MarkSyncJobDone(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, markSyncJobDoneSQL, time.Now().UTC(), id); err != nil {
		return fmt.Errorf("標記 sync_job %d 完成: %w", id, err)
	}
	return nil
}
