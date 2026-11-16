package xero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestClient 建立指向 httptest.Server 的 client，並注入固定 access token。
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(nil)
	c.tokenSource = func(context.Context, string) (string, error) { return "tok", nil }
	require.NoError(t, c.setBaseURL(srv.URL))
	return c
}

func TestGetSendsHeaders(t *testing.T) {
	t.Parallel()
	got := make(chan http.Header, 1)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		fmt.Fprint(w, `{}`)
	})

	_, err := c.Get(context.Background(), "t1", "/Invoices", nil, nil)
	require.NoError(t, err)

	h := <-got
	require.Equal(t, "Bearer tok", h.Get("Authorization"))
	require.Equal(t, "t1", h.Get("Xero-tenant-id"))
	require.Equal(t, "application/json", h.Get("Accept"))
}

func TestGetPagesStopsAtShortPage(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reqs, calls := 0, 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, "["+strings.Repeat(`{"x":1},`, pageSize-1)+`{"x":1}]`)
			return
		}
		fmt.Fprint(w, `[{"x":1},{"x":2},{"x":3}]`)
	})

	fn := func(page int, body []byte) (int, error) {
		calls++
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return 0, err
		}
		return len(items), nil
	}
	require.NoError(t, c.GetPages(context.Background(), "t1", "/Invoices", nil, nil, fn))
	require.Equal(t, 2, calls)
	require.Equal(t, 2, reqs)
}

func TestGetRetriesOn429(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reqs := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		first := reqs == 1
		mu.Unlock()
		if first {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	body, err := c.Get(context.Background(), "t1", "/Invoices", nil, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(body))
	require.Equal(t, 2, reqs)
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"Message":"not found"}`)
	})

	_, err := c.Get(context.Background(), "t1", "/Invoices", nil, nil)
	require.Error(t, err)
	require.True(t, IsNotFound(err))
	require.False(t, IsRateLimited(err))
}

func TestGetRetryExhausted(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	reqs := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := c.Get(context.Background(), "t1", "/Invoices", nil, nil)
	require.Error(t, err)
	require.True(t, IsRateLimited(err))
	require.Equal(t, maxRetries+1, reqs)
}
