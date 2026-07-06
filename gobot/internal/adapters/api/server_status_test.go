package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetServerStatusParsesResetDateAndServerResets(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"resetDate":"2026-06-22","serverResets":{"next":"2026-07-13T16:00:00.000Z","frequency":"fortnightly"}}`)
	}))
	t.Cleanup(server.Close)
	client, _ := newRetryTestClient(server.URL, 3)

	status, err := client.GetServerStatus(context.Background())

	require.NoError(t, err)
	require.Equal(t, "/", gotPath)
	require.Equal(t, "2026-06-22", status.ResetDate)
	require.Equal(t, "2026-07-13T16:00:00.000Z", status.ServerResets.Next)
	require.Equal(t, "fortnightly", status.ServerResets.Frequency)
}
