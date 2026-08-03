package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-m3122 liveness-watchdog fakes --------------------------------------

// fakeTourLiveness is the driven port that reports each running tour container's last
// real-progress time (plan/navigate/arrive/buy/sell). A container absent from `progress`
// is reported as unknown (not in the returned map) — the watchdog must then leave it
// alone (fail-closed), never kill a tour whose progress it could not read.
type fakeTourLiveness struct {
	progress  map[string]time.Time
	err       error
	callCount int
	lastAsked []string
}

func (f *fakeTourLiveness) LastTourProgress(_ context.Context, _ shared.PlayerID, containerIDs []string) (map[string]time.Time, error) {
	f.callCount++
	f.lastAsked = containerIDs
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]time.Time, len(containerIDs))
	for _, id := range containerIDs {
		if ts, ok := f.progress[id]; ok {
			out[id] = ts
		}
	}
	return out, nil
}

// fakeTourStopper records every kill and can be told to fail for a specific container
// (a kill that fails must NOT be followed by a relaunch — the old container may still
// hold the hull).
type fakeTourStopper struct {
	stopped []string
	failFor map[string]error
}

func (f *fakeTourStopper) StopTour(_ context.Context, containerID, _ string) error {
	if f.failFor != nil {
		if err, ok := f.failFor[containerID]; ok {
			return err
		}
	}
	f.stopped = append(f.stopped, containerID)
	return nil
}

// fakeAbsorptionReclaimer records the dead-container absorption sweep the coordinator
// runs on restart / after a kill (sp-m3122 part 3).
type fakeAbsorptionReclaimer struct {
	calls     int
	playerIDs []shared.PlayerID
	count     int
	err       error
}

func (f *fakeAbsorptionReclaimer) ReclaimDeadContainerAbsorption(_ context.Context, playerID shared.PlayerID) (int, error) {
	f.calls++
	f.playerIDs = append(f.playerIDs, playerID)
	return f.count, f.err
}

// ---- ship builders ---------------------------------------------------------

// inTransitRunningTradeHull models a trade hull mid-tour AND mid-flight: a live container
// claim with NavStatusInTransit. This is the legitimately-long-leg case (multi-hop travel
// / jump) the watchdog must NEVER kill even when its last logged progress looks old.
func inTransitRunningTradeHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-TR-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusInTransit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(tradeFleet)
	require.NoError(t, ship.AssignToContainer("tour-live-"+symbol, clockAt(0)))
	return ship
}

// ---- watchdog harness ------------------------------------------------------

// watchdogCmd arms a reconcile at the default 12-min stall threshold (the watchdog is
// always-on; the threshold is the only knob).
func watchdogCmd() *RunTradeFleetCoordinatorCommand {
	return &RunTradeFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "trade-coord-1",
		AgentSymbol: "TORWIND",
		Enabled:     true,
	}
}

// container ID a runningTradeHull / inTransitRunningTradeHull carries.
func liveContainer(symbol string) string { return "tour-live-" + symbol }

// nowAfterBase returns a clock N seconds past baseTime.
func nowAfterBase(secs int) *shared.MockClock { return clockAt(secs) }

// ---- sp-m3122 watchdog tests -----------------------------------------------

// (B1) A RUNNING trade tour that has made ZERO progress for longer than the stall
// threshold is HUNG: the coordinator kills its container and relaunches a fresh tour —
// exactly the manual `container stop <hung-tour>` remedy, automated.
func TestTradeWatchdog_HungRunningTour_KilledAndRelaunchedFresh(t *testing.T) {
	now := nowAfterBase(1000) // 1000s past baseTime
	hung := runningTradeHull(t, "TORWIND-57")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{
		liveContainer("TORWIND-57"): baseTime, // last progress 1000s ago >> 720s threshold
	}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.Equal(t, []string{liveContainer("TORWIND-57")}, stopper.stopped, "the hung tour container must be killed")
	require.Equal(t, []string{"TORWIND-57"}, launcher.launchedSymbols(), "a fresh tour must be relaunched for the hung hull")
	require.Equal(t, 1, launched)
	require.True(t, logger.loggedContaining("no progress", "TORWIND-57"), "the kill must be logged with the honest reason")
}

