package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// baseTime is the fixed epoch every test clock is offset from, so release times and
// "now" are exact and the cooldown math is deterministic.
var baseTime = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

func clockAt(offsetSecs int) *shared.MockClock {
	return &shared.MockClock{CurrentTime: baseTime.Add(time.Duration(offsetSecs) * time.Second)}
}

// ---- fakes -----------------------------------------------------------------

// tradeCaptureLogger records message text so tests can assert the honest
// cooldown/relaunch/cap reasons the coordinator logs.
type tradeCaptureLogger struct{ messages []string }

func (l *tradeCaptureLogger) Log(_, message string, _ map[string]interface{}) {
	l.messages = append(l.messages, message)
}

func (l *tradeCaptureLogger) loggedContaining(subs ...string) bool {
	for _, m := range l.messages {
		all := true
		for _, s := range subs {
			if !strings.Contains(m, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// fakeTradeShipRepo is a ShipRepository over a fixed roster; idle/assignment state is
// derived from the entities themselves, exactly like the DB round-trip.
type fakeTradeShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *fakeTradeShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

// fakeTourLauncher records the launch specs and can be told to fail (globally or per
// hull). It never touches ship state — proving the coordinator's relaunch is a pure
// read of the fleet.
type fakeTourLauncher struct {
	launches []TourLaunchSpec
	failAll  error
	failFor  map[string]error
}

func (l *fakeTourLauncher) LaunchTour(_ context.Context, spec TourLaunchSpec) (string, error) {
	if l.failFor != nil {
		if err, ok := l.failFor[spec.ShipSymbol]; ok {
			return "", err
		}
	}
	if l.failAll != nil {
		return "", l.failAll
	}
	l.launches = append(l.launches, spec)
	return "tour-container-" + spec.ShipSymbol, nil
}

func (l *fakeTourLauncher) launchedSymbols() []string {
	out := make([]string, 0, len(l.launches))
	for _, s := range l.launches {
		out = append(out, s.ShipSymbol)
	}
	return out
}

// ---- ship builders ---------------------------------------------------------

func tradeHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-TR-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(tradeFleet)
	return ship
}

// parkedTradeHull models a trade hull whose prior tour made an honest exit at
// releaseSecs: it was claimed by a tour container, then released (ForceRelease stamps
// released_at + the exit reason), so it is now idle with a real cooldown anchor.
func parkedTradeHull(t *testing.T, symbol string, releaseSecs int, exitReason string) *navigation.Ship {
	t.Helper()
	ship := tradeHull(t, symbol)
	require.NoError(t, ship.AssignToContainer("tour-prev-"+symbol, clockAt(releaseSecs-300)))
	ship.ForceRelease(exitReason, clockAt(releaseSecs))
	return ship
}

// runningTradeHull models a trade hull mid-tour: a live container claim.
func runningTradeHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	ship := tradeHull(t, symbol)
	require.NoError(t, ship.AssignToContainer("tour-live-"+symbol, clockAt(0)))
	return ship
}

func reservedTradeHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	ship := tradeHull(t, symbol)
	require.NoError(t, ship.ReserveByCaptain("captain manual use", clockAt(0)))
	return ship
}

// ---- harness ---------------------------------------------------------------

func newTradeHandler(repo *fakeTradeShipRepo, launcher *fakeTourLauncher, clock shared.Clock) *RunTradeFleetCoordinatorHandler {
	h := NewRunTradeFleetCoordinatorHandler(repo, clock)
	if launcher != nil {
		h.SetTourLauncher(launcher)
	}
	return h
}

func tradeCtx(logger common.ContainerLogger) context.Context {
	return common.WithLogger(context.Background(), logger)
}

func tradeCmd() *RunTradeFleetCoordinatorCommand {
	return &RunTradeFleetCoordinatorCommand{
		PlayerID:           shared.MustNewPlayerID(1),
		ContainerID:        "trade-coord-1",
		AgentSymbol:        "TORWIND",
		Enabled:            true,
		CooldownSecs:       180,
		MaxConcurrentTours: 0, // unlimited
	}
}

// ---- tests -----------------------------------------------------------------

// Idle 'trade' hull past cooldown -> a CONTINUOUS tour is launched through the daemon
// path (the launcher), carrying the fleet-standard flags. The operation="trade" stamp
// itself is applied by StartTourRun (container_ops_tour.go), which this launcher wraps.
func TestTradeReconcile_IdlePastCooldown_LaunchesContinuousTour(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWIND-19", 0, "margins_died_both_systems")}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000)) // 1000s >> 180s cooldown

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Len(t, launcher.launches, 1)

	spec := launcher.launches[0]
	require.Equal(t, "TORWIND-19", spec.ShipSymbol)
	require.Equal(t, tourIterationsContinuous, spec.Iterations, "relaunched tours must be continuous (-1)")
	require.Equal(t, "TORWIND", spec.AgentSymbol)
	require.Equal(t, 1, spec.PlayerID)
	require.True(t, logger.loggedContaining("Relaunched continuous tour", "TORWIND-19"))
}

// A fresh trade hull that never ran a tour has no cooldown anchor -> launched at once.
func TestTradeReconcile_NeverTouredHull_LaunchesImmediately(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{tradeHull(t, "TORWIND-20")}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(0))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Equal(t, []string{"TORWIND-20"}, launcher.launchedSymbols())
}

