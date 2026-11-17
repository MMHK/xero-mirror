package sync

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/xero"
	"github.com/mmhk/xero-mirror/tests"
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
	tests.LockTestDatabase(t, dsn)
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

func TestRunBatchDedupKeepsOrder(t *testing.T) {
	var calls atomic.Int32
	got := make(chan string, 1)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		got <- req.URL.Query().Get("IDs")
		fmt.Fprint(w, `{"Invoices":[]}`)
	})
	err := r.RunBatch(context.Background(), "t1", EntityInvoices, []string{"inv-a", "inv-b", "inv-a", ""})
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load(), "去重後只發一次請求")
	require.Equal(t, "inv-a,inv-b", <-got)
}

func TestRunBatchChunksAt30(t *testing.T) {
	var calls atomic.Int32
	got := make(chan string, 2)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		got <- req.URL.Query().Get("IDs")
		fmt.Fprint(w, `{"Invoices":[]}`)
	})
	ids := make([]string, 31)
	for i := range ids {
		ids[i] = fmt.Sprintf("inv-%d", i)
	}
	require.NoError(t, r.RunBatch(context.Background(), "t1", EntityInvoices, ids))
	require.Equal(t, int32(2), calls.Load())
	first, second := <-got, <-got
	require.Len(t, strings.Split(first, ","), 30)
	require.Len(t, strings.Split(second, ","), 1)
}

