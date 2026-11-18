package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// bulkUpsertRows 是單一句多列 upsert 的最大列數；超過就切塊，避免 placeholder 過多。
const bulkUpsertRows = 100

// bulkStatement 把 queries.go 的單列 upsert 模板改寫成 n 列版本：
// 欄位清單與 ON DUPLICATE KEY UPDATE 原樣保留，只把數值群組重複 n 次；
// 同時回傳每列的 placeholder 數，供 upsertBulk 交叉驗證參數數量。
// 產出的 SQL 只含固定文字與 ? placeholder，不含任何資料值。
// 切割標記 ") VALUES (" 與 "\nON DUPLICATE KEY UPDATE" 是與 queries.go 的硬約定：
// 改動單列模板格式（例如換行或多餘空白）時必須同步調整這裡。
func bulkStatement(singleSQL string, n int) (string, int, error) {
	const valuesMark = ") VALUES ("
	const updateMark = "\nON DUPLICATE KEY UPDATE"
	if n <= 0 {
		return "", 0, fmt.Errorf("多列 upsert 的列數必須大於 0")
	}
	vi := strings.Index(singleSQL, valuesMark)
	oi := strings.Index(singleSQL, updateMark)
	if vi < 0 || oi < vi {
		return "", 0, fmt.Errorf("單列 upsert 模板格式不符")
	}
	// 取出單列的 placeholder 群組（去掉結尾右括號），再複製 n 份串接。
	group := singleSQL[vi+len(valuesMark) : oi]
	if !strings.HasSuffix(group, ")") {
		return "", 0, fmt.Errorf("單列 upsert 模板缺少數值群組結尾")
	}
	group = strings.TrimSuffix(group, ")")
	groups := make([]string, 0, n)
	for i := 0; i < n; i++ {
		groups = append(groups, "("+group+")")
	}
	// valuesMark 的結尾 "(" 由 groups 自帶，前綴只留到 ") VALUES "（含空格）。
	head := singleSQL[:vi+len(valuesMark)-1]
	// placeholder 計數假設模板內沒有 ? 字面值（WHERE 等固定文字不會出現 ?）。
	return head + strings.Join(groups, ", ") + singleSQL[oi:], strings.Count(group, "?"), nil
}

// upsertBulk 依 bulkUpsertRows 切塊，把 rows 以多列 upsert 寫入同一交易。
// table 與 singleSQL 供組 SQL 與錯誤訊息使用；bind 把單列轉成參數清單。
func upsertBulk[T any](ctx context.Context, tx *sql.Tx, table, singleSQL string,
	rows []T, bind func(T) []any) error {
	if len(rows) == 0 {
		return nil
	}
	written := 0
	for start := 0; start < len(rows); start += bulkUpsertRows {
		end := start + bulkUpsertRows
		if end > len(rows) {
			end = len(rows)
		}
		stmt, placeholders, err := bulkStatement(singleSQL, end-start)
		if err != nil {
			return fmt.Errorf("產生多列 upsert SQL: %w", err)
		}
		// 第一列只 bind 一次，同時用它的參數數交叉驗證模板 placeholder 數量。
		firstArgs := bind(rows[start])
		if placeholders != len(firstArgs) {
			return fmt.Errorf("%s 的 placeholder 數 %d 與欄位數 %d 不符",
				table, placeholders, len(firstArgs))
		}
		args := make([]any, 0, (end-start)*len(firstArgs))
		args = append(args, firstArgs...)
		for i := start + 1; i < end; i++ {
			args = append(args, bind(rows[i])...)
		}
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("多列寫入: %w", err)
		}
		written += end - start
		slog.Debug("multi-row upsert", "table", table, "rows", end-start, "total", written)
	}
	return nil
}

// 以下五個 bulkUpsertX 刻意保留 method 形式（receiver 未使用），
// 是為了與 queries.go 的 UpsertX 命名對齊，方便對照兩者的欄位順序。

// bulkUpsertContacts 以多列 upsert 寫入 contact；空 slice 直接回 nil。
func (s *Store) bulkUpsertContacts(ctx context.Context, tx *sql.Tx, rows []Contact) error {
	return upsertBulk(ctx, tx, "contact", upsertContactSQL, rows, func(c Contact) []any {
		return []any{c.TenantID, c.ContactID, c.Name, c.Status, c.XeroUpdatedAt, c.SyncedAt}
	})
}

// bulkUpsertAccounts 以多列 upsert 寫入 account；空 slice 直接回 nil。
func (s *Store) bulkUpsertAccounts(ctx context.Context, tx *sql.Tx, rows []Account) error {
	return upsertBulk(ctx, tx, "account", upsertAccountSQL, rows, func(a Account) []any {
		return []any{a.TenantID, a.AccountID, a.Code, a.Name, a.Type, a.Status, a.XeroUpdatedAt, a.SyncedAt}
	})
}

// bulkUpsertInvoices 以多列 upsert 寫入 invoice；空 slice 直接回 nil。
func (s *Store) bulkUpsertInvoices(ctx context.Context, tx *sql.Tx, rows []Invoice) error {
	return upsertBulk(ctx, tx, "invoice", upsertInvoiceSQL, rows, func(inv Invoice) []any {
		return []any{inv.TenantID, inv.InvoiceID, inv.Type, inv.Number, inv.ContactID, inv.Status,
			inv.Date, inv.DueDate, amountOrZero(inv.Total), amountOrZero(inv.AmountDue), inv.Currency,
			inv.XeroUpdatedAt, inv.SyncedAt}
	})
}

// bulkUpsertCreditNotes 以多列 upsert 寫入 credit note；空 slice 直接回 nil。
func (s *Store) bulkUpsertCreditNotes(ctx context.Context, tx *sql.Tx, rows []CreditNote) error {
	return upsertBulk(ctx, tx, "credit_note", upsertCreditNoteSQL, rows, func(cn CreditNote) []any {
		return []any{cn.TenantID, cn.CreditNoteID, cn.Type, cn.Number, cn.ContactID, cn.Status,
			cn.Date, cn.DueDate, amountOrZero(cn.Total), amountOrZero(cn.AmountDue),
			amountOrZero(cn.RemainingCredit), cn.Currency, cn.XeroUpdatedAt, cn.SyncedAt}
	})
}

// bulkUpsertPayments 以多列 upsert 寫入 payment；空 slice 直接回 nil。
func (s *Store) bulkUpsertPayments(ctx context.Context, tx *sql.Tx, rows []Payment) error {
	return upsertBulk(ctx, tx, "payment", upsertPaymentSQL, rows, func(p Payment) []any {
		return []any{p.TenantID, p.PaymentID, p.InvoiceNumber, p.ContactID, amountOrZero(p.Amount),
			p.Date, p.Status, p.XeroUpdatedAt, p.SyncedAt}
	})
}
