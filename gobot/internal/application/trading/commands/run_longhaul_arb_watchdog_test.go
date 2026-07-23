package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// runningLongHaulHull models a long-haul hull mid-episode: a live container claim (parked).
func runningLongHaulHull(t *testing.T, symbol, containerID string) *navigation.Ship {
	t.Helper()
	ship := longHaulHull(t, symbol, longHaulFleet, "HAULER", 60)
	require.NoError(t, ship.AssignToContainer(containerID, clockAt(0)))
	return ship
}

// inTransitLongHaulHull models a long-haul hull mid-multi-hop: a live claim AND
// NavStatusInTransit — the legitimately-long-leg case the watchdog must never kill.
func inTransitLongHaulHull(t *testing.T, symbol, containerID string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-LH-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(60, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 60, cargo, 30, "FRAME_FREIGHTER", "HAULER", nil, navigation.NavStatusInTransit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(longHaulFleet)
	require.NoError(t, ship.AssignToContainer(containerID, clockAt(0)))
	return ship
}

func newLongHaulWatchdogHandler(repo *fakeTradeShipRepo, launcher *fakeLongHaulLauncher, liveness *fakeTourLiveness, stopper *fakeTourStopper, clock shared.Clock) *LongHaulArbFleetCoordinatorHandler {
	h := NewLongHaulArbFleetCoordinatorHandler(repo, clock)
	h.SetLongHaulLauncher(launcher)
	if liveness != nil {
		h.SetTourLiveness(liveness)
	}
	if stopper != nil {
		h.SetTourStopper(stopper)
	}
	return h
}

// ONE WATCHDOG, BOTH FLEETS (sp-mepj): a RUNNING long-haul worker silent past the stall
// threshold is HUNG — the SHARED sp-m3122 engine kills its container and relaunches a fresh
// long-haul worker (through the long-haul launcher, carrying the envelope). This is the
// design's "one watchdog serves trade tours AND long-haul", exercised through the long-haul
// binding.
func TestLongHaulWatchdog_HungWorker_KilledAndRelaunchedFresh(t *testing.T) {
	now := clockAt(1000) // 1000s past baseTime >> 720s threshold
	hung := runningLongHaulHull(t, "LH-77", "lh-live-77")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{"lh-live-77": baseTime}}
	stopper := &fakeTourStopper{}
	launcher := &fakeLongHaulLauncher{}
	logger := &tradeCaptureLogger{}

	h := newLongHaulWatchdogHandler(repo, launcher, liveness, stopper, now)

	launched, err := h.reconcileOnce(tradeCtx(logger), longHaulCmd())

	require.NoError(t, err)
	require.Equal(t, []string{"lh-live-77"}, stopper.stopped, "the hung worker's container is killed")
	require.Equal(t, []string{"LH-77"}, launcher.launchedSymbols(), "a fresh long-haul worker is relaunched for the hung hull")
	require.Equal(t, 1, launched)
	require.True(t, logger.loggedContaining("no progress", "LH-77"), "the kill is logged with the honest reason")
	require.Equal(t, defaultLongHaulPerHaulCap, launcher.launches[0].PerHaulCap, "the relaunched worker carries the envelope")
}

// A long-haul worker on a legitimately-long multi-hop leg (IN_TRANSIT) is NEVER killed, even
// when its last logged progress predates the threshold — a flying hull is progressing.
func TestLongHaulWatchdog_InTransitLongLeg_NotKilled(t *testing.T) {
	now := clockAt(1000)
	flying := inTransitLongHaulHull(t, "LH-79", "lh-live-79")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{flying}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{"lh-live-79": baseTime}}
	stopper := &fakeTourStopper{}
	launcher := &fakeLongHaulLauncher{}

	h := newLongHaulWatchdogHandler(repo, launcher, liveness, stopper, now)

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), longHaulCmd())

	require.NoError(t, err)
	require.Empty(t, stopper.stopped, "a hull mid-multi-hop is never killed")
	require.Empty(t, launcher.launches)
	require.Equal(t, 0, launched)
}

// sp-m3122 part 3: on restart (the first reconcile pass) the long-haul coordinator reclaims
// absorption reservations held by dead containers — reused verbatim, keyed on container
// liveness — and does not sweep every steady tick.
func TestLongHaulWatchdog_FirstPass_ReclaimsDeadContainerAbsorption(t *testing.T) {
	now := clockAt(10)
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{}}
	reclaimer := &fakeAbsorptionReclaimer{count: 3}
	launcher := &fakeLongHaulLauncher{}

	h := NewLongHaulArbFleetCoordinatorHandler(repo, now)
	h.SetLongHaulLauncher(launcher)
	h.SetAbsorptionReclaimer(reclaimer)

	_, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 1, reclaimer.calls, "the restart reclaim runs once on the first pass")
	require.Equal(t, []shared.PlayerID{shared.MustNewPlayerID(1)}, reclaimer.playerIDs)

	_, err = h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 1, reclaimer.calls, "no needless per-tick sweep once the restart cleanup is done")
}
