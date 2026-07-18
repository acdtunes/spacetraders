package commands

import (
	"context"
	"errors"
	"fmt"
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

// parkedTradeHullGap models a trade hull whose prior tour ran for EXACTLY
// (releaseSecs - assignSecs) seconds before an honest exit (sp-1pli). Unlike
// parkedTradeHull (a fixed 300s gap, always well above minProductiveTourDuration), this
// lets a test control whether the exit lands on the productive or unproductive side of
// the adaptive-backoff fast-fail line.
func parkedTradeHullGap(t *testing.T, symbol string, assignSecs, releaseSecs int, exitReason string) *navigation.Ship {
	t.Helper()
	ship := tradeHull(t, symbol)
	require.NoError(t, ship.AssignToContainer("tour-prev-"+symbol, clockAt(assignSecs)))
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

// ---- sp-1pli: adaptive per-hull relaunch backoff ---------------------------
//
// The trade fleet coordinator relaunches every idle hull on a flat per-hull cooldown
// (180s default) forever, even when EVERY relaunch immediately exits margins-death
// (fleet-wide infeasibility) — burning a full discovery+solver pass, and the API/log
// volume that comes with it, every single cooldown window. These tests drive that
// escalation through reconcileOnce exactly like the tests above, distinguishing
// "would launch under the OLD flat cooldown" from "held under the NEW escalated one"
// by picking `now` to land strictly between the two cooldown values — the same
// discriminating-boundary style TestTradeReconcile_CooldownSurvivesCoordinatorRestart
// already uses for the base cooldown.
//
// The productivity signal is a fast-fail duration heuristic (minProductiveTourDuration
// = 90s): a tour that ran assignedAt->releasedAt for at least 90s flew a plausible
// trade leg (productive); shorter is treated as an immediate margins-death fast-fail
// (unproductive). Every fixture above (parkedTradeHull's fixed 300s gap) sits well
// above this line, so all 13 pre-existing tests are unaffected by construction.

// A single unproductive exit (20s, well under the 90s line) DOUBLES the hull's
// cooldown from the base 180s to 360s on the very pass that scores it. At now=300s
// (280s since the 20s release) the OLD flat 180s cooldown would already have cleared
// (280 >= 180), but the freshly-escalated 360s has not (280 < 360) — so the hull is
// held, proving the escalation actually changed the relaunch decision, not just an
// internal counter. The single INFO escalation line is asserted verbatim.
func TestTradeFleetBackoff_UnproductiveExit_EscalatesCooldown(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-90", 0, 20, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(300))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched, "280s clears the old flat 180s cooldown but not the escalated 360s one")
	require.Empty(t, launcher.launches)
	require.True(t, logger.loggedContaining(ship.ShipSymbol(), "cooldown escalating to 6m0s after 1 consecutive"))
}

// Calling reconcileOnce again against the SAME unchanged parked hull (no new tour
// cycle in between — exactly what happens every tick while a hull just sits out its
// cooldown) must NOT rescore the identical exit a second time. Without the
// scoredRelease guard, a single unproductive tour would runaway-escalate toward the
// max within a couple of ticks instead of once per real tour cycle — violating the
// bead's explicit "no per-tick spam" requirement.
func TestTradeFleetBackoff_SameExitScoredOnce_NoDoubleEscalationAcrossTicks(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-91", 0, 20, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()

	// Tick 1: scores the exit (unproductive) -> 180s escalates to 360s. 980s clears it.
	launched1, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched1)

	// Tick 2: same handler (backoff state persists), same unchanged ship (the fake
	// launcher never mutates ship state, so it still reads as the identical 20s exit).
	launched2, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched2, "still clears the (unchanged) 360s cooldown, but must not rescore")

	escalations := 0
	for _, m := range logger.messages {
		if strings.Contains(m, "cooldown escalating") {
			escalations++
		}
	}
	require.Equal(t, 1, escalations, "the same tour exit must be scored exactly once, not once per tick")
}

