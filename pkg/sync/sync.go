// pkg/sync 是唯一的同步核心：cursor 增量、月度視窗回補與未來的 webhook／cron
// 觸發都走同一條 fetch → upsert 路徑，不得另寫第二套抓取或寫入邏輯。
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/xero"
)

// Entity 是同步實體；值同時寫入 sync_state.entity。
type Entity string

const (
	EntityContacts    Entity = "CONTACTS"
	EntityAccounts    Entity = "ACCOUNTS"
	EntityInvoices    Entity = "INVOICES"
	EntityCreditNotes Entity = "CREDIT_NOTES"
	EntityPayments    Entity = "PAYMENTS"
)

// Window 是同步範圍：Cursor 為增量水位，MonthStart／MonthEnd 為回補區間
// （下月 1 日 00:00 止，半開區間）；Cursor 與月度視窗互斥。
type Window struct {
	Cursor     *time.Time
	MonthStart *time.Time
	MonthEnd   *time.Time
}

// Runner 執行同步；client 與 store 由外部注入。
type Runner struct {
	client *xero.Client
	store  *store.Store
}

// NewRunner 建立同步執行器。
func NewRunner(c *xero.Client, s *store.Store) *Runner {
	return &Runner{client: c, store: s}
}

// entitySpec 描述端點能力；未核對支援度的能力一律關閉，走全量 + 冪等 upsert。
type entitySpec struct {
	path             string
	paginated        bool
	supportsSince    bool
	monthFilter      bool
	monthFilterField string // 月度過濾使用的欄位名稱（"Date" 或 "UpdatedDateUTC"）
	supportsBatchIDs bool   // 支援 ?IDs=uuid1,uuid2,... 批量查詢
	singlePath       string // single get 端點路徑模板（含 %s 佔位符），空字串代表不支援
}

// entitySpecs 是五個實體的端點能力表（對照 doc/spec.md §5）。
var entitySpecs = map[Entity]entitySpec{
	EntityContacts:    {path: "/Contacts", paginated: true, supportsSince: true, monthFilter: true, monthFilterField: "UpdatedDateUTC", supportsBatchIDs: true, singlePath: ""},
	EntityAccounts:    {path: "/Accounts", paginated: false, supportsSince: false, monthFilter: true, monthFilterField: "UpdatedDateUTC", supportsBatchIDs: false, singlePath: "/Accounts/%s"},
	EntityInvoices:    {path: "/Invoices", paginated: true, supportsSince: true, monthFilter: true, monthFilterField: "Date", supportsBatchIDs: true, singlePath: ""},
	EntityCreditNotes: {path: "/CreditNotes", paginated: true, supportsSince: true, monthFilter: true, monthFilterField: "Date", supportsBatchIDs: false, singlePath: "/CreditNotes/%s"},
	EntityPayments:    {path: "/Payments", paginated: true, supportsSince: true, monthFilter: true, monthFilterField: "Date", supportsBatchIDs: false, singlePath: "/Payments/%s"},
}

// Entities 回傳固定順序的五個同步實體；cron 與整 tenant 編排都照此順序執行。
func Entities() []Entity {
	return []Entity{
		EntityInvoices,
		EntityCreditNotes,
		EntityPayments,
		EntityContacts,
		EntityAccounts,
	}
}

// SupportsBatchIDs 回傳實體端點是否支援 ?IDs= 批量查詢；未知實體回 false。
func SupportsBatchIDs(e Entity) bool {
	spec, ok := entitySpecs[e]
	return ok && spec.supportsBatchIDs
}

// SupportsSingleGet 回傳實體端點是否支援 single get（/{Entity}/{id}）；未知實體回 false。
func SupportsSingleGet(e Entity) bool {
	spec, ok := entitySpecs[e]
	return ok && spec.singlePath != ""
}