// A hull mid-tour (live container claim) is never disturbed.
func TestTradeReconcile_MidTourHull_Untouched(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{runningTradeHull(t, "TORWIND-21")}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launches)
}

// Inside the cooldown window the parked hull is held, not relaunched.
func TestTradeReconcile_WithinCooldown_NoLaunch(t *testing.T) {
	// Parked at 900s, now 1000s -> 100s elapsed < 180s cooldown.
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWIND-22", 900, "sold_out_completion")}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launches)
	require.True(t, logger.loggedContaining("TORWIND-22", "cooling down"))
}

// The cooldown is DERIVED from the hull's persisted release time, so a coordinator
// restart (a brand-new handler with no in-memory state) still honors it — and once the
// window elapses, a fresh handler relaunches. No double-launch inside the window across
// the restart boundary.
func TestTradeReconcile_CooldownSurvivesCoordinatorRestart(t *testing.T) {
	parked := parkedTradeHull(t, "TORWIND-23", 900, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parked}}

	// Restart #1: still inside the window (now 1000s, 100s elapsed) -> no relaunch,
	// even though this handler has never seen the hull before.
	launcher1 := &fakeTourLauncher{}
	h1 := newTradeHandler(repo, launcher1, clockAt(1000))
	launched1, err := h1.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched1)
	require.Empty(t, launcher1.launches)

	// Restart #2: window elapsed (now 1100s, 200s >= 180s) -> a fresh handler relaunches.
	launcher2 := &fakeTourLauncher{}
	h2 := newTradeHandler(repo, launcher2, clockAt(1100))
	launched2, err := h2.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched2)
	require.Equal(t, []string{"TORWIND-23"}, launcher2.launchedSymbols())
}

// A captain-reserved hull is never relaunched (respect the captain's off-switch),
// while a co-resident idle hull still is.
func TestTradeReconcile_CaptainReservedHull_Skipped(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		reservedTradeHull(t, "TORWIND-24"),
		parkedTradeHull(t, "TORWIND-25", 0, "margins_died_both_systems"),
	}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Equal(t, []string{"TORWIND-25"}, launcher.launchedSymbols(), "reserved hull must not be relaunched")
}

// A hull unpinned from 'trade' (dedicated to another fleet) is invisible to this
// coordinator — the captain's per-hull, no-restart opt-out.
func TestTradeReconcile_UnpinnedHull_Skipped(t *testing.T) {
	unpinned := tradeHull(t, "TORWIND-26")
	unpinned.SetDedicatedFleet("contract") // moved off the trade fleet
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{unpinned}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launches)
}

