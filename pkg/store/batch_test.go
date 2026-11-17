package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testBatch 組出含 1 張 invoice、2 個 contact 與指定 cursor 的最小批次。
func testBatch(cursor time.Time) Batch {
	now := time.Now().UTC()
	return Batch{
		TenantID: "t1", Entity: "INVOICE", Cursor: &cursor,
		Invoices: []Invoice{{
			TenantID: "t1", InvoiceID: "inv-1", Total: "100.0000",
			AmountDue: "100.0000", SyncedAt: now,
		}},
		Contacts: []Contact{
			{TenantID: "t1", ContactID: "c-1", SyncedAt: now},
			{TenantID: "t1", ContactID: "c-2", SyncedAt: now},
		},
	}
}

// countRows 回傳指定資料表的列數，供測試斷言使用。
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func TestApplyBatchIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	b := testBatch(cursor)
	require.NoError(t, s.ApplyBatch(ctx, b))
	require.NoError(t, s.ApplyBatch(ctx, b))

	require.Equal(t, 1, countRows(t, s, "xero_invoices"))
	require.Equal(t, 2, countRows(t, s, "xero_contacts"))
	st, err := s.GetSyncState(ctx, "t1", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(cursor), "cursor 應等於第二批的 cursor")
}

func TestApplyBatchRollbackKeepsCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 明確設嚴格模式，確保 "abc" 寫入 DECIMAL 一定被拒，不依賴容器預設 sql_mode。
	_, err := s.db.ExecContext(ctx, "SET SESSION sql_mode='STRICT_TRANS_TABLES'")
	require.NoError(t, err)
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.ApplyBatch(ctx, testBatch(t1)))

	t2 := t1.Add(time.Hour)
	b := testBatch(t2)
	b.Invoices[0].Total = "abc" // 非法 DECIMAL，讓 MySQL 拒絕寫入
	require.Error(t, s.ApplyBatch(ctx, b))

	st, err := s.GetSyncState(ctx, "t1", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(t1), "失敗批次不得推進 cursor")
	require.Equal(t, 1, countRows(t, s, "xero_invoices"))
	require.Equal(t, 2, countRows(t, s, "xero_contacts"))
}

func TestUpsertSyncStateKeepsCursorWhenNil(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.ApplyBatch(ctx, testBatch(t1)))

	// 只有資料、沒有新水位的批次不得清空既有 cursor。
	b := Batch{TenantID: "t1", Entity: "INVOICE", Contacts: []Contact{
		{TenantID: "t1", ContactID: "c-3", SyncedAt: time.Now().UTC()},
	}}
	require.NoError(t, s.ApplyBatch(ctx, b))

	st, err := s.GetSyncState(ctx, "t1", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(t1), "nil cursor 不得清空既有水位")
}

func TestUpsertSyncStateFillsNullCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 先寫資料但不帶 cursor，模擬月度回補建立的 sync_state 列（cursor 為 NULL）。
	require.NoError(t, s.ApplyBatch(ctx, Batch{TenantID: "t1", Entity: "INVOICE", Contacts: []Contact{
		{TenantID: "t1", ContactID: "c-1", SyncedAt: time.Now().UTC()},
	}}))
	pre, err := s.GetSyncState(ctx, "t1", "INVOICE")
	require.NoError(t, err)
	require.Nil(t, pre.CursorTime)

	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.ApplyBatch(ctx, testBatch(t1)))

	st, err := s.GetSyncState(ctx, "t1", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(t1), "既有 cursor 為 NULL 時應直接採用新值")
}

// TestApplyBatchBulkChunks 驗證超過 bulkUpsertRows 的批次可跨塊寫入，
// 重跑時走 ON DUPLICATE KEY UPDATE 更新既有列且不新增列。
func TestApplyBatchBulkChunks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	n := bulkUpsertRows + 1
	contacts := make([]Contact, n)
	for i := range contacts {
		contacts[i] = Contact{TenantID: "t1", ContactID: fmt.Sprintf("c-%03d", i), SyncedAt: now}
	}
	// 不帶 cursor，只驗證多列 upsert 的切塊與更新語意。
	b := Batch{TenantID: "t1", Entity: "CONTACT", Contacts: contacts}
	require.NoError(t, s.ApplyBatch(ctx, b))
	require.Equal(t, n, countRows(t, s, "xero_contacts"))

	// 更新對象取第一塊內的索引 n/2，確保驗到同一句多列語句內逐列 VALUES 的更新語意。
	idx := n / 2
	name := "更新後名稱"
	b.Contacts[idx].Name = &name
	require.NoError(t, s.ApplyBatch(ctx, b))
	require.Equal(t, n, countRows(t, s, "xero_contacts"))
	var got string
	require.NoError(t, s.db.QueryRowContext(ctx,
		"SELECT name FROM xero_contacts WHERE tenant_id = ? AND contact_id = ?",
		"t1", contacts[idx].ContactID).Scan(&got))
	require.Equal(t, name, got, "重跑後應更新既有列的 name")
}