// Run 依 Window 抓取單一 entity：逐頁累積，每達 writeChunkRows 列即分塊寫入 DB，
// 全部頁完成後再 flush 一次並帶入本輪水位。
// 抓取過程不推進 cursor，避免中途失敗後用較新水位略過尚未抓到的資料。
func (r *Runner) Run(ctx context.Context, tenantID string, entity Entity, w Window) (retErr error) {
	spec, ok := entitySpecs[entity]
	if !ok {
		return fmt.Errorf("同步 %s: 未知的實體", entity)
	}
	if err := validateWindow(w); err != nil {
		return fmt.Errorf("同步 %s: %w", entity, err)
	}

	// 標記 running（store 為 nil 時跳過，維持測試行為）
	if r.store != nil {
		if err := r.store.MarkSyncRunning(ctx, tenantID, string(entity)); err != nil {
			return fmt.Errorf("標記同步開始 %s: %w", entity, err)
		}
	}

	// 失敗時標記 failed（defer + named return）
	defer func() {
		if retErr != nil && r.store != nil {
			// 忽略 MarkSyncFailed 的錯誤，避免掩蓋原始錯誤
			_ = r.store.MarkSyncFailed(ctx, tenantID, string(entity), retErr.Error())
		}
	}()

	query, headers := buildRequest(spec, w)
	a := &pageApplier{
		tenantID: tenantID, entity: entity,
		store: r.store, ctx: ctx,
		syncedAt: time.Now().UTC(), trackCursor: w.MonthStart == nil,
	}
	if spec.paginated {
		if err := r.client.GetPages(ctx, tenantID, spec.path, query, headers, a.handle); err != nil {
			return fmt.Errorf("同步 %s: %w", entity, err)
		}
	} else if body, err := r.client.Get(ctx, tenantID, spec.path, query, headers); err != nil {
		return fmt.Errorf("同步 %s: %w", entity, err)
	} else if _, err := a.handle(0, body); err != nil {
		return fmt.Errorf("同步 %s: %w", entity, err)
	}
	if a.total > 0 {
		cursor := (*time.Time)(nil)
		if a.trackCursor {
			cursor = a.maxAt
		}
		if err := a.flush(cursor); err != nil {
			return fmt.Errorf("同步 %s: %w", entity, err)
		}
	}
	slog.Info("同步完成", "tenant", tenantID, "entity", entity, "count", a.total)
	return nil
}

// maxBatchIDs 是單批 ?IDs= 上限；Xero URL 長度約容納 40 個 ID，保守取 30。
const maxBatchIDs = 30

// RunBatch 以 ?IDs= 批量抓取指定 entry；只支援 supportsBatchIDs 的端點。
// webhook 觸發不得推進 cursor（trackCursor=false），中途仍依 writeChunkRows 分塊 flush。
func (r *Runner) RunBatch(ctx context.Context, tenantID string, entity Entity, ids []string) (retErr error) {
	spec, ok := entitySpecs[entity]
	if !ok {
		return fmt.Errorf("批量同步 %s: 未知的實體", entity)
	}
	if !spec.supportsBatchIDs {
		return fmt.Errorf("批量同步 %s: 端點不支援批量 IDs", entity)
	}
	unique := dedupIDs(ids)
	if len(unique) == 0 {
		return nil
	}

	// 標記 running（store 為 nil 時跳過，維持測試行為）
	if r.store != nil {
		if err := r.store.MarkSyncRunning(ctx, tenantID, string(entity)); err != nil {
			return fmt.Errorf("標記同步開始 %s: %w", entity, err)
		}
	}

	// 失敗時標記 failed（defer + named return）
	defer func() {
		if retErr != nil && r.store != nil {
			// 忽略 MarkSyncFailed 的錯誤，避免掩蓋原始錯誤
			_ = r.store.MarkSyncFailed(ctx, tenantID, string(entity), retErr.Error())
		}
	}()

	a := &pageApplier{
		tenantID: tenantID, entity: entity,
		store: r.store, ctx: ctx,
		syncedAt: time.Now().UTC(), trackCursor: false,
	}
	for _, chunk := range chunkIDs(unique, maxBatchIDs) {
		query := url.Values{}
		query.Set("IDs", strings.Join(chunk, ","))
		if err := r.client.GetPages(ctx, tenantID, spec.path, query, nil, a.handle); err != nil {
			return fmt.Errorf("批量同步 %s: %w", entity, err)
		}
	}
	if a.total > 0 {
		if err := a.flush(nil); err != nil {
			return fmt.Errorf("批量同步 %s: %w", entity, err)
		}
	}
	slog.Info("批量同步完成", "tenant", tenantID, "entity", entity, "ids", len(ids), "count", a.total)
	return nil
}

