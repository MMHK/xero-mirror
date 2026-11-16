package sync

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mmhk/xero-mirror/pkg/xero"
	"github.com/stretchr/testify/require"
)

func TestMappingInvoicePreservesDecimalString(t *testing.T) {
	synced := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	rows := []xero.Invoice{{
		InvoiceID: "inv-1", Total: json.Number("123.4500"),
		UpdatedDateUTC: xero.XeroTime{Time: synced},
	}}
	got := toStoreInvoices(rows, "t1", synced)
	require.Len(t, got, 1)
	require.Equal(t, "123.4500", got[0].Total)
	require.Equal(t, "0", got[0].AmountDue)
}

func TestMappingNullDateBecomesNil(t *testing.T) {
	date := time.Date(2026, 5, 2, 3, 4, 5, 0, time.UTC)
	rows := []xero.Invoice{
		{InvoiceID: "a"},
		{InvoiceID: "b", Date: xero.XeroTime{Time: date}},
	}
	got := toStoreInvoices(rows, "t1", time.Now().UTC())
	require.Nil(t, got[0].Date)
	require.NotNil(t, got[1].Date)
	require.True(t, got[1].Date.Equal(date))
}