// sp-nxrt part (a): the 2nd CONSECUTIVE fast-fail escalates to MOVEMENT, not a longer
// sleep. The 1st fast-fail still doubles the sleep (the market here may just be thin —
// the lxwn rich->tapped->rich cycle, so one wait-in-place cycle is right). But when the
// hull fast-fails AGAIN, waiting-in-place did not help: the lane is gone from HERE, so
// the coordinator arms reposition-reach on the relaunch and drops the sleep back to the
// base breather so the hull MOVES promptly instead of idling another (720s) escalation
// step. The discriminating boundary is picked so the OLD behavior (720s) would HOLD the
// hull while the NEW behavior launches it: 300s elapsed clears the 180s base but not a
// 720s doubled sleep. The launched spec carries the reach-armed flag; cycle-1's does not.
func TestTradeFleetBackoff_SecondConsecutive_EscalatesToMovementNotSleep(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-92", 0, 20, "margins_died_both_systems") // 1st: 20s, unproductive
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()

	// Pass 1 (streak 1): 180s -> 360s sleep escalation. 1000-20=980s >= 360s -> launches,
	// reach NOT armed (a first fast-fail waits in place, it does not move).
	launched1, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched1)
	require.False(t, launcher.launches[0].RepositionReachEscalated, "the first fast-fail waits in place — reach is not armed")

	// 2nd tour cycle: another short, unproductive exit.
	require.NoError(t, ship.AssignToContainer("tour-2-"+ship.ShipSymbol(), clockAt(1000)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1015)) // 15s, unproductive

	clock.CurrentTime = baseTime.Add(1315 * time.Second) // 1315-1015=300s: >=180s base, <720s the old doubling
	launched2, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched2, "escalate-to-MOVEMENT relaunches at the base breather (300s clears 180s); the old 720s doubling would still HOLD it")
	require.True(t, launcher.launches[1].RepositionReachEscalated, "the 2nd consecutive fast-fail arms reposition-reach on the relaunch")
	require.True(t, logger.loggedContaining(ship.ShipSymbol(), "escalating to MOVEMENT", "reposition-reach"),
		"the movement escalation is logged distinctly from a sleep escalation")
	require.False(t, logger.loggedContaining("cooldown escalating to 12m0s"),
		"the 2nd fast-fail must NOT double the sleep to 720s — it moves instead")
}

// A PRODUCTIVE exit (>= 90s) resets a previously-escalated hull straight back to base,
// not merely to "one step down". After one prior unproductive cycle escalates 180s ->
// 360s, a second cycle running 150s (comfortably >= the 90s line) must drop the
// cooldown all the way back to 180s. now is picked to land strictly between the two:
// 200s elapsed clears a correctly-reset 180s base but would still be held under a
// wrongly-retained 360s — a discriminating boundary, same style as the escalation test
// above. No escalation line is logged for a productive (reset) exit.
func TestTradeFleetBackoff_ProductiveExit_ResetsToBase(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-93", 0, 20, "margins_died_both_systems") // 1st: 20s, unproductive
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()

	// Pass 1: 180s -> 360s escalation; 980s clears it. Own logger — this pass is
	// EXPECTED to log an escalation, so it must not leak into pass 2's assertion.
	logger1 := &tradeCaptureLogger{}
	launched1, err := h.reconcileOnce(tradeCtx(logger1), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched1)

	// 2nd tour cycle: a real trade leg this time (150s >= 90s line) -> productive.
	require.NoError(t, ship.AssignToContainer("tour-2-"+ship.ShipSymbol(), clockAt(1000)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1150)) // 150s, productive

	clock.CurrentTime = baseTime.Add(1350 * time.Second) // 1350-1150=200s: >=180s base, <360s stale escalated
	logger2 := &tradeCaptureLogger{}
	launched2, err := h.reconcileOnce(tradeCtx(logger2), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched2, "a productive exit must reset to the 180s base, not remain at the stale 360s")
	require.False(t, logger2.loggedContaining("cooldown escalating"), "a productive (reset) exit logs no escalation")
}