// RunSingle 用 single get 端點抓取單一實體；不推進 cursor。
// Xero single get response 格式與 list 相同（包在 array 裡），可複用 decodePage。
func (r *Runner) RunSingle(ctx context.Context, tenantID string, entity Entity, id string) error {
	spec, ok := entitySpecs[entity]
	if !ok {
		return fmt.Errorf("single get %s: 未知的實體", entity)
	}
	if spec.singlePath == "" {
		return fmt.Errorf("single get %s: 端點不支援 single get", entity)
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	path := fmt.Sprintf(spec.singlePath, id)
	body, err := r.client.Get(ctx, tenantID, path, nil, nil)
	if err != nil {
		return fmt.Errorf("single get %s/%s: %w", entity, id, err)
	}
	syncedAt := time.Now().UTC()
	b, n, _, err := decodePage(entity, body, tenantID, syncedAt)
	if err != nil {
		return fmt.Errorf("single get %s/%s: %w", entity, id, err)
	}
	if n == 0 || r.store == nil {
		return nil
	}
	batch := b
	batch.TenantID = tenantID
	batch.Entity = string(entity)
	if err := r.store.ApplyBatch(ctx, batch); err != nil {
		return fmt.Errorf("single get %s/%s: %w", entity, id, err)
	}
	slog.Info("single get 同步完成", "tenant", tenantID, "entity", entity, "id", id)
	return nil
}

// RunIncremental 讀 sync_state 水位後跑單一實體增量；無紀錄（sql.ErrNoRows）視為首跑全量。
func (r *Runner) RunIncremental(ctx context.Context, tenantID string, entity Entity) error {
	st, err := r.store.GetSyncState(ctx, tenantID, string(entity))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("讀取同步狀態 %s/%s: %w", tenantID, entity, err)
	}
	var cursor *time.Time
	if err == nil {
		cursor = st.CursorTime
	}
	return r.Run(ctx, tenantID, entity, Window{Cursor: cursor})
}

// RunTenant 依固定順序跑單一 tenant 的五個實體增量；單一實體失敗只 Warn 續跑，
// 全部實體都失敗才回錯，部分成功回 nil（錯誤已記 log）。
func (r *Runner) RunTenant(ctx context.Context, tenantID string) error {
	return r.runTenantEntities(ctx, tenantID, Entities())
}

// runTenantEntities 是單一 tenant × 指定實體的增量迴圈；單一實體失敗只 Warn 續跑，
// 全部實體都失敗才回錯，部分成功回 nil（錯誤已記 log）。
func (r *Runner) runTenantEntities(ctx context.Context, tenantID string, entities []Entity) error {
	slog.Info("開始 tenant 同步", "tenant", tenantID)
	failed := 0
	for _, entity := range entities {
		if err := r.RunIncremental(ctx, tenantID, entity); err != nil {
			failed++
			slog.Warn("實體同步失敗，續跑其餘實體",
				"tenant", tenantID, "entity", entity, "err", err)
		}
	}
	if failed == len(entities) {
		return fmt.Errorf("tenant %s 的全部 %d 個實體同步皆失敗", tenantID, failed)
	}
	slog.Info("tenant 同步結束", "tenant", tenantID, "failed", failed)
	return nil
}

// RunAllIncremental 列出全部 tenant 並逐個跑五實體增量；單一 tenant 失敗只 Warn 續跑，
// 回傳失敗 tenant 數組成的錯誤（全成功回 nil）。
func (r *Runner) RunAllIncremental(ctx context.Context) error {
	return r.runAllByEntities(ctx, Entities())
}

// RunEntityIncremental 對全部 active tenant 跑單一實體增量；未知實體在讀 tenant 清單前回錯。
func (r *Runner) RunEntityIncremental(ctx context.Context, entity Entity) error {
	if _, ok := entitySpecs[entity]; !ok {
		return fmt.Errorf("未知的實體 %q", entity)
	}
	return r.runAllByEntities(ctx, []Entity{entity})
}

