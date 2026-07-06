package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegisterPostsSymbolAndFactionWithAccountTokenAndParsesAgentToken(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"token":"agent-jwt-token","agent":{"symbol":"ORION","startingFaction":"COSMIC"}}}`)
	}))
	t.Cleanup(server.Close)
	client, _ := newRetryTestClient(server.URL, 3)

	result, err := client.Register(context.Background(), "account-token-abc", "ORION", "COSMIC")

	require.NoError(t, err)
	require.Equal(t, "/register", gotPath)
	require.Equal(t, "Bearer account-token-abc", gotAuth)
	require.Equal(t, "ORION", gotBody["symbol"])
	require.Equal(t, "COSMIC", gotBody["faction"])
	require.Equal(t, "agent-jwt-token", result.Token)
	require.Equal(t, "ORION", result.AgentSymbol)
	require.Equal(t, "COSMIC", result.Faction)
}

func TestRegisterReturnsErrorOnNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":{"message":"agent symbol already taken","code":4111}}`)
	}))
	t.Cleanup(server.Close)
	client, _ := newRetryTestClient(server.URL, 0)

	result, err := client.Register(context.Background(), "account-token-abc", "TAKEN", "COSMIC")

	require.Error(t, err)
	require.Nil(t, result)
}
