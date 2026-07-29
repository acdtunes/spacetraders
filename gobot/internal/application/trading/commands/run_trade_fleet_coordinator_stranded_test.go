package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// PRODUCTION REPRODUCTION, the resolving half (agent TORWIND, 4 containers):
//
//	completion refused (honest-completion contract): stranded cargo: 18 ANTIMATTER
//	still aboard at X1-MY3-EE1D (tour-bought, unsold) - reporting failure
//
// The refusal is right and stays right. What was missing is the next move. The tour's
// only ungated way out of a laden exit is the exit sweep, and that sweep can only sell
// where the hull already stands: it collects the CURRENT system's listings and takes the
// best non-EXPORT bid among them, so a hold nothing in this system will buy leaves it
// with nothing to do. Its own log says exactly where that ends — the load is "stranded on
// an idle hull until it is re-tasked or hand-recovered".
//
// Nothing re-tasked it. The hull came back to the fleet marked like any other failure, so
// this coordinator relaunched the same tour onto the same ground it had just proved
// cannot absorb the cargo — which inherits the same unsold obligation and is refused
// again. A container burned per turn, the cargo never moving.
//
// The route out already exists and was simply never pointed at this case:
// reposition-reach (RepositionReachEscalated) broadens discovery to 2-4 gate hops so a
// hull whose ground is dead HERE moves to a system it could not otherwise see. A hull
// released by the honest-completion veto is the definitive case of dead ground — the run
// did not merely trade badly, it ended unable to sell what it holds — so its relaunch
// goes out with reach ARMED instead of pointed back at the same markets.
//
// This changes nothing about the contract itself: the run still reported failure, and
// still may not claim success.
func TestTradeReconcile_StrandVetoedHull_RelaunchesWithReachArmed(t *testing.T) {
	hull := parkedTradeHull(t, "TORWIND-19", 0, common.ReleaseReasonHonestCompletionVeto)
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hull}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000)) // well past the 180s cooldown

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())

	require.NoError(t, err)
	require.Equal(t, 1, launched, "a stranded hull must still be relaunched, not abandoned")
	require.Len(t, launcher.launches, 1)
	require.True(t, launcher.launches[0].RepositionReachEscalated,
		"a hull released by the honest-completion veto must relaunch with reposition-reach ARMED — "+
			"relaunching onto the ground that just refused its cargo repeats the failure verbatim")
}

// The escalation must be earned, not universal. A hull parked by an ordinary tour exit
// relaunches exactly as before — reach unarmed — so the broadened, more expensive
// discovery is spent only on hulls that actually proved their ground dead. This is what
// fails if the arming is ever widened to "any failure".
func TestTradeReconcile_OrdinaryExit_RelaunchesWithoutReach(t *testing.T) {
	hull := parkedTradeHull(t, "TORWIND-19", 0, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hull}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())

	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Len(t, launcher.launches, 1)
	require.False(t, launcher.launches[0].RepositionReachEscalated,
		"an ordinary exit must not buy the broadened reach — only a proven-dead ground earns it")
}