// runAllByEntities 列出全部 tenant 並逐個跑指定實體增量；單一 tenant 失敗只 Warn 續跑，
// 回傳失敗 tenant 數組成的錯誤（全成功回 nil）。
func (r *Runner) runAllByEntities(ctx context.Context, entities []Entity) error {
	tenants, err := r.store.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("讀取 tenant 清單: %w", err)
	}
	failed := 0
	for _, t := range tenants {
		if err := r.runTenantEntities(ctx, t.TenantID, entities); err != nil {
			failed++
			slog.Warn("tenant 同步失敗，續跑下一個 tenant", "tenant", t.TenantID, "err", err)
		}
	}
	slog.Info("全 tenant 增量同步結束", "tenants", len(tenants), "failed", failed)
	if failed > 0 {
		return fmt.Errorf("同步失敗 %d 個 tenant", failed)
	}
	return nil
}

// RunAllMonth 逐 tenant × Entities() 跑指定月度視窗（UTC 半開區間）；單一實體失敗只 Warn
// 續跑，任一實體失敗即記該 tenant 失敗，回傳失敗 tenant 數組成的錯誤（全成功回 nil）。
func (r *Runner) RunAllMonth(ctx context.Context, start, end time.Time) error {
	return r.runAllMonthByEntities(ctx, start, end, Entities())
}

// RunEntityMonth 對全部 active tenant 跑單一實體的月度視窗；未知實體在讀 tenant 清單前回錯。
func (r *Runner) RunEntityMonth(ctx context.Context, start, end time.Time, entity Entity) error {
	if _, ok := entitySpecs[entity]; !ok {
		return fmt.Errorf("未知的實體 %q", entity)
	}
	return r.runAllMonthByEntities(ctx, start, end, []Entity{entity})
}

// runAllMonthByEntities 逐 tenant × 指定實體跑月度視窗（UTC 半開區間）；單一實體失敗只 Warn
// 續跑，任一實體失敗即記該 tenant 失敗，回傳失敗 tenant 數組成的錯誤（全成功回 nil）。
func (r *Runner) runAllMonthByEntities(ctx context.Context, start, end time.Time, entities []Entity) error {
	tenants, err := r.store.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("讀取 tenant 清單: %w", err)
	}
	w := Window{MonthStart: &start, MonthEnd: &end}
	failed := 0
	for _, t := range tenants {
		tenantFailed := false
		for _, entity := range entities {
			if err := r.Run(ctx, t.TenantID, entity, w); err != nil {
				tenantFailed = true
				slog.Warn("月度回補失敗，續跑其餘實體",
					"tenant", t.TenantID, "entity", entity, "err", err)
			}
		}
		if tenantFailed {
			failed++
		}
	}
	slog.Info("全 tenant 月度回補結束", "tenants", len(tenants), "failed", failed)
	if failed > 0 {
		return fmt.Errorf("同步失敗 %d 個 tenant", failed)
	}
	return nil
}

// dedupIDs 去除空字串與重複 ID，保留首次出現的順序。
func dedupIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// chunkIDs 把 IDs 切成每批最多 size 個，維持原順序。
func chunkIDs(ids []string, size int) [][]string {
	var chunks [][]string
	for i := 0; i < len(ids); i += size {
		end := min(i+size, len(ids))
		chunks = append(chunks, ids[i:end])
	}
	return chunks
}

// writeChunkRows 是單次 flush 的累積上限；約每 5 頁（每頁 100 筆）寫一次 DB。
const writeChunkRows = 500

// pageApplier 彙總一次 Run 的抓取結果；handle 符合 GetPages 的回呼簽名。
// 累積達 writeChunkRows 即 flush，避免全部頁面同時佔用記憶體與拉長交易。
type pageApplier struct {
	tenantID    string
	entity      Entity
	store       *store.Store
	ctx         context.Context
	syncedAt    time.Time
	trackCursor bool
	batch       store.Batch
	maxAt       *time.Time
	total       int
	written     int
}

