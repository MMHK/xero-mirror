package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/tests"
	"github.com/stretchr/testify/require"
)

// openTestStore 清空測試庫、套用 migration 並回傳 Store；無 TEST_MYSQL_DSN 時跳過。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	tests.RequireTestDatabase(t, dsn)
	tests.LockTestDatabase(t, dsn)
	cleaner, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	dropAllTables(t, cleaner)
	require.NoError(t, cleaner.Close())
	db, err := OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestUpsertInvoiceIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	inv := Invoice{TenantID: "t1", InvoiceID: "inv-1", Total: "100.0000", AmountDue: "100.0000", SyncedAt: time.Now().UTC()}
	write := func() {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.UpsertInvoice(ctx, tx, inv) }))
	}
	write()
	status := "PAID"
	inv.Status, inv.Total = &status, "88.0000"
	write()

	var n int
	require.NoError(t, s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM xero_invoices WHERE tenant_id = ? AND invoice_id = ?", "t1", "inv-1").Scan(&n))
	require.Equal(t, 1, n)
	var gotStatus, gotTotal string
	require.NoError(t, s.db.QueryRowContext(ctx,
		"SELECT status, total FROM xero_invoices WHERE tenant_id = ? AND invoice_id = ?", "t1", "inv-1").
		Scan(&gotStatus, &gotTotal))
	require.Equal(t, "PAID", gotStatus)
	require.Equal(t, "88.0000", gotTotal)
}

func TestInsertWebhookEventDedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ev := WebhookEvent{EventID: "ev-1", TenantID: "t1", Entity: "INVOICE", XeroID: "inv-1", ReceivedAt: time.Now().UTC()}
	insert := func() bool {
		var dup bool
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			var err error
			dup, err = s.InsertWebhookEvent(ctx, tx, ev)
			return err
		}))
		return dup
	}
	require.False(t, insert())
	require.True(t, insert())
}

func TestWithTxRollback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if uerr := s.UpsertContact(ctx, tx, Contact{TenantID: "t1", ContactID: "c-1", SyncedAt: time.Now().UTC()}); uerr != nil {
			return uerr
		}
		return errors.New("故意失敗")
	})
	require.Error(t, err)
	var n int
	require.NoError(t, s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM xero_contacts WHERE tenant_id = ?", "t1").Scan(&n))
	require.Zero(t, n)
}

// TestUpsertInvoiceLongNumber 驗證 255 字元的 Xero 發票號可完整寫入並讀回。
func TestUpsertInvoiceLongNumber(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	number := strings.Repeat("N", 255)
	inv := Invoice{TenantID: "t1", InvoiceID: "inv-long", Number: &number, SyncedAt: time.Now().UTC()}
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return s.UpsertInvoice(ctx, tx, inv) }))

	var got string
	require.NoError(t, s.db.QueryRowContext(ctx,
		"SELECT number FROM xero_invoices WHERE tenant_id = ? AND invoice_id = ?", "t1", "inv-long").Scan(&got))
	require.Equal(t, number, got)
}

// TestMarkSyncRunning 驗證首次呼叫 INSERT 列（status='running'）；第二次呼叫 UPDATE（last_error 清空）。
func TestMarkSyncRunning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkSyncRunning(ctx, "t-running", "INVOICE"))
	st, err := s.GetSyncState(ctx, "t-running", "INVOICE")
	require.NoError(t, err)
	require.Equal(t, "running", st.Status)
	require.Nil(t, st.LastError)

	// 模擬先前有錯誤，再標記 running 應清空 last_error
	require.NoError(t, s.MarkSyncFailed(ctx, "t-running", "INVOICE", "舊錯誤"))
	require.NoError(t, s.MarkSyncRunning(ctx, "t-running", "INVOICE"))
	st, err = s.GetSyncState(ctx, "t-running", "INVOICE")
	require.NoError(t, err)
	require.Equal(t, "running", st.Status)
	require.Nil(t, st.LastError)
}

// TestMarkSyncFailed 驗證首次呼叫 INSERT 列（status='failed', last_error='...'）；第二次呼叫 UPDATE。
func TestMarkSyncFailed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkSyncFailed(ctx, "t-failed", "INVOICE", "首次失敗"))
	st, err := s.GetSyncState(ctx, "t-failed", "INVOICE")
	require.NoError(t, err)
	require.Equal(t, "failed", st.Status)
	require.NotNil(t, st.LastError)
	require.Equal(t, "首次失敗", *st.LastError)

	require.NoError(t, s.MarkSyncFailed(ctx, "t-failed", "INVOICE", "第二次失敗"))
	st, err = s.GetSyncState(ctx, "t-failed", "INVOICE")
	require.NoError(t, err)
	require.Equal(t, "failed", st.Status)
	require.Equal(t, "第二次失敗", *st.LastError)
}

// TestMarkSyncFailedPreservesCursor 驗證 MarkSyncFailed 不會動到 cursor_time。
func TestMarkSyncFailedPreservesCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.ApplyBatch(ctx, Batch{
		TenantID: "t-cursor-preserve", Entity: "INVOICE", Cursor: &cursor,
	}))
	require.NoError(t, s.MarkSyncFailed(ctx, "t-cursor-preserve", "INVOICE", "失敗但 cursor 不動"))
	st, err := s.GetSyncState(ctx, "t-cursor-preserve", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(cursor), "MarkSyncFailed 不得動到 cursor_time")
	require.Equal(t, "failed", st.Status)
}
