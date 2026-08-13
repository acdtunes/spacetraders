package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// Retirement is the operator's per-hull drain: the tour in flight finishes and sells
// normally, and the coordinator declines to plan another ONCE THE HOLD IS EMPTY. A retiring
// hull still holding cargo keeps relaunching so it can sell — the drain is the point, and a
// parked laden hull is worse than a trading one (RULINGS #4).

// retiringTradeHull is an idle trade hull the operator has marked retiring, holding `units`.
func retiringTradeHull(t *testing.T, symbol string, capacity, units int) *navigation.Ship {
	t.Helper()
	ship := tradeHullWithCargo(t, symbol, capacity, units)
	ship.MarkRetiring(clockAt(0))
	return ship
}

// A DRAINED retiring hull is never planned another tour. Empty hold => scrap-ready, and the
// decline is logged so the operator can see the retirement actually took.
func TestTradeReconcile_RetiringDrainedHull_DeclinesAnotherTour(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		retiringTradeHull(t, "TR-RETIRED", 40, 0),
		tradeHullWithCargo(t, "TR-NORMAL", 40, 0),
	}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 1, launched, "only the un-retired hull is planned a tour")
	require.Equal(t, []string{"TR-NORMAL"}, launcher.launchedSymbols(),
		"a drained retiring hull is never planned another tour")
	require.True(t, logger.loggedContaining("retiring", "TR-RETIRED"),
		"the declined relaunch names the hull and its reason")
}

// A retiring hull still LADEN keeps relaunching: that relaunch is how it sells. Declining
// here would strand a laden hull for good, which is strictly worse than letting it trade.
func TestTradeReconcile_RetiringLadenHull_StillRelaunchesToDrain(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		retiringTradeHull(t, "TR-LADEN", 40, 20),
	}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 1, launched)
	require.Equal(t, []string{"TR-LADEN"}, launcher.launchedSymbols(),
		"a retiring hull that still holds cargo relaunches so it can sell it")
}

// Marking a hull mid-tour changes nothing about the tour in flight: the claim stands, no
// container is stopped, nothing is relaunched onto it. The mark only governs the NEXT tour
// (RULINGS #3 — the retirement never becomes a second writer of the hull).
func TestTradeReconcile_RetiringHullMidTour_LeavesTheFlyingTourAlone(t *testing.T) {
	flying := retiringTradeHull(t, "TR-FLYING", 40, 20)
	require.NoError(t, flying.AssignToContainer("tour-live-TR-FLYING", clockAt(0)))
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{flying}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launchedSymbols(), "nothing is launched onto a hull already flying")
	require.Equal(t, "tour-live-TR-FLYING", flying.ContainerID(),
		"the in-flight tour keeps its claim and finishes normally")
	require.True(t, flying.IsAssigned(), "the retirement mark never releases a live claim")
}

// Ships inert (PLAYBOOK 10): with no hull marked, every idle hull relaunches exactly as
// before, so an unmarked fleet is byte-identical.
func TestTradeReconcile_NothingMarkedRetiring_ByteIdentical(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		tradeHullWithCargo(t, "TR-A", 40, 0),
		tradeHullWithCargo(t, "TR-B", 40, 20),
	}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.Equal(t, 2, launched)
	require.Equal(t, []string{"TR-A", "TR-B"}, launcher.launchedSymbols())
	require.False(t, logger.loggedContaining("retiring"), "no retirement decision is even considered")
}