// handle 解析單頁並累積到批次；達 writeChunkRows 即 flush（cursor 留到最後一次）。
// page 供 debug log 追蹤進度；maxAt 與 total 跨所有頁累積，不因 flush 重置。
func (a *pageApplier) handle(page int, body []byte) (int, error) {
	b, n, maxAt, err := decodePage(a.entity, body, a.tenantID, a.syncedAt)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	a.batch.Contacts = append(a.batch.Contacts, b.Contacts...)
	a.batch.Accounts = append(a.batch.Accounts, b.Accounts...)
	a.batch.Invoices = append(a.batch.Invoices, b.Invoices...)
	a.batch.CreditNotes = append(a.batch.CreditNotes, b.CreditNotes...)
	a.batch.Payments = append(a.batch.Payments, b.Payments...)
	if a.trackCursor {
		a.maxAt = latest(a.maxAt, maxAt)
	}
	a.total += n
	slog.Debug("抓取分頁", "tenant", a.tenantID, "entity", a.entity, "page", page, "count", n)
	if a.pending() >= writeChunkRows {
		if err := a.flush(nil); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// flush 把目前累積的資料寫入 DB；Batch 的 TenantID／Entity 由 flush 自行填入，
// 成功後只清空五個 slice，失敗時保留累積內容並直接回錯。
// cursor 只有整輪最後一次 flush 會帶入；store 為 nil（無 DB 的單元測試）時直接略過，不 panic。
func (a *pageApplier) flush(cursor *time.Time) error {
	if a.store == nil {
		return nil
	}
	b := a.batch
	b.TenantID = a.tenantID
	b.Entity = string(a.entity)
	b.Cursor = cursor
	rows := a.pending()
	if err := a.store.ApplyBatch(a.ctx, b); err != nil {
		return err
	}
	a.batch.Contacts = nil
	a.batch.Accounts = nil
	a.batch.Invoices = nil
	a.batch.CreditNotes = nil
	a.batch.Payments = nil
	a.written += rows
	slog.Debug("分塊寫入", "tenant", a.tenantID, "entity", a.entity,
		"rows", rows, "total", a.written, "with_cursor", cursor != nil)
	return nil
}

// pending 回傳尚未寫入 DB 的列數。
func (a *pageApplier) pending() int {
	return len(a.batch.Contacts) + len(a.batch.Accounts) + len(a.batch.Invoices) +
		len(a.batch.CreditNotes) + len(a.batch.Payments)
}

// decodePage 依實體分派到對應解碼函式；不認識的實體回錯誤。
func decodePage(entity Entity, body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	switch entity {
	case EntityContacts:
		return decodeContacts(body, tenantID, syncedAt)
	case EntityAccounts:
		return decodeAccounts(body, tenantID, syncedAt)
	case EntityInvoices:
		return decodeInvoices(body, tenantID, syncedAt)
	case EntityCreditNotes:
		return decodeCreditNotes(body, tenantID, syncedAt)
	case EntityPayments:
		return decodePayments(body, tenantID, syncedAt)
	}
	return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: 未知的實體", entity)
}

// decodeContacts 解析 /Contacts 單頁；n 是本頁筆數。
func decodeContacts(body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	var resp xero.ContactsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: %w", EntityContacts, err)
	}
	b := store.Batch{TenantID: tenantID, Contacts: toStoreContacts(resp.Contacts, tenantID, syncedAt)}
	return b, len(b.Contacts), maxUpdatedAt(b.Contacts, func(c store.Contact) *time.Time { return c.XeroUpdatedAt }), nil
}

// decodeAccounts 解析 /Accounts 單頁；n 是本頁筆數。
func decodeAccounts(body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	var resp xero.AccountsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: %w", EntityAccounts, err)
	}
	b := store.Batch{TenantID: tenantID, Accounts: toStoreAccounts(resp.Accounts, tenantID, syncedAt)}
	return b, len(b.Accounts), maxUpdatedAt(b.Accounts, func(a store.Account) *time.Time { return a.XeroUpdatedAt }), nil
}

