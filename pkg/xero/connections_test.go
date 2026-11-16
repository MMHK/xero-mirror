package xero

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetConnections(t *testing.T) {
	t.Parallel()
	authCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tenantId":"t1","tenantName":"A","tenantType":"COMPANY"},
			{"tenantId":"t2","tenantName":"B","tenantType":"COMPANY"}]`)
	}))
	defer srv.Close()

	conns, err := (&OAuth{ConnectionsURL: srv.URL}).GetConnections(context.Background(), "access-123")
	require.NoError(t, err)
	require.Equal(t, "Bearer access-123", <-authCh)
	require.Len(t, conns, 2)
	require.Equal(t, "t1", conns[0].TenantID)
	require.Equal(t, "B", conns[1].TenantName)
}