// (B2) A RUNNING tour that is making progress (recent activity, well within the stall
// threshold) is healthy — never killed, never relaunched.
func TestTradeWatchdog_HealthyProgressingTour_LeftAlone(t *testing.T) {
	now := nowAfterBase(1000)
	healthy := runningTradeHull(t, "TORWIND-55")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{healthy}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{
		liveContainer("TORWIND-55"): baseTime.Add(940 * time.Second), // 60s ago << 720s
	}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.Empty(t, stopper.stopped, "a progressing tour must never be killed")
	require.Empty(t, launcher.launches)
	require.Equal(t, 0, launched)
}

// (B3) A RUNNING tour on a legitimately-long leg (IN_TRANSIT: multi-hop travel / jump)
// is NEVER killed, even when its last logged progress predates the threshold — the
// watchdog keys on PROGRESS (a flying hull is progressing), not wall-clock silence.
func TestTradeWatchdog_InTransitLongLeg_NotKilled(t *testing.T) {
	now := nowAfterBase(1000)
	flying := inTransitRunningTradeHull(t, "TORWIND-59")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{flying}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{
		liveContainer("TORWIND-59"): baseTime, // 1000s of "silence" — but it is flying
	}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.Empty(t, stopper.stopped, "a hull mid-flight must never be killed")
	require.Empty(t, launcher.launches)
	require.Equal(t, 0, launched)
}

// (B4) Fail-closed: the watchdog kills NOTHING when it cannot confirm a tour is stalled —
// no liveness port, no stopper, a liveness read error, or a container whose progress is
// unknown (absent from the map). It must never kill blindly.
func TestTradeWatchdog_FailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		wireLive   bool
		wireStop   bool
		liveErr    error
		progress   map[string]time.Time
		wantLogSub string
	}{
		{name: "no liveness port wired", wireLive: false, wireStop: true},
		{name: "no stopper wired", wireLive: true, wireStop: false, progress: map[string]time.Time{liveContainer("TORWIND-57"): baseTime}},
		{name: "liveness read error", wireLive: true, wireStop: true, liveErr: errors.New("db down"), wantLogSub: "could not read tour progress"},
		{name: "unknown progress (not in map)", wireLive: true, wireStop: true, progress: map[string]time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := nowAfterBase(1000)
			hung := runningTradeHull(t, "TORWIND-57")
			repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
			stopper := &fakeTourStopper{}
			launcher := &fakeTourLauncher{}
			logger := &tradeCaptureLogger{}

			h := NewRunTradeFleetCoordinatorHandler(repo, now)
			h.SetTourLauncher(launcher)
			if tc.wireLive {
				h.SetTourLiveness(&fakeTourLiveness{progress: tc.progress, err: tc.liveErr})
			}
			if tc.wireStop {
				h.SetTourStopper(stopper)
			}

			launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

			require.NoError(t, err)
			require.Empty(t, stopper.stopped, "fail-closed: nothing killed")
			require.Empty(t, launcher.launches, "fail-closed: nothing relaunched")
			require.Equal(t, 0, launched)
			if tc.wantLogSub != "" {
				require.True(t, logger.loggedContaining(tc.wantLogSub))
			}
		})
	}
}

// (B1 edge) A kill that FAILS must NOT be followed by a relaunch — the doomed container may
// still hold the hull, and a relaunch would be refused (or double-claim). Retry next tick.
func TestTradeWatchdog_KillFails_NoRelaunch(t *testing.T) {
	now := nowAfterBase(1000)
	hung := runningTradeHull(t, "TORWIND-57")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{liveContainer("TORWIND-57"): baseTime}}
	stopper := &fakeTourStopper{failFor: map[string]error{liveContainer("TORWIND-57"): errors.New("stop failed")}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.Empty(t, launcher.launches, "a hull whose hung container could not be killed must not be relaunched")
	require.Equal(t, 0, launched)
	require.True(t, logger.loggedContaining("failed to kill", "TORWIND-57"))
}