// decodeInvoices 解析 /Invoices 單頁；n 是本頁筆數。
func decodeInvoices(body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	var resp xero.InvoicesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: %w", EntityInvoices, err)
	}
	b := store.Batch{TenantID: tenantID, Invoices: toStoreInvoices(resp.Invoices, tenantID, syncedAt)}
	return b, len(b.Invoices), maxUpdatedAt(b.Invoices, func(i store.Invoice) *time.Time { return i.XeroUpdatedAt }), nil
}

// decodeCreditNotes 解析 /CreditNotes 單頁；n 是本頁筆數。
func decodeCreditNotes(body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	var resp xero.CreditNotesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: %w", EntityCreditNotes, err)
	}
	b := store.Batch{TenantID: tenantID, CreditNotes: toStoreCreditNotes(resp.CreditNotes, tenantID, syncedAt)}
	return b, len(b.CreditNotes), maxUpdatedAt(b.CreditNotes, func(c store.CreditNote) *time.Time { return c.XeroUpdatedAt }), nil
}

// decodePayments 解析 /Payments 單頁；n 是本頁筆數。
func decodePayments(body []byte, tenantID string, syncedAt time.Time) (store.Batch, int, *time.Time, error) {
	var resp xero.PaymentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.Batch{}, 0, nil, fmt.Errorf("解析 %s: %w", EntityPayments, err)
	}
	b := store.Batch{TenantID: tenantID, Payments: toStorePayments(resp.Payments, tenantID, syncedAt)}
	return b, len(b.Payments), maxUpdatedAt(b.Payments, func(p store.Payment) *time.Time { return p.XeroUpdatedAt }), nil
}

// validateWindow 檢查 Cursor 與月度視窗互斥，且月度視窗兩端必須成對。
func validateWindow(w Window) error {
	if w.Cursor != nil && (w.MonthStart != nil || w.MonthEnd != nil) {
		return fmt.Errorf("Cursor 與月度視窗不可同時使用")
	}
	if (w.MonthStart == nil) != (w.MonthEnd == nil) {
		return fmt.Errorf("月度視窗需同時提供 MonthStart 與 MonthEnd")
	}
	return nil
}

// buildRequest 依端點能力決定 query 與 headers；不支援的能力一律退化為全量。
func buildRequest(spec entitySpec, w Window) (url.Values, map[string]string) {
	query := url.Values{}
	headers := map[string]string{}
	if w.Cursor != nil && spec.supportsSince {
		headers["If-Modified-Since"] = w.Cursor.UTC().Format(http.TimeFormat)
	}
	if w.MonthStart == nil || w.MonthEnd == nil {
		return query, headers
	}
	if spec.monthFilter && spec.monthFilterField != "" {
		query.Set("where", monthWhere(spec.monthFilterField, *w.MonthStart, *w.MonthEnd))
	}
	return query, headers
}

// monthWhere 組出 Xero where 條件（UTC 年月日、半開區間）：{field}>=DateTime(...)&&{field}<DateTime(...)。
func monthWhere(field string, start, end time.Time) string {
	s, e := start.UTC(), end.UTC()
	return fmt.Sprintf("%s>=DateTime(%d,%d,%d)&&%s<DateTime(%d,%d,%d)",
		field, s.Year(), int(s.Month()), s.Day(),
		field, e.Year(), int(e.Month()), e.Day())
}

// latest 回傳 cur 與 batch 中較晚者；batch 為 nil 時沿用 cur，不清空水位。
func latest(cur, batch *time.Time) *time.Time {
	if batch == nil {
		return cur
	}
	if cur != nil && !batch.After(*cur) {
		return cur
	}
	v := batch.UTC()
	return &v
}

// maxUpdatedAt 回傳這批資料最大的 xero_updated_at；全為 nil 時回 nil。
func maxUpdatedAt[T any](rows []T, at func(T) *time.Time) *time.Time {
	var max *time.Time
	for _, r := range rows {
		t := at(r)
		if t == nil {
			continue
		}
		if max == nil || t.After(*max) {
			v := t.UTC()
			max = &v
		}
	}
	return max
}