// The backoff ceiling is a config value (RelaunchBackoffMaxSecs, RULINGS #5), not a
// hardcoded cap. Post-sp-nxrt the clamp lives in the RESUMED backoff (streak >= 3): the
// 2nd fast-fail escalates to movement (reach) at the base breather rather than doubling,
// so the sleep only resumes climbing once the reach-armed relaunch ALSO fast-failed
// (genuine map-wide exhaustion). This drives four consecutive fast-fails with the ceiling
// configured to 400s: streak1 180->360 (under ceiling); streak2 -> movement, sleep back
// to 180; streak3 resumes 180->360; streak4 would double to 720 but must CLAMP to 400.
// now on the 4th pass is picked between the two (500s elapsed): clears a correctly-clamped
// 400s but would still be held under an unclamped 720s.
func TestTradeFleetBackoff_MaxClamp_HonorsConfiguredCeiling(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-94", 0, 20, "margins_died_both_systems") // 1st: 20s, unproductive
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()
	cmd.RelaunchBackoffMaxSecs = 400

	// Pass 1 (streak 1): 180s -> 360s (under the 400s ceiling). 980s clears it.
	launched1, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched1)

	// Cycle 2 (streak 2): escalate-to-movement — sleep drops back to the 180s base breather.
	require.NoError(t, ship.AssignToContainer("tour-2-"+ship.ShipSymbol(), clockAt(1000)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1015)) // 15s, unproductive
	clock.CurrentTime = baseTime.Add(1215 * time.Second)          // 1215-1015=200s >= 180s base -> launches (reach armed)
	launched2, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched2, "the 2nd fast-fail moves at the base breather, it does not lengthen the sleep")

	// Cycle 3 (streak 3): the reach-armed relaunch ALSO fast-failed -> resume backoff 180->360.
	require.NoError(t, ship.AssignToContainer("tour-3-"+ship.ShipSymbol(), clockAt(1215)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1230)) // 15s, unproductive
	clock.CurrentTime = baseTime.Add(1630 * time.Second)          // 1630-1230=400s >= 360s -> launches
	launched3, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched3, "streak 3 resumes the sleep escalation at 360s")

	// Cycle 4 (streak 4): would double 360->720, must CLAMP to the configured 400s.
	require.NoError(t, ship.AssignToContainer("tour-4-"+ship.ShipSymbol(), clockAt(1630)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1645)) // 15s, unproductive
	clock.CurrentTime = baseTime.Add(2145 * time.Second)          // 2145-1645=500s: >=400s clamp, <720s unclamped
	launched4, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched4, "500s clears the clamped 400s ceiling but would not clear an unclamped 720s")
	require.True(t, logger.loggedContaining(ship.ShipSymbol(), "cooldown escalating to 6m40s after 4 consecutive"),
		"6m40s = 400s, the configured ceiling — not 12m0s (720s), what an unclamped doubling would produce")
}

// sp-nxrt part (a): after the movement escalation, a hull whose reach-armed relaunch
// ALSO fast-fails (streak >= 3) means even the broadened 2-4-hop reach found no ground
// worth the jump — genuine map-wide margin exhaustion. The coordinator RESUMES the
// bounded sleep backoff (so a dead map is not hammered with a discovery+solver pass every
// base cooldown) while KEEPING reach armed (the honest response the instant a ground
// reopens). This walks streak1->2->3 and asserts the streak-3 relaunch both resumes the
// sleep (360s, shown by the escalation log value) AND still carries the reach flag.
func TestTradeFleetBackoff_ThirdConsecutive_ResumesBoundedBackoffReachStaysArmed(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-95", 0, 20, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()

	// streak 1: 180 -> 360; 980s clears it.
	launched1, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched1)

	// streak 2: movement escalation, sleep back to 180s base.
	require.NoError(t, ship.AssignToContainer("tour-2-"+ship.ShipSymbol(), clockAt(1000)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1015))
	clock.CurrentTime = baseTime.Add(1215 * time.Second) // 200s >= 180 base
	launched2, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched2)
	require.True(t, launcher.launches[1].RepositionReachEscalated)

	// streak 3: the reach-armed relaunch fast-failed too -> resume backoff 180 -> 360,
	// reach STAYS armed. 400s clears the resumed 360s so the relaunch spec is observable.
	require.NoError(t, ship.AssignToContainer("tour-3-"+ship.ShipSymbol(), clockAt(1215)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1230))
	clock.CurrentTime = baseTime.Add(1630 * time.Second) // 400s >= 360 resumed
	launched3, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 1, launched3)
	require.True(t, launcher.launches[2].RepositionReachEscalated,
		"reach stays armed while the coordinator backs off a genuinely map-wide-dead neighbourhood")
	require.True(t, logger.loggedContaining(ship.ShipSymbol(), "cooldown escalating to 6m0s after 3 consecutive"),
		"streak 3 resumes the sleep escalation at 360s (6m0s), bounded by the ceiling")
}