func TestRunBatchRejectsUnsupported(t *testing.T) {
	var calls atomic.Int32
	r := newRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"CreditNotes":[]}`)
	})
	err := r.RunBatch(context.Background(), "t1", EntityCreditNotes, []string{"cn-1"})
	require.ErrorContains(t, err, "不支援批量 IDs")
	require.Zero(t, calls.Load(), "不支援的端點不得發出請求")
}

// emptyByEntity 是各實體空陣列回應的 JSON key 對照。
var emptyByEntity = map[Entity]string{
	EntityInvoices:    `{"Invoices":[]}`,
	EntityCreditNotes: `{"CreditNotes":[]}`,
	EntityPayments:    `{"Payments":[]}`,
	EntityContacts:    `{"Contacts":[]}`,
	EntityAccounts:    `{"Accounts":[]}`,
}

// newDBRunner 建立帶 Store 並指向 httptest.Server 的 Runner；無 TEST_MYSQL_DSN 時跳過。
func newDBRunner(t *testing.T, h http.HandlerFunc) (*Runner, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	tests.LockTestDatabase(t, dsn)
	db, err := store.OpenMigrated(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := xero.NewClientWithTokenSource(srv.URL, func(context.Context, string) (string, error) {
		return "tok", nil
	})
	require.NoError(t, err)
	return NewRunner(c, store.NewStore(db)), store.NewStore(db)
}

func TestEntitiesFixedOrder(t *testing.T) {
	want := []Entity{EntityInvoices, EntityCreditNotes, EntityPayments, EntityContacts, EntityAccounts}
	require.Equal(t, want, Entities())
}

func TestRunIncrementalUsesCursor(t *testing.T) {
	ctx := context.Background()
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	got := make(chan string, 1)
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		got <- req.Header.Get("If-Modified-Since")
		fmt.Fprint(w, emptyByEntity[EntityInvoices])
	})
	// 先寫入 cursor 水位，再驗證增量請求有帶 If-Modified-Since。
	require.NoError(t, st.ApplyBatch(ctx, store.Batch{
		TenantID: "t1", Entity: string(EntityInvoices), Cursor: &cursor,
	}))
	require.NoError(t, r.RunIncremental(ctx, "t1", EntityInvoices))
	require.Equal(t, cursor.Format(http.TimeFormat), <-got)
}

func TestRunTenantContinuesOnEntityError(t *testing.T) {
	// 用 400（不觸發 client 重試退避）讓 CreditNotes 立即失敗，驗證其餘實體仍續跑。
	var calls atomic.Int32
	r, _ := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		entity := entityFromPath(req.URL.Path)
		calls.Add(1)
		if entity == EntityCreditNotes {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, emptyByEntity[entity])
	})
	require.NoError(t, r.RunTenant(context.Background(), "t1"), "單一實體失敗應降級續跑且整體回 nil")
	require.Equal(t, int32(len(Entities())), calls.Load(), "五個實體都應被呼叫")
}

// entityFromPath 把測試請求路徑（如 /CreditNotes）對回 Entity。
func entityFromPath(path string) Entity {
	for e, spec := range entitySpecs {
		if spec.path == path {
			return e
		}
	}
	return ""
}

// insertTestTenant 以固定 ID 冪等 upsert 測試 tenant，重跑不會產生重複列。
func insertTestTenant(ctx context.Context, st *store.Store, tenantID string) error {
	now := time.Now().UTC()
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		return st.UpsertTenant(ctx, tx, store.Tenant{
			TenantID: tenantID, TenantName: tenantID,
			AuthStatus: "active", CreatedAt: now, UpdatedAt: now,
		})
	})
}

// TestRunAllMonthCoversTenantsAndEntities 驗證月度回補對每個 tenant 都跑完五個實體，
// 且全部成功時回 nil（DB 可能殘留其他 tenant，斷言只看本測試建立的兩個）。
func TestRunAllMonthCoversTenantsAndEntities(t *testing.T) {
	ctx := context.Background()
	hits := make(map[string]map[Entity]int)
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		tenant, entity := req.Header.Get("Xero-tenant-id"), entityFromPath(req.URL.Path)
		if hits[tenant] == nil {
			hits[tenant] = make(map[Entity]int)
		}
		hits[tenant][entity]++
		fmt.Fprint(w, emptyByEntity[entity])
	})
	tenants := []string{"t13month-a", "t13month-b"}
	for _, id := range tenants {
		require.NoError(t, insertTestTenant(ctx, st, id))
		t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, id)) })
	}
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, r.RunAllMonth(ctx, start, start.AddDate(0, 1, 0)))
	for _, id := range tenants {
		require.Len(t, hits[id], len(Entities()), "tenant %s 應只被呼叫五個實體各一次", id)
		for _, e := range Entities() {
			require.Equal(t, 1, hits[id][e], "tenant %s 的 %s 應被呼叫一次", id, e)
		}
	}
}

// TestRunAllIncrementalReportsFailedTenant 驗證單一 tenant 全實體失敗時回錯誤，
// 且該 tenant 的五個實體都仍被嘗試。
func TestRunAllIncrementalReportsFailedTenant(t *testing.T) {
	ctx := context.Background()
	const badTenant = "t13inc-fail"
	var attempts atomic.Int32
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Xero-tenant-id") == badTenant {
			attempts.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, emptyByEntity[entityFromPath(req.URL.Path)])
	})
	require.NoError(t, insertTestTenant(ctx, st, badTenant))
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, badTenant)) })
	require.ErrorContains(t, r.RunAllIncremental(ctx), "同步失敗")
	require.Equal(t, int32(len(Entities())), attempts.Load(), "五個實體都應嘗試")
}

// TestRunMonthDoesNotAdvanceCursor 驗證月度回補會寫入資料，但不得推進 sync_state 的 cursor。
func TestRunMonthDoesNotAdvanceCursor(t *testing.T) {
	ctx := context.Background()
	const tenant = "t13month-cursor"
	cursor := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	r, st := newDBRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"Invoices":[{"InvoiceID":"inv-month-1","Total":123.4500}]}`)
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, st.ApplyBatch(ctx, store.Batch{
		TenantID: tenant, Entity: string(EntityInvoices), Cursor: &cursor,
	}))
	require.NoError(t, r.Run(ctx, tenant, EntityInvoices, monthWindow()))
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.NotNil(t, got.CursorTime)
	require.True(t, got.CursorTime.Equal(cursor), "月度回補不得推進 cursor")
	n, err := st.CountInvoicesByTenant(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, 1, n, "invoice 應已寫入")
}

