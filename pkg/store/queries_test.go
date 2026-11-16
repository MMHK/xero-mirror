package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openTestStore 清空測試庫、套用 migration 並回傳 Store；無 TEST_MYSQL_DSN 時跳過。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	requireTestDatabase(t, dsn)
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
