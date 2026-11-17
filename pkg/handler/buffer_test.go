package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
	"github.com/mmhk/xero-mirror/pkg/xero"
	"github.com/stretchr/testify/require"
)

// newBufferRunner 建立指向 httptest.Server 的 Runner；空回應不寫 DB，store 傳 nil。
// idsCh 回傳每次請求的 IDs query，緩衝放大避免 event loop flush 時被 channel 阻塞。
func newBufferRunner(t *testing.T) (*pkgsync.Runner, chan string) {
	t.Helper()
	idsCh := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		idsCh <- req.URL.Query().Get("IDs")
		_, _ = w.Write([]byte(`{"Invoices":[]}`))
	}))
	t.Cleanup(srv.Close)
	client, err := xero.NewClientWithTokenSource(srv.URL, func(context.Context, string) (string, error) {
		return "tok", nil
	})
	require.NoError(t, err)
	return pkgsync.NewRunner(client, nil), idsCh
}

func TestBufferFlushesAtBatchSize(t *testing.T) {
	runner, idsCh := newBufferRunner(t)
	buf := NewWebhookBuffer(context.Background(), runner, 3, time.Hour)
	t.Cleanup(func() { _ = buf.Shutdown(context.Background()) })

	for _, id := range []string{"inv-1", "inv-2", "inv-3"} {
		buf.Add("t1", pkgsync.EntityInvoices, id)
	}
	select {
	case ids := <-idsCh:
		require.Len(t, strings.Split(ids, ","), 3, "單次批量請求應含 3 個去重 IDs")
	case <-time.After(time.Second):
		t.Fatal("達 batch size 後未觸發 flush")
	}
	select {
	case <-idsCh:
		t.Fatal("batch size flush 後不應再有請求")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBufferFlushesOnTimer(t *testing.T) {
	runner, idsCh := newBufferRunner(t)
	buf := NewWebhookBuffer(context.Background(), runner, 100, 100*time.Millisecond)
	t.Cleanup(func() { _ = buf.Shutdown(context.Background()) })

	buf.Add("t1", pkgsync.EntityInvoices, "inv-1")
	select {
	case <-idsCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timer 到期後未觸發 flush")
	}
}

func TestBufferFlushesOnShutdown(t *testing.T) {
	runner, idsCh := newBufferRunner(t)
	buf := NewWebhookBuffer(context.Background(), runner, 100, time.Hour)
	buf.Add("t1", pkgsync.EntityInvoices, "inv-1")
	buf.Add("t1", pkgsync.EntityInvoices, "inv-2")

	require.NoError(t, buf.Shutdown(context.Background()))
	select {
	case ids := <-idsCh:
		require.Len(t, strings.Split(ids, ","), 2, "Shutdown flush 應含剩餘 2 個 IDs")
	case <-time.After(time.Second):
		t.Fatal("Shutdown 未 flush 剩餘 pending")
	}
	require.NoError(t, buf.Shutdown(context.Background()), "重複 Shutdown 不得 panic")
}