// (B1 acceptance b) A simulated daemon restart mid-tour: several RUNNING tours are hung
// (silent since before the restart), one is healthy, one is mid-flight. After ONE reconcile
// pass, EVERY hung hull has a fresh tour and NO RUNNING-but-hung container remains; the
// healthy and flying tours are untouched.
func TestTradeWatchdog_SimulatedRestart_HungKilledHealthyKept(t *testing.T) {
	now := nowAfterBase(2000)
	hung57 := runningTradeHull(t, "TORWIND-57")
	hung59 := runningTradeHull(t, "TORWIND-59")
	healthy55 := runningTradeHull(t, "TORWIND-55")
	flying56 := inTransitRunningTradeHull(t, "TORWIND-56")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung57, hung59, healthy55, flying56}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{
		liveContainer("TORWIND-57"): baseTime,                         // silent since before restart -> hung
		liveContainer("TORWIND-59"): baseTime.Add(100 * time.Second),  // still >> threshold -> hung
		liveContainer("TORWIND-55"): baseTime.Add(1950 * time.Second), // 50s ago -> healthy
		liveContainer("TORWIND-56"): baseTime,                         // stale, but IN_TRANSIT -> not hung
	}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.ElementsMatch(t, []string{liveContainer("TORWIND-57"), liveContainer("TORWIND-59")}, stopper.stopped,
		"exactly the two hung tours are killed")
	require.ElementsMatch(t, []string{"TORWIND-57", "TORWIND-59"}, launcher.launchedSymbols(),
		"exactly the two hung hulls get a fresh tour within one reconcile interval")
	require.Equal(t, 2, launched)
}

// ---- sp-m3122 part 3: dead-container absorption reclaim --------------------

// (B5) On restart (the coordinator's first reconcile pass) the coordinator promptly releases
// absorption-ledger reservations held by containers that no longer exist, rather than waiting
// for the TTL sweep, so phantom reservations never make open sinks look contended.
func TestTradeWatchdog_FirstPass_ReclaimsDeadContainerAbsorption(t *testing.T) {
	now := nowAfterBase(10)
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{}} // no hulls needed
	reclaimer := &fakeAbsorptionReclaimer{count: 3}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetAbsorptionReclaimer(reclaimer)

	_, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())
	require.NoError(t, err)

	require.Equal(t, 1, reclaimer.calls, "the restart reclaim runs once on the first pass")
	require.Equal(t, []shared.PlayerID{shared.MustNewPlayerID(1)}, reclaimer.playerIDs)

	// A steady second pass with no kills does NOT sweep again (the restart cleanup is one-shot).
	_, err = h.reconcileOnce(tradeCtx(logger), watchdogCmd())
	require.NoError(t, err)
	require.Equal(t, 1, reclaimer.calls, "no needless per-tick sweep once the restart cleanup is done")
}

// (B5) After the watchdog kills a hung tour, the coordinator reclaims that now-dead
// container's absorption reservations promptly (the "release failed ... context canceled"
// observation) — even on a later pass, past the one-shot restart sweep.
func TestTradeWatchdog_AfterKill_ReclaimsDeadContainerAbsorption(t *testing.T) {
	now := nowAfterBase(1000)
	hung := runningTradeHull(t, "TORWIND-57")
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{liveContainer("TORWIND-57"): baseTime}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	reclaimer := &fakeAbsorptionReclaimer{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)
	h.SetAbsorptionReclaimer(reclaimer)
	h.startupReclaimDone = true // simulate a steady (non-first) pass so only the kill can trigger the sweep

	_, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())
	require.NoError(t, err)

	require.Equal(t, []string{liveContainer("TORWIND-57")}, stopper.stopped)
	require.Equal(t, 1, reclaimer.calls, "killing a hung tour triggers a prompt dead-container absorption reclaim")
}

// ---- sp-39hjn: cooldown-aware liveness -------------------------------------

// cooldownRunningTradeHull models a RUNNING (container-claimed) trade hull that has
// finished its jump and is now PARKED (IN_ORBIT) waiting out the game jump cooldown — the
// sp-tp5c3 far-tour case: the jump COMPLETED (so the hull is NOT IN_TRANSIT) but it is
// legitimately idle until the cooldown clears, emitting no tour-progress log meanwhile.
func cooldownRunningTradeHull(t *testing.T, symbol string, cooldownExpiry time.Time) *navigation.Ship {
	t.Helper()
	ship := runningTradeHull(t, symbol)
	ship.SetCooldown(cooldownExpiry)
	return ship
}

