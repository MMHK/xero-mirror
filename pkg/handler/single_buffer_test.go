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

// newSingleBufferRunner 建立指向 httptest.Server 的 Runner；回傳請求路徑的收集 channel。
func newSingleBufferRunner(t *testing.T) (*pkgsync.Runner, chan string) {
	t.Helper()
	pathsCh := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		pathsCh <- req.URL.Path
		// 回傳符合 single get 格式的回應（包在 array 裡）
		if strings.HasPrefix(req.URL.Path, "/CreditNotes/") {
			_, _ = w.Write([]byte(`{"CreditNotes":[]}`))
		} else if strings.HasPrefix(req.URL.Path, "/Payments/") {
			_, _ = w.Write([]byte(`{"Payments":[]}`))
		} else {
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	client, err := xero.NewClientWithTokenSource(srv.URL, func(context.Context, string) (string, error) {
		return "tok", nil
	})
	require.NoError(t, err)
	return pkgsync.NewRunner(client, nil), pathsCh
}

func TestSingleBufferFlushesOnTimer(t *testing.T) {
	runner, pathsCh := newSingleBufferRunner(t)
	buf := NewSingleGetBuffer(context.Background(), runner, 100*time.Millisecond)
	t.Cleanup(func() { _ = buf.Shutdown(context.Background()) })

	buf.Add("t1", pkgsync.EntityCreditNotes, "cn-1")
	select {
	case path := <-pathsCh:
		require.Equal(t, "/CreditNotes/cn-1", path, "應以 single get 端點抓取")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timer 到期後未觸發 flush")
	}
}

func TestSingleBufferDeduplicates(t *testing.T) {
	runner, pathsCh := newSingleBufferRunner(t)
	buf := NewSingleGetBuffer(context.Background(), runner, 100*time.Millisecond)
	t.Cleanup(func() { _ = buf.Shutdown(context.Background()) })

	// 加入重複的 (tenant, entity, id)
	buf.Add("t1", pkgsync.EntityPayments, "pay-1")
	buf.Add("t1", pkgsync.EntityPayments, "pay-1")
	buf.Add("t1", pkgsync.EntityPayments, "pay-1")

	select {
	case path := <-pathsCh:
		require.Equal(t, "/Payments/pay-1", path)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timer 到期後未觸發 flush")
	}
	// 應該只有一個請求
	select {
	case <-pathsCh:
		t.Fatal("去重後不應有第二個請求")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSingleBufferFlushesOnShutdown(t *testing.T) {
	runner, pathsCh := newSingleBufferRunner(t)
	buf := NewSingleGetBuffer(context.Background(), runner, time.Hour) // 長延遲，不靠 timer
	buf.Add("t1", pkgsync.EntityCreditNotes, "cn-1")
	buf.Add("t1", pkgsync.EntityCreditNotes, "cn-2")

	require.NoError(t, buf.Shutdown(context.Background()))

	var paths []string
	timeout := time.After(time.Second)
	for len(paths) < 2 {
		select {
		case p := <-pathsCh:
			paths = append(paths, p)
		case <-timeout:
			t.Fatalf("Shutdown 未 flush 全部 pending，只收到 %d 個請求", len(paths))
		}
	}
	require.Len(t, paths, 2, "Shutdown flush 應含剩餘 2 個 single get 請求")
	require.NoError(t, buf.Shutdown(context.Background()), "重複 Shutdown 不得 panic")
}

func TestSingleBufferMultipleEntities(t *testing.T) {
	runner, pathsCh := newSingleBufferRunner(t)
	buf := NewSingleGetBuffer(context.Background(), runner, 100*time.Millisecond)
	t.Cleanup(func() { _ = buf.Shutdown(context.Background()) })

	buf.Add("t1", pkgsync.EntityCreditNotes, "cn-1")
	buf.Add("t1", pkgsync.EntityPayments, "pay-1")

	var paths []string
	timeout := time.After(time.Second)
	for len(paths) < 2 {
		select {
		case p := <-pathsCh:
			paths = append(paths, p)
		case <-timeout:
			t.Fatalf("未收到全部請求，只收到 %d 個", len(paths))
		}
	}
	require.Len(t, paths, 2)
	require.Contains(t, paths, "/CreditNotes/cn-1")
	require.Contains(t, paths, "/Payments/pay-1")
}