// sp-nxrt part (a): a PRODUCTIVE tour (the hull found a ground and traded) resets the
// streak AND disarms the reach escalation — a hull that recovered must relaunch normally,
// not keep force-arming reach forever. After the 2nd fast-fail arms reach, a productive
// exit drops the next relaunch back to an un-escalated, non-reach launch.
func TestTradeFleetBackoff_ProductiveExit_DisarmsReachEscalation(t *testing.T) {
	ship := parkedTradeHullGap(t, "TORWIND-96", 0, 20, "margins_died_both_systems")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{ship}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	clock := clockAt(1000)
	h := newTradeHandler(repo, launcher, clock)
	cmd := tradeCmd()

	// streak 1 then streak 2 (reach armed).
	_, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.NoError(t, ship.AssignToContainer("tour-2-"+ship.ShipSymbol(), clockAt(1000)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1015))
	clock.CurrentTime = baseTime.Add(1215 * time.Second)
	_, err = h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.True(t, launcher.launches[1].RepositionReachEscalated, "precondition: reach armed at streak 2")

	// Productive tour cycle (150s >= 90s line): resets streak and disarms reach.
	require.NoError(t, ship.AssignToContainer("tour-3-"+ship.ShipSymbol(), clockAt(1215)))
	ship.ForceRelease("margins_died_both_systems", clockAt(1365)) // 150s, productive
	clock.CurrentTime = baseTime.Add(1565 * time.Second)          // 200s >= 180 base
	_, err = h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.False(t, launcher.launches[2].RepositionReachEscalated,
		"a productive tour disarms the reach escalation — the recovered hull relaunches normally")
}

// sp-nxrt part (c): the default relaunch-backoff ceiling is 600s (10 min), lowered from
// the old 1800s (30 min) now that a fast-failing hull escalates to MOVEMENT rather than
// ever-longer sleep. A captain [trade_fleet] override still wins (RULINGS #5).
func TestTradeFleetCommand_DefaultBackoffCeiling_Is600s(t *testing.T) {
	require.Equal(t, 600*time.Second, (&RunTradeFleetCoordinatorCommand{}).relaunchBackoffMaxDuration(),
		"the default ceiling is 600s post-sp-nxrt (escalate-to-movement replaces the long sleep)")
	require.Equal(t, 900*time.Second, (&RunTradeFleetCoordinatorCommand{RelaunchBackoffMaxSecs: 900}).relaunchBackoffMaxDuration(),
		"a captain override still takes precedence over the default")
}

// ---- sp-nkci: restart-induced mass-park is non-signal -----------------------
//
// A daemon blip/restart force-parks the whole trade fleet in one narrow window. The
// sp-1pli adaptive backoff, reading each of those short synchronized exits as an
// unproductive fast-fail, would DOUBLE every hull's cooldown at once → the whole fleet
// idles in lockstep (~12min observed). A synchronized mass-park says nothing about
// market depth (organic thin-depth parks a hull at a time, when ITS market dies) — it
// is a restart signature, so it must be exempt from the backoff. These tests pin that a
// mass-park does NOT ramp the fleet, that the exemption has a config kill switch (which
// restores the old ramp so the sp-1pli backoff itself is proven intact), and that a
// SMALL simultaneous park (below the mass-park threshold) still rides the normal
// per-hull backoff — the spread-out single-hull fast-fail sp-1pli exists for is
// untouched.