// enabled:false makes the reconcile pass inert (the container still runs, so a config
// flip + restart re-arms it).
func TestTradeReconcile_Disabled_Inert(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWIND-27", 0, "margins_died_both_systems")}}
	launcher := &fakeTourLauncher{}
	cmd := tradeCmd()
	cmd.Enabled = false
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), cmd)
	require.NoError(t, err)
	require.Equal(t, 0, launched)
	require.Empty(t, launcher.launches)
}

// max_concurrent bounds simultaneous tours: 1 already running + cap 2 => exactly one of
// the three idle hulls is relaunched this tick.
func TestTradeReconcile_MaxConcurrentHonored(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		runningTradeHull(t, "TORWIND-30"),
		parkedTradeHull(t, "TORWIND-31", 0, "margins_died_both_systems"),
		parkedTradeHull(t, "TORWIND-32", 0, "margins_died_both_systems"),
		parkedTradeHull(t, "TORWIND-33", 0, "margins_died_both_systems"),
	}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	cmd := tradeCmd()
	cmd.MaxConcurrentTours = 2
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched, "1 running + cap 2 leaves room for exactly 1 relaunch")
	require.Len(t, launcher.launches, 1)
	require.Equal(t, "TORWIND-31", launcher.launches[0].ShipSymbol, "deterministic order picks the lowest symbol")
	require.True(t, logger.loggedContaining("max concurrent"))
}

// The coordinator only READS the exit reason to log it — it never rewrites the hull's
// release metadata, so honest-exit telemetry accumulates unchanged.
func TestTradeReconcile_ExitReasonUnaltered(t *testing.T) {
	parked := parkedTradeHull(t, "TORWIND-40", 0, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parked}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	_, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)

	require.NotNil(t, parked.Assignment())
	require.NotNil(t, parked.Assignment().ReleaseReason())
	require.Equal(t, "margins_died_both_systems", *parked.Assignment().ReleaseReason(), "exit reason must be untouched")
	require.True(t, logger.loggedContaining("prior exit: margins_died_both_systems"))
}

// A per-hull launch failure does not abort the pass — the rest of the fleet is still
// serviced (RULINGS #1: keep working).
func TestTradeReconcile_PerHullLaunchFailure_ContinuesFleet(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{
		parkedTradeHull(t, "TORWIND-50", 0, "margins_died_both_systems"),
		parkedTradeHull(t, "TORWIND-51", 0, "margins_died_both_systems"),
	}}
	launcher := &fakeTourLauncher{failFor: map[string]error{"TORWIND-50": errors.New("claimed between snapshot and launch")}}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 1, launched)
	require.Equal(t, []string{"TORWIND-51"}, launcher.launchedSymbols())
	require.True(t, logger.loggedContaining("Failed to relaunch", "TORWIND-50"))
}

// With no launcher wired the pass fails closed (an error) rather than silently
// reading as "nothing to do".
func TestTradeReconcile_LauncherNotWired_FailsClosed(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWIND-60", 0, "margins_died_both_systems")}}
	h := newTradeHandler(repo, nil, clockAt(1000)) // launcher intentionally unwired

	launched, err := h.reconcileOnce(tradeCtx(&tradeCaptureLogger{}), tradeCmd())
	require.Error(t, err)
	require.Equal(t, 0, launched)
}

// Handle returns promptly on a cancelled context and rejects a wrong request type.
func TestTradeHandle_ContextCancelledReturns(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{parkedTradeHull(t, "TORWIND-70", 0, "x")}}
	launcher := &fakeTourLauncher{}
	h := newTradeHandler(repo, launcher, clockAt(1000))

	ctx, cancel := context.WithCancel(tradeCtx(&tradeCaptureLogger{}))
	cancel() // already cancelled before Handle runs

	_, err := h.Handle(ctx, tradeCmd())
	require.ErrorIs(t, err, context.Canceled)
}

func TestTradeHandle_WrongRequestType(t *testing.T) {
	h := newTradeHandler(&fakeTradeShipRepo{}, &fakeTourLauncher{}, clockAt(0))
	_, err := h.Handle(tradeCtx(&tradeCaptureLogger{}), &struct{ common.Request }{})
	require.Error(t, err)
}
