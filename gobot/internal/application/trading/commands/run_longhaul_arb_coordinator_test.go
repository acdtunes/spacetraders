package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes (fakeTradeShipRepo, clockAt, tradeCaptureLogger, tradeCtx are shared with
// run_trade_fleet_coordinator_test.go — same package) --------------------------------

// fakeLongHaulLauncher records the launch specs and can be told to fail (globally or per
// hull). It never touches ship state — proving the coordinator's dispatch is a pure read
// of the fleet.
type fakeLongHaulLauncher struct {
	launches []LongHaulLaunchSpec
	failAll  error
	failFor  map[string]error
}

func (l *fakeLongHaulLauncher) LaunchLongHaul(_ context.Context, spec LongHaulLaunchSpec) (string, error) {
	if l.failFor != nil {
		if err, ok := l.failFor[spec.ShipSymbol]; ok {
			return "", err
		}
	}
	if l.failAll != nil {
		return "", l.failAll
	}
	l.launches = append(l.launches, spec)
	return "longhaul-container-" + spec.ShipSymbol, nil
}

func (l *fakeLongHaulLauncher) launchedSymbols() []string {
	out := make([]string, 0, len(l.launches))
	for _, s := range l.launches {
		out = append(out, s.ShipSymbol)
	}
	return out
}

// ---- ship builder ----------------------------------------------------------

// longHaulHull builds a hull with an explicit fleet tag, role, and cargo capacity so a
// test can express isolation (tag), the #7 frigate exclusion (role/symbol), and the
// probe exclusion (cargoCap 0) precisely. Idle (IN_ORBIT, unassigned) unless a test
// assigns/reserves it after.
func longHaulHull(t *testing.T, symbol, fleet, role string, cargoCap int) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-LH-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, cargoCap, cargo, 30, "FRAME_FREIGHTER", role, nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	if fleet != "" {
		ship.SetDedicatedFleet(fleet)
	}
	return ship
}

func newLongHaulHandler(repo *fakeTradeShipRepo, launcher *fakeLongHaulLauncher, clock shared.Clock) *LongHaulArbFleetCoordinatorHandler {
	h := NewLongHaulArbFleetCoordinatorHandler(repo, clock)
	if launcher != nil {
		h.SetLongHaulLauncher(launcher)
	}
	return h
}

func longHaulCmd() *LongHaulArbFleetCoordinatorCommand {
	return &LongHaulArbFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "longhaul-coord-1",
		AgentSymbol: "TORWIND",
	}
}

// ---- tests -----------------------------------------------------------------

// ISOLATION (design §1): the coordinator dispatches a worker for ONLY the idle,
// cargo-bearing, long-haul-tagged hulls — never contract/warehouse/untagged hulls
// (isolation by tag), never a hull already running a worker, never a captain-reserved
// hull, and never a 0-cargo probe. This is the whole "touches ONLY the tag" guarantee in
// one observable: exactly which hulls reach the launcher (the driven-port boundary).
func TestLongHaulReconcile_Isolation_DispatchesOnlyIdleLongHaulHulls(t *testing.T) {
	runningLH := longHaulHull(t, "LH-RUN-C", longHaulFleet, "HAULER", 60)
	require.NoError(t, runningLH.AssignToContainer("longhaul-live-C", clockAt(0)))
	reservedLH := longHaulHull(t, "LH-RESV-D", longHaulFleet, "HAULER", 60)
	require.NoError(t, reservedLH.ReserveByCaptain("captain manual use", clockAt(0)))

	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		longHaulHull(t, "LH-IDLE-A", longHaulFleet, "HAULER", 60), // dispatch
		longHaulHull(t, "LH-IDLE-B", longHaulFleet, "HAULER", 40), // dispatch
		runningLH,  // running: not re-dispatched
		reservedLH, // captain reserved: skipped
		longHaulHull(t, "LH-PROBE-E", longHaulFleet, "SATELLITE", 0), // 0-cargo probe: skipped
		longHaulHull(t, "TR-F", tradeFleet, "HAULER", 60),            // other fleet: isolation
		longHaulHull(t, "CT-G", "contract", "HAULER", 60),            // other fleet: isolation
		longHaulHull(t, "WH-H", "warehouse", "HAULER", 60),           // other fleet: isolation
		longHaulHull(t, "UNTAG-I", "", "HAULER", 60),                 // untagged: isolation
	}}
	launcher := &fakeLongHaulLauncher{}
	logger := &tradeCaptureLogger{}
	h := newLongHaulHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 2, launched)
	require.Equal(t, []string{"LH-IDLE-A", "LH-IDLE-B"}, launcher.launchedSymbols(),
		"only idle, cargo-bearing, long-haul-tagged hulls are dispatched")

	spec := launcher.launches[0]
	require.Equal(t, "TORWIND", spec.AgentSymbol)
	require.Equal(t, 1, spec.PlayerID)
	require.Equal(t, longHaulIterationsContinuous, spec.Iterations, "workers run continuous (-1)")
}

