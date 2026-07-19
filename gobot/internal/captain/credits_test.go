package watchkeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// fakeAgentAPI is a recording stand-in for *api.SpaceTradersClient's GetAgent,
// returning a fixed live-credits value (or an error) so tests can prove the
// wake gate evaluates the LIVE agent-API number, not a ledger reconstruction.
type fakeAgentAPI struct {
	credits  int
	err      error
	calls    int
	gotToken string
}

func (f *fakeAgentAPI) GetAgent(_ context.Context, token string) (*player.AgentData, error) {
	f.calls++
	f.gotToken = token
	if f.err != nil {
		return nil, f.err
	}
	return &player.AgentData{Credits: f.credits}, nil
}

func seedBalance(t *testing.T, sup *Supervisor, playerID, balanceAfter int) {
	t.Helper()
	require.NoError(t, sup.db.Create(&persistence.TransactionModel{
		ID: "t-seed", PlayerID: playerID, Timestamp: time.Now(), TransactionType: "SELL_CARGO",
		Category: "TRADING_REVENUE", Amount: 1, BalanceBefore: balanceAfter - 1, BalanceAfter: balanceAfter,
	}).Error)
}

// D3 (1): the gate must evaluate the LIVE agent-API credits (what the captain
// sees via `player info`), not the contract-anchored ledger reconstruction —
// the two can disagree. Here the API says 1,150,000 (>= the declared 1,100,000
// bound) while the reconstruction says only 700,000 (< bound): a wake proves
// the API value drove the decision.
func TestTickWakesOnLiveAgentCreditsNotLedgerReconstruction(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	sup.lastSession = time.Now() // heartbeat cadence nowhere near due
	seedBalance(t, sup, s.playerID, 700000)

	api := &fakeAgentAPI{credits: 1150000}
	sup.SetAgentAPI(api, "captain-token")

	above := 1100000
	require.NoError(t, SaveWakePolicy(NewWorkspace(s.dir).StatePath(), WakePolicy{CreditsAbove: &above}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran, "live credits (1,150,000) are at/above the declared bound: must wake")
	require.Len(t, gw.nudges, 1, "credits-triggered wake with zero events is a heartbeat-style nudge")
	require.Equal(t, 1150000, sup.lastCredits, "s.lastCredits reflects the LIVE API value")
	require.Positive(t, api.calls, "the live agent API was actually consulted")
	require.Equal(t, "captain-token", api.gotToken)
}

// D3 (2): a live-fetch error is logged (never silent) and falls back to the
// ledger reconstruction value.
func TestRefreshCreditsFallsBackAndLogsOnAPIError(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	seedBalance(t, sup, s.playerID, 700000)
	sup.SetAgentAPI(&fakeAgentAPI{err: errors.New("api 500")}, "tok")

	out := captureOutput(t, func() {
		sup.refreshCredits(context.Background())
	})

	require.Equal(t, 700000, sup.lastCredits, "falls back to the reconstruction value")
	require.Contains(t, out, "live credits fetch failed", "the fetch failure must be logged")
}

// D3 (3): with no API wired (legacy path), behavior is identical to today —
// the reconstruction value, and no live-fetch attempt or log line.
func TestRefreshCreditsLegacyPathUnchangedWhenAPIUnwired(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	seedBalance(t, sup, s.playerID, 640000)

	out := captureOutput(t, func() {
		sup.refreshCredits(context.Background())
	})

	require.Equal(t, 640000, sup.lastCredits)
	require.NotContains(t, out, "live credits fetch failed", "API not wired: no fetch attempted")
}

// D3 (4): when BOTH the API and the DB reconstruction fail, the last known
// value is retained (never a spurious 0) and BOTH failures are logged.
func TestRefreshCreditsRetainsLastKnownWhenBothFail(t *testing.T) {
	sup, _, _ := newBridgeSupervisor(t)
	sup.lastCredits = 424242 // prior known value
	sup.SetAgentAPI(&fakeAgentAPI{err: errors.New("api 500")}, "tok")

	// Break the DB so the reconstruction fallback also errors.
	sqlDB, err := sup.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	out := captureOutput(t, func() {
		sup.refreshCredits(context.Background())
	})

	require.Equal(t, 424242, sup.lastCredits, "both sources down: retain last known, never reset to 0")
	require.Contains(t, out, "live credits fetch failed", "API failure logged")
	require.Contains(t, out, "reconstruction", "reconstruction failure logged too")
}
