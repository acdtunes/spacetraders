package grpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// TestAutosizerTreasuryReaderSeesPostSpendCreditsAfterBuy is the headline
// money-safety proof for the shared agent cache (sp-oszc), driven end-to-end
// through the REAL money guard — autosizerTreasuryReader.Treasury, the credit
// source for the 25%-treasury + reserve-floor BUY guards — not the client in
// isolation. The cache lives in the one shared *api.SpaceTradersClient that both
// the guard and every spender hold, so this proves the guard can NEVER be fed a
// pre-spend (stale-HIGH) balance after we have spent:
//
//	read treasury (caches N) -> buy (spends, invalidates) -> read treasury again
//	=> the guard observes the REDUCED balance, never the cached N.
//
// If the purchase-path invalidation were removed, the second Treasury read would
// return the stale 1000 and this test would fail — the over-spend hazard.
func TestAutosizerTreasuryReaderSeesPostSpendCreditsAfterBuy(t *testing.T) {
	var mu sync.Mutex
	credits := 1000
	agentReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/my/agent":
			agentReads++
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"accountId":"A","symbol":"AGENT","headquarters":"X1-HQ-A1","credits":%d,"startingFaction":"COSMIC"}}`, credits)
		case strings.HasSuffix(r.URL.Path, "/purchase"):
			credits -= 300
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"data":{"agent":{"credits":%d},"transaction":{"totalPrice":300,"units":10}}}`, credits)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := api.NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	reader := &autosizerTreasuryReader{api: client}
	ctx := common.WithPlayerToken(context.Background(), "player-token")

	// The guard reads treasury: 1000 is cached.
	before, ok, err := reader.Treasury(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(1000), before)

	// A buy spends 300 through the SAME shared client -> cache invalidated.
	_, err = client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 10, "player-token")
	require.NoError(t, err)

	// The guard reads treasury again: it MUST see the reduced 700, never the
	// stale-high 1000. This is the over-spend guarantee, at the money guard.
	after, ok, err := reader.Treasury(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(700), after,
		"the money guard must read POST-spend credits (700), never the stale pre-spend 1000")

	// Sanity: two live agent reads (pre-spend + post-invalidation), proving the
	// second was a genuine re-fetch, not a cache hit.
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, agentReads)
}
