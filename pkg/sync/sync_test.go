package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/xero"
	"github.com/stretchr/testify/require"
)

// newRunner 建立指向 httptest.Server 的 Runner；store 傳 nil（測試回應皆為空，不寫 DB）。
func newRunner(t *testing.T, h http.HandlerFunc) *Runner {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := xero.NewClientWithTokenSource(srv.URL, func(context.Context, string) (string, error) {
		return "tok", nil
	})
	require.NoError(t, err)
	return NewRunner(c, nil)
}

// monthWindow 回傳 2026-05 的半開區間 [2026-05-01, 2026-06-01)。
func monthWindow() Window {
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return Window{MonthStart: &start, MonthEnd: &end}
}

func TestRunSendsIfModifiedSince(t *testing.T) {
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	got := make(chan string, 1)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		got <- req.Header.Get("If-Modified-Since")
		fmt.Fprint(w, `{"Invoices":[]}`)
	})
	require.NoError(t, r.Run(context.Background(), "t1", EntityInvoices, Window{Cursor: &cursor}))
	require.Equal(t, cursor.Format(http.TimeFormat), <-got)
}

func TestRunMonthWindowFallback(t *testing.T) {
	got := make(chan url.Values, 1)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		got <- req.URL.Query()
		fmt.Fprint(w, `{"Contacts":[]}`)
	})
	require.NoError(t, r.Run(context.Background(), "t1", EntityContacts, monthWindow()))
	q := <-got
	require.Empty(t, q.Get("where"))
	require.Empty(t, q.Get("from"))
	require.Empty(t, q.Get("to"))
	require.Equal(t, "1", q.Get("page"))
}

func TestRunAccountsNoPagination(t *testing.T) {
	got := make(chan url.Values, 1)
	var calls atomic.Int32
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		got <- req.URL.Query()
		fmt.Fprint(w, `{"Accounts":[]}`)
	})
	require.NoError(t, r.Run(context.Background(), "t1", EntityAccounts, Window{}))
	require.Empty(t, (<-got).Get("page"))
	require.Equal(t, int32(1), calls.Load())
}

func TestRunMonthUsesWhereClause(t *testing.T) {
	got := make(chan url.Values, 1)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		got <- req.URL.Query()
		fmt.Fprint(w, `{"Invoices":[]}`)
	})
	require.NoError(t, r.Run(context.Background(), "t1", EntityInvoices, monthWindow()))
	q := <-got
	require.Contains(t, q.Get("where"), "DateTime(2026,5,1)")
	require.Contains(t, q.Get("where"), "DateTime(2026,6,1)")
	require.Empty(t, q.Get("from"))
	require.Empty(t, q.Get("to"))
}

func TestRunCursorMonthMutuallyExclusive(t *testing.T) {
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	r := newRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"Invoices":[]}`)
	})
	w := monthWindow()
	w.Cursor = &cursor
	err := r.Run(context.Background(), "t1", EntityInvoices, w)
	require.ErrorContains(t, err, "Cursor 與月度視窗不可同時使用")
	require.Zero(t, calls.Load(), "參數衝突時不得發出任何請求")
}

func TestApplyBatchCursorNeverDowngrades(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	db, err := store.OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	s := store.NewStore(db)
	ctx := context.Background()
	t2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, cur := range []time.Time{t2, t2.Add(-time.Hour)} {
		require.NoError(t, s.ApplyBatch(ctx, store.Batch{TenantID: "downgrade", Entity: "INVOICE", Cursor: &cur}))
	}
	st, err := s.GetSyncState(ctx, "downgrade", "INVOICE")
	require.NoError(t, err)
	require.NotNil(t, st.CursorTime)
	require.True(t, st.CursorTime.Equal(t2), "較舊的 cursor 不得覆蓋既有水位")
}