// (B6, HEADLINE) A RUNNING trade hull parked (IN_ORBIT) waiting out a STILL-ACTIVE
// jump cooldown is NOT hung — it is legitimately idle until the game timer clears. Even
// though its last tour-progress log predates the stall threshold (a far sp-tp5c3 tour's
// multi-minute cooldown is a silent log gap), the watchdog keys on the hull's OWN cooldown
// state and skips it: no kill, no relaunch. This is the false-kill that orphaned the hull's
// reserved-but-unexecuted sells and thrashed the trade fleet.
func TestTradeWatchdog_ParkedOnActiveCooldown_NotKilled(t *testing.T) {
	now := nowAfterBase(1000) // now = baseTime + 1000s
	// Cooldown expires 600s in the FUTURE — the hull is genuinely mid-cooldown.
	cooling := cooldownRunningTradeHull(t, "TORWIND-61", baseTime.Add(1600*time.Second))
	repo := &fakeTradeShipRepo{ships: []*navigation.Ship{cooling}}
	liveness := &fakeTourLiveness{progress: map[string]time.Time{
		liveContainer("TORWIND-61"): baseTime, // 1000s of "silence" >> 720s threshold — but it is cooling down
	}}
	stopper := &fakeTourStopper{}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetTourLiveness(liveness)
	h.SetTourStopper(stopper)

	launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

	require.NoError(t, err)
	require.Empty(t, stopper.stopped, "a hull waiting out an active jump cooldown must never be killed")
	require.Empty(t, launcher.launches)
	require.Equal(t, 0, launched)
}

// (B7, sp-39hjn REGRESSION GUARD) Cooldown-awareness must NOT blunt genuine hang detection:
// a PARKED hull whose cooldown has already EXPIRED (a stale past timestamp) or that has NO
// cooldown at all, silent past the stall threshold, is STILL hung and STILL killed —
// byte-identical to previous behavior. Only a FUTURE (active) cooldown protects a hull.
func TestTradeWatchdog_ParkedCooldownExpiredOrNone_StillKilled(t *testing.T) {
	cases := []struct {
		name        string
		setCooldown bool
		expiry      time.Time
	}{
		{name: "cooldown already expired (past)", setCooldown: true, expiry: baseTime.Add(500 * time.Second)}, // 500s < now(1000s) -> expired
		{name: "no cooldown at all", setCooldown: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := nowAfterBase(1000)
			hung := runningTradeHull(t, "TORWIND-63")
			if tc.setCooldown {
				hung.SetCooldown(tc.expiry)
			}
			repo := &fakeTradeShipRepo{ships: []*navigation.Ship{hung}}
			liveness := &fakeTourLiveness{progress: map[string]time.Time{
				liveContainer("TORWIND-63"): baseTime, // 1000s silent >> 720s
			}}
			stopper := &fakeTourStopper{}
			launcher := &fakeTourLauncher{}
			logger := &tradeCaptureLogger{}

			h := NewRunTradeFleetCoordinatorHandler(repo, now)
			h.SetTourLauncher(launcher)
			h.SetTourLiveness(liveness)
			h.SetTourStopper(stopper)

			launched, err := h.reconcileOnce(tradeCtx(logger), watchdogCmd())

			require.NoError(t, err)
			require.Equal(t, []string{liveContainer("TORWIND-63")}, stopper.stopped, "a hull with no ACTIVE cooldown, silent past the threshold, is still hung and must be killed")
			require.Equal(t, []string{"TORWIND-63"}, launcher.launchedSymbols())
			require.Equal(t, 1, launched)
		})
	}
}

// ---- threshold knob --------------------------------------------------------

func TestWatchdogStallThreshold_DefaultAndOverride(t *testing.T) {
	require.Equal(t, time.Duration(defaultWatchdogStallSeconds)*time.Second, (&RunTradeFleetCoordinatorCommand{}).watchdogStallThreshold())
	require.Equal(t, 15*time.Minute, (&RunTradeFleetCoordinatorCommand{WatchdogStallSecs: 900}).watchdogStallThreshold())
}
