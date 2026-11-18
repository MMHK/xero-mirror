package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// Batch 是一次同步的寫入單位：同一個交易內完成實體 upsert 與 cursor 更新。
// Entity 只用於定位 sync_state 的水位，不決定要寫哪張表（五個 slice 各自獨立處理）。
type Batch struct {
	TenantID string
	Entity   string
	// Cursor 是本批推進到的水位；nil 代表本批沒有新水位，不會清空既有 cursor。
	Cursor *time.Time
	// IsFinalFlush 標記是否為最後一次 flush；只有最後一次才更新 status 和 last_run_at，
	// 中間 flush 只更新 cursor_time，避免提前把 status 設為 idle。
	IsFinalFlush bool
	Contacts     []Contact
	Accounts     []Account
	Invoices     []Invoice
	CreditNotes  []CreditNote
	Payments     []Payment
}

// ApplyBatch 在單一交易內 upsert 全部資料並依 IsFinalFlush 決定更新範圍。
// IsFinalFlush=true 時更新 cursor、status、last_run_at（最後一次 flush 或空資料）；
// IsFinalFlush=false 且 Cursor!=nil 時只更新 cursor_time（中間 flush，記錄進度但不改變狀態）。
// 任一 upsert 失敗即整筆回滾；空批次（無資料、無 cursor）直接回 nil。
func (s *Store) ApplyBatch(ctx context.Context, b Batch) error {
	if b.empty() {
		return nil
	}
	var n int
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		count, applyErr := s.applyEntities(ctx, tx, b)
		if applyErr != nil {
			return applyErr
		}
		n = count

		// 只有最後一次 flush 才更新 status 和 last_run_at
		if b.IsFinalFlush {
			now := time.Now().UTC()
			return s.UpsertSyncState(ctx, tx, SyncState{
				TenantID: b.TenantID, Entity: b.Entity, CursorTime: b.Cursor,
				LastRunAt: &now, Status: "idle",
			})
		} else if b.Cursor != nil {
			// 中間 flush 只更新 cursor_time
			return s.UpdateCursor(ctx, tx, b.TenantID, b.Entity, b.Cursor)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.Debug("批次寫入完成", "tenant", b.TenantID, "entity", b.Entity, "count", n)
	return nil
}

// applyEntities 依序以多列 upsert 寫入五類實體，回傳寫入總筆數；
// 失敗時回傳 0 與帶實體名的錯誤。
func (s *Store) applyEntities(ctx context.Context, tx *sql.Tx, b Batch) (int, error) {
	if err := s.bulkUpsertContacts(ctx, tx, b.Contacts); err != nil {
		return 0, fmt.Errorf("寫入 contact: %w", err)
	}
	if err := s.bulkUpsertAccounts(ctx, tx, b.Accounts); err != nil {
		return 0, fmt.Errorf("寫入 account: %w", err)
	}
	if err := s.bulkUpsertInvoices(ctx, tx, b.Invoices); err != nil {
		return 0, fmt.Errorf("寫入 invoice: %w", err)
	}
	if err := s.bulkUpsertCreditNotes(ctx, tx, b.CreditNotes); err != nil {
		return 0, fmt.Errorf("寫入 credit_note: %w", err)
	}
	if err := s.bulkUpsertPayments(ctx, tx, b.Payments); err != nil {
		return 0, fmt.Errorf("寫入 payment: %w", err)
	}
	return len(b.Contacts) + len(b.Accounts) + len(b.Invoices) +
		len(b.CreditNotes) + len(b.Payments), nil
}

// empty 判斷批次是否沒有任何要寫入的資料與 cursor。
func (b Batch) empty() bool {
	return b.Cursor == nil && len(b.Contacts) == 0 && len(b.Accounts) == 0 &&
		len(b.Invoices) == 0 && len(b.CreditNotes) == 0 && len(b.Payments) == 0
}