// RULINGS #7: the command frigate is NEVER a long-haul hull — even if the operator
// mis-tags it long-haul. Both identities of a command hull are rejected (role=="COMMAND"
// and the "-1" symbol suffix), so a fat-fingered `fleet add` can never put the frigate on
// a long-haul run.
func TestLongHaulReconcile_ExcludesCommandFrigate_EvenWhenTaggedLongHaul(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		longHaulHull(t, "TORWIND-1", longHaulFleet, "HAULER", 60),    // command by "-1" suffix
		longHaulHull(t, "LH-CMD-ROLE", longHaulFleet, "COMMAND", 60), // command by role
	}}
	launcher := &fakeLongHaulLauncher{}
	logger := &tradeCaptureLogger{}
	h := newLongHaulHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launches, "the command frigate is never dispatched on a long-haul run (RULINGS #7)")
}

// A reconcile that cannot launch (launcher never wired) must fail CLOSED — an error, not a
// silent "nothing to do" that reads as success while an idle long-haul hull sits undispatched.
func TestLongHaulReconcile_FailsClosedWhenLauncherUnwired(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{longHaulHull(t, "LH-IDLE-A", longHaulFleet, "HAULER", 60)}}
	logger := &tradeCaptureLogger{}
	h := newLongHaulHandler(repo, nil, clockAt(1000)) // launcher NOT wired

	launched, err := h.reconcileOnce(tradeCtx(logger), longHaulCmd())
	require.Error(t, err)
	require.Equal(t, 0, launched)
}

// UNCAPPED CONCURRENCY (Admiral order): the coordinator launches a worker for
// EVERY idle tagged long-haul hull each tick — the total-exposure CONCURRENCY ceiling is
// removed, so tagged hulls never sit idle behind it even when the number already running
// meets or exceeds the OLD cap (default 2M/1M = 2). It still threads the money envelope
// (per-haul cap + total-exposure figure) to each worker it launches; spend stays
// fail-closed PER BUY at the reserve-floor fence inside the worker (unchanged).
func TestLongHaulReconcile_LaunchesAllIdleHulls_UncappedConcurrency_ThreadsEnvelope(t *testing.T) {
	// The production wedge: two hulls already in flight (T59+T82) SATURATED the old cap of 2,
	// so the two tagged-but-idle hulls (B2+B3) were held every tick and never launched.
	runningT59 := longHaulHull(t, "TORWIND-T59", longHaulFleet, "HAULER", 60)
	require.NoError(t, runningT59.AssignToContainer("lh-live-T59", clockAt(0)))
	runningT82 := longHaulHull(t, "TORWIND-T82", longHaulFleet, "HAULER", 60)
	require.NoError(t, runningT82.AssignToContainer("lh-live-T82", clockAt(0)))
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		runningT59, // running — under the old cap these two saturated it
		runningT82, // running
		longHaulHull(t, "TORWIND-B2", longHaulFleet, "HAULER", 60), // idle, was held at the cap
		longHaulHull(t, "TORWIND-B3", longHaulFleet, "HAULER", 60), // idle, was held at the cap
	}}
	launcher := &fakeLongHaulLauncher{}
	h := newLongHaulHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 2, launched, "both idle hulls launch despite 2 already running (no concurrency ceiling)")
	require.Equal(t, []string{"TORWIND-B2", "TORWIND-B3"}, launcher.launchedSymbols(),
		"every idle tagged hull is dispatched, none held behind an exposure cap")
	require.Equal(t, defaultLongHaulPerHaulCap, launcher.launches[0].PerHaulCap, "per-haul cap still threaded to the worker")
	require.Equal(t, defaultLongHaulTotalExposureCap, launcher.launches[0].TotalExposureCap, "total-exposure figure still threaded to the worker")
}

// A per-hull launch failure is logged and skipped — the rest of the fleet is still
// serviced (RULINGS #1), never an aborted pass.
func TestLongHaulReconcile_PerHullLaunchFailure_ServicesTheRest(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		longHaulHull(t, "LH-IDLE-A", longHaulFleet, "HAULER", 60),
		longHaulHull(t, "LH-IDLE-B", longHaulFleet, "HAULER", 60),
	}}
	launcher := &fakeLongHaulLauncher{failFor: map[string]error{"LH-IDLE-A": errors.New("claimed between snapshot and launch")}}
	logger := &tradeCaptureLogger{}
	h := newLongHaulHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), longHaulCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Equal(t, []string{"LH-IDLE-B"}, launcher.launchedSymbols())
}