// TestRunFetchFailureKeepsCursor 驗證抓取失敗時不推進 cursor，下次仍從原水位續跑。
func TestRunFetchFailureKeepsCursor(t *testing.T) {
	ctx := context.Background()
	const tenant = "t13fail-cursor"
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// 400 不觸發 client 重試，讓抓取立即失敗。
	r, st := newDBRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, st.ApplyBatch(ctx, store.Batch{
		TenantID: tenant, Entity: string(EntityInvoices), Cursor: &cursor,
	}))
	require.Error(t, r.Run(ctx, tenant, EntityInvoices, Window{Cursor: &cursor}))
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.NotNil(t, got.CursorTime)
	require.True(t, got.CursorTime.Equal(cursor), "抓取失敗不得推進 cursor")
}

// TestParseEntity 驗證別名表大小寫不敏感、可含頭尾空白，未知值回 false（表驅動）。
func TestParseEntity(t *testing.T) {
	cases := []struct {
		raw  string
		want Entity
		ok   bool
	}{
		{"contacts", EntityContacts, true},
		{"contact", EntityContacts, true},
		{"accounts", EntityAccounts, true},
		{"account", EntityAccounts, true},
		{"  Invoices ", EntityInvoices, true},
		{"invoice", EntityInvoices, true},
		{"creditnotes", EntityCreditNotes, true},
		{"credit_notes", EntityCreditNotes, true},
		{"creditnote", EntityCreditNotes, true},
		{"payment", EntityPayments, true},
		{"nonsense", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		e, ok := ParseEntity(c.raw)
		require.Equal(t, c.ok, ok, c.raw)
		require.Equal(t, c.want, e, c.raw)
	}
}

// TestRunEntityRejectsUnknown 驗證未知實體在讀 tenant 清單前就回錯（store 為 nil 也不得 panic）。
func TestRunEntityRejectsUnknown(t *testing.T) {
	r := &Runner{}
	unknown := Entity("NONSENSE")
	require.ErrorContains(t, r.RunEntityIncremental(context.Background(), unknown), "未知的實體")
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.ErrorContains(t, r.RunEntityMonth(context.Background(), start, start.AddDate(0, 1, 0), unknown), "未知的實體")
}

// TestRunEntityIncrementalOnlyRequestsEntity 驗證指定 payments 時只打 /Payments，不請求其他實體。
func TestRunEntityIncrementalOnlyRequestsEntity(t *testing.T) {
	ctx := context.Background()
	const tenant = "t17entity-payments"
	var all, invoices atomic.Int32
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Xero-tenant-id") == tenant {
			all.Add(1)
			if req.URL.Path == "/Invoices" {
				invoices.Add(1)
			}
		}
		if req.URL.Path != "/Payments" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, emptyByEntity[EntityPayments])
	})
	require.NoError(t, insertTestTenant(ctx, st, tenant))
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, r.RunEntityIncremental(ctx, EntityPayments))
	require.Equal(t, int32(1), all.Load(), "指定 payments 時只應送出一次請求")
	require.Zero(t, invoices.Load(), "指定 payments 時不得請求 /Invoices")
}