// massParkFleet builds n trade hulls that each ran a SHORT (20s, unproductive) tour and
// were all released at the SAME instant (releaseSecs) — the daemon-blip mass-park shape.
func massParkFleet(t *testing.T, n, releaseSecs int) []*navigation.Ship {
	t.Helper()
	ships := make([]*navigation.Ship, 0, n)
	for i := 0; i < n; i++ {
		symbol := fmt.Sprintf("TORWIND-%02d", i)
		ships = append(ships, parkedTradeHullGap(t, symbol, releaseSecs-20, releaseSecs, "margins_died_both_systems"))
	}
	return ships
}

// Five hulls force-parked in the same window (each a 20s unproductive exit) are exempt
// from the adaptive backoff: their cooldown stays at BASE, so at 200s elapsed (>= 180s
// base, < 360s the escalation would produce) all five relaunch instead of idling in
// lockstep. No escalation is logged; the mass-park exemption is.
func TestTradeFleetBackoff_MassPark_ExemptFromLockstepRamp(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: massParkFleet(t, 5, 20)}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(220)) // 220-20 = 200s since the synchronized park

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 5, launched, "a synchronized mass-park must NOT ramp the fleet — all relaunch at base cooldown")
	require.False(t, logger.loggedContaining("cooldown escalating"),
		"a restart mass-park is non-signal: it must not feed the thin-depth backoff")
	require.True(t, logger.loggedContaining("mass-park", "exempt"),
		"the exemption is logged so the captain can see why cooldowns did not ramp after a restart")
}

// sp-nxrt part (a) x sp-nkci: a restart mass-park must NOT trigger the movement
// escalation either. The whole fleet fast-failing in one synchronized window is a restart
// signature, not per-hull thin depth — repositioning the entire fleet off a daemon blip
// would be a mass reposition-churn event (the lead's explicit "do not reposition during a
// mass-park"). Because the exemption holds each hull's scoring, consecutiveUnproductive
// never climbs to 2, so reach is never armed: every relaunch spec is a normal (non-reach)
// launch and no movement escalation is logged.
func TestTradeFleetBackoff_MassPark_NoMovementEscalation(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: massParkFleet(t, 5, 20)}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(220)) // 200s since the synchronized park

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 5, launched)
	for _, spec := range launcher.launches {
		require.False(t, spec.RepositionReachEscalated,
			"a mass-parked hull is exempt — the movement escalation must never fire off a restart signature")
	}
	require.False(t, logger.loggedContaining("escalating to MOVEMENT"),
		"no movement escalation during a mass-park")
}

// The exemption is a live-by-default knob with a kill switch (RULINGS #5). Disabling it
// restores the pre-fix behavior — the same mass-park ramps every hull to 360s, so at
// 200s elapsed all five are held in lockstep. This both documents the defect and proves
// the underlying sp-1pli backoff still fires when the exemption is off.
func TestTradeFleetBackoff_MassPark_ExemptDisabled_RampsInLockstep(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: massParkFleet(t, 5, 20)}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	cmd := tradeCmd()
	cmd.MassParkExemptDisabled = true
	h := newTradeHandler(repo, launcher, clockAt(220))

	launched, err := h.reconcileOnce(tradeCtx(logger), cmd)
	require.NoError(t, err)
	require.Equal(t, 0, launched, "with the exemption off the whole fleet ramps to 360s and idles in lockstep at 200s elapsed")
	require.True(t, logger.loggedContaining("cooldown escalating"),
		"the sp-1pli backoff still fires for every hull when the mass-park exemption is disabled")
}

// A park below the mass-park threshold (two hulls) is NOT a restart signature — it still
// rides the normal per-hull sp-1pli backoff. This guards against over-exemption: the
// spread-out single/low-count fast-fail the backoff exists for must be untouched. Both
// hulls ramp to 360s and are held at 200s elapsed.
func TestTradeFleetBackoff_SmallSimultaneousPark_NotExempt_StillRamps(t *testing.T) {
	repo := &fakeTradeShipRepo{ships: massParkFleet(t, 2, 20)}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := newTradeHandler(repo, launcher, clockAt(220))

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched, "a below-threshold park is organic thin-depth, not a mass-park — the backoff still ramps it")
	require.True(t, logger.loggedContaining("cooldown escalating"),
		"the per-hull sp-1pli backoff must remain intact for spread-out / low-count fast-fails")
	require.False(t, logger.loggedContaining("mass-park", "exempt"))
}
