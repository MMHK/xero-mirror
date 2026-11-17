package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBulkStatement 驗證多列模板的 placeholder 數與更新尾段與單列模板一致。
func TestBulkStatement(t *testing.T) {
	singles := []string{upsertContactSQL, upsertAccountSQL, upsertInvoiceSQL,
		upsertCreditNoteSQL, upsertPaymentSQL}
	wantCols := []int{6, 8, 13, 14, 9}
	for i, single := range singles {
		require.Contains(t, single, "\nON DUPLICATE KEY UPDATE")
		tail := single[strings.Index(single, "\nON DUPLICATE KEY UPDATE"):]
		for _, n := range []int{1, 2} {
			sql, cols, err := bulkStatement(single, n)
			require.NoError(t, err, "cols=%d n=%d", wantCols[i], n)
			require.Equal(t, wantCols[i], cols, "cols=%d n=%d 每列 placeholder 數", wantCols[i], n)
			require.Equal(t, wantCols[i]*n, strings.Count(sql, "?"), "cols=%d n=%d 總數", wantCols[i], n)
			require.True(t, strings.HasSuffix(sql, tail), "cols=%d n=%d 尾段", wantCols[i], n)
		}
	}
}