// TestRunEntityMonthOnlyRequestsEntity 驗證月度模式只打指定端點、資料寫入但 cursor 不推進。
func TestRunEntityMonthOnlyRequestsEntity(t *testing.T) {
	ctx := context.Background()
	const tenant = "t17entity-month"
	var all atomic.Int32
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Xero-tenant-id") == tenant {
			all.Add(1)
		}
		if req.URL.Path != "/Payments" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"Payments":[{"PaymentID":"pay-month-1","Amount":10.0000,"Date":"/Date(1746057600000+0000)/","UpdatedDateUTC":"/Date(1746057600000+0000)/"}]}`)
	})
	require.NoError(t, insertTestTenant(ctx, st, tenant))
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, r.RunEntityMonth(ctx, start, start.AddDate(0, 1, 0), EntityPayments))
	require.Equal(t, int32(1), all.Load(), "月度模式只應請求 /Payments 一次")
	state, err := st.GetSyncState(ctx, tenant, string(EntityPayments))
	require.NoError(t, err, "資料寫入後應有 sync_state")
	require.Nil(t, state.CursorTime, "月度模式不得推進 cursor")
}

// TestRunSendsIfModifiedSinceByEntity 驗證 CreditNotes／Payments／Contacts 帶 Cursor 時送出
// If-Modified-Since，格式為 http.TimeFormat（表驅動）。
func TestRunSendsIfModifiedSinceByEntity(t *testing.T) {
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, entity := range []Entity{EntityCreditNotes, EntityPayments, EntityContacts} {
		got := make(chan string, 1)
		r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
			got <- req.Header.Get("If-Modified-Since")
			fmt.Fprint(w, emptyByEntity[entity])
		})
		require.NoError(t, r.Run(context.Background(), "t1", entity, Window{Cursor: &cursor}))
		require.Equal(t, cursor.Format(http.TimeFormat), <-got, string(entity))
	}
}

// TestRunOmitsIfModifiedSinceWithoutCursor 驗證首跑（Cursor 為 nil）不送 If-Modified-Since。
func TestRunOmitsIfModifiedSinceWithoutCursor(t *testing.T) {
	for _, entity := range []Entity{EntityCreditNotes, EntityPayments, EntityContacts} {
		got := make(chan bool, 1)
		r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
			got <- req.Header.Get("If-Modified-Since") != ""
			fmt.Fprint(w, emptyByEntity[entity])
		})
		require.NoError(t, r.Run(context.Background(), "t1", entity, Window{}))
		require.False(t, <-got, string(entity))
	}
}

// invoicePage 產生 n 筆 invoice 的單頁 JSON，UpdatedDateUTC 全部為 at（毫秒精度）。
func invoicePage(prefix string, n int, at time.Time) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(
			`{"InvoiceID":"%s-%d","Total":1.0000,"UpdatedDateUTC":"/Date(%d+0000)/"}`,
			prefix, i, at.UnixMilli())
	}
	return `{"Invoices":[` + strings.Join(items, ",") + `]}`
}

// TestRunChunkedWriteAdvancesCursor 驗證 600 筆分 6 頁（超過 writeChunkRows）會中途 flush，
// 最終資料完整，且 cursor 只在整輪結束時推進到最大 UpdatedDateUTC。
func TestRunChunkedWriteAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	const tenant = "t4chunk-ok"
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("page"))
		if page > 6 {
			fmt.Fprint(w, `{"Invoices":[]}`)
			return
		}
		fmt.Fprint(w, invoicePage(fmt.Sprintf("inv-%d", page), 100, base.Add(time.Duration(page)*time.Minute)))
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, r.Run(ctx, tenant, EntityInvoices, Window{}))
	n, err := st.CountInvoicesByTenant(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, 600, n, "六頁共 600 筆都應寫入")
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.NotNil(t, got.CursorTime)
	require.True(t, got.CursorTime.Equal(base.Add(6*time.Minute)), "cursor 應為最大 UpdatedDateUTC")
}

// TestRunChunkedWriteFailureKeepsOldCursor 驗證第 7 頁失敗時，前段已 flush 的資料留在 DB，
// 但 cursor 不前進。
func TestRunChunkedWriteFailureKeepsOldCursor(t *testing.T) {
	ctx := context.Background()
	const tenant = "t4chunk-fail"
	old := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	r, st := newDBRunner(t, func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("page"))
		if page > 6 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, invoicePage(fmt.Sprintf("inv-fail-%d", page), 100, base.Add(time.Duration(page)*time.Minute)))
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, st.ApplyBatch(ctx, store.Batch{
		TenantID: tenant, Entity: string(EntityInvoices), Cursor: &old,
	}))
	require.Error(t, r.Run(ctx, tenant, EntityInvoices, Window{Cursor: &old}))
	n, err := st.CountInvoicesByTenant(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, 500, n, "DB 應只有第一次 flush 的 500 筆")
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.NotNil(t, got.CursorTime)
	require.True(t, got.CursorTime.Equal(old), "抓取失敗不得推進 cursor")
}

// TestRunSingleUsesSingleGetEndpoint 驗證 RunSingle 以 /{Entity}/{id} 端點抓取，
// 且回應格式與 list 相同（包在 array 裡）可正確解析。
func TestRunSingleUsesSingleGetEndpoint(t *testing.T) {
	got := make(chan string, 1)
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		got <- req.URL.Path
		// Xero single get 回應格式與 list 相同，包在 array 裡
		fmt.Fprint(w, `{"CreditNotes":[{"CreditNoteID":"cn-123","Type":"ACCRECCREDIT","Status":"AUTHORISED"}]}`)
	})
	err := r.RunSingle(context.Background(), "t1", EntityCreditNotes, "cn-123")
	require.NoError(t, err)
	require.Equal(t, "/CreditNotes/cn-123", <-got, "應使用 single get 端點")
}

// TestRunSingleRejectsUnsupported 驗證不支援 single get 的實體（如 Invoices）會回錯。
func TestRunSingleRejectsUnsupported(t *testing.T) {
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		t.Fatal("不應發出請求")
	})
	err := r.RunSingle(context.Background(), "t1", EntityInvoices, "inv-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "不支援 single get")
}

// TestRunSingleSkipsEmptyID 驗證空白 ID 直接略過，不發請求。
func TestRunSingleSkipsEmptyID(t *testing.T) {
	r := newRunner(t, func(w http.ResponseWriter, req *http.Request) {
		t.Fatal("空白 ID 不應發出請求")
	})
	require.NoError(t, r.RunSingle(context.Background(), "t1", EntityPayments, "  "))
	require.NoError(t, r.RunSingle(context.Background(), "t1", EntityPayments, ""))
}

// TestSupportsSingleGetCoversExpectedEntities 驗證 single get 支援度矩陣正確。
func TestSupportsSingleGetCoversExpectedEntities(t *testing.T) {
	require.True(t, SupportsSingleGet(EntityCreditNotes))
	require.True(t, SupportsSingleGet(EntityPayments))
	require.True(t, SupportsSingleGet(EntityAccounts))
	require.False(t, SupportsSingleGet(EntityInvoices), "Invoices 支援 batch IDs，不需要 single get")
	require.False(t, SupportsSingleGet(EntityContacts), "Contacts 支援 batch IDs，不需要 single get")
}

// TestRunMarksRunningThenIdle 驗證成功流程：先標記 running，最終 status='idle'。
func TestRunMarksRunningThenIdle(t *testing.T) {
	ctx := context.Background()
	const tenant = "t-status-ok"
	r, st := newDBRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		// 回傳一筆資料，確保 ApplyBatch 被呼叫、status 更新為 idle
		fmt.Fprint(w, `{"Invoices":[{"InvoiceID":"inv-ok-1","Total":10.0000}]}`)
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	require.NoError(t, r.Run(ctx, tenant, EntityInvoices, Window{}))
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.Equal(t, "idle", got.Status, "成功後 status 應為 idle")
	require.Nil(t, got.LastError)
}

// TestRunMarksFailedOnError 驗證 mock 400 錯誤後 status='failed'，last_error 非空，cursor 不變。
func TestRunMarksFailedOnError(t *testing.T) {
	ctx := context.Background()
	const tenant = "t-status-fail"
	cursor := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	r, st := newDBRunner(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	t.Cleanup(func() { require.NoError(t, st.DeleteTenant(ctx, tenant)) })
	// 先寫入 cursor 水位
	require.NoError(t, st.ApplyBatch(ctx, store.Batch{
		TenantID: tenant, Entity: string(EntityInvoices), Cursor: &cursor,
	}))
	require.Error(t, r.Run(ctx, tenant, EntityInvoices, Window{Cursor: &cursor}))
	got, err := st.GetSyncState(ctx, tenant, string(EntityInvoices))
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status, "失敗後 status 應為 failed")
	require.NotNil(t, got.LastError, "失敗後 last_error 應非空")
	require.NotNil(t, got.CursorTime)
	require.True(t, got.CursorTime.Equal(cursor), "失敗不得推進 cursor")
}
