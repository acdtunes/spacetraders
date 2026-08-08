package commands

import (
	"context"
	"errors"
	"testing"
)

// --- Terminal no-op on an already-bootstrapped (mature) fleet ---
//
// The bootstrap coordinator is boot-standing, so it is (re)launched on a MATURE fleet at every daemon
// restart. Its own tick-cadence rationale assumes it EXITS once the gate is built ("bootstrap runs ONLY
// during cold start ... it exits at EXPANSION before the fleet is ever large, so a fast tick carries zero
// API-pacing concern"). That assumption is load-bearing: a coordinator that keeps ticking on a large fleet
// pays a fully-paginated fleet re-read every tick, which on a several-hundred-hull fleet is a double-digit
// share of the account-wide (unraisable) request budget.
//
// The terminal signal is the home jump gate reading COMPLETE — bootstrap's own goal, and a POSITIVE
// live-world assertion: every read miss along the gate-snapshot path leaves ConstructionComplete false, so
// absent evidence keeps the arc in a cold-start phase and can never be mistaken for "done".

// matureObs is an already-bootstrapped fleet: the home jump gate reads COMPLETE, the treasury and income
// are steady-state, coverage is full, probes are at target. This is what a mid-era daemon restart observes.
func matureObs() Observation {
	return Observation{
		HomeSystem: "X1-HQ", MarketsTotal: 20, MarketsCovered: 20, Treasury: 248_000_000,
		IncomePerHour:        9_800_000,
		ProbeCount:           12,
		ProbesScouting:       12,
		GateSite:             "X1-HQ-GATE",
		ConstructionStarted:  true,
		ConstructionComplete: true,
		ConstructionPercent:  100,
		ManufacturingRunning: true,
		ManufacturingAdopted: true,
		HasIdlePurchaser:     true,
		Readable:             true,
	}
}

// coldStartSpies are the cold-start collaborators wired into every no-op test so that any cold-start
// action would be RECORDED rather than silently skipped for want of a port — the difference between
// "took no action" and "could not have acted".
type coldStartSpies struct {
	probes    *fakeAcquirer
	haulers   *fakeHaulerAcquirer
	gateWork  *fakeGateAcquirer
	construct *fakeConstruction
	contracts *fakeContractRunner
	frigate   *fakeFrigateLoop
	posts     *fakeDeclarer
	refresher *fakeRefresher
}

// spiedHandler wires a handler over the given observation with every cold-start port spied.
func spiedHandler(obs Observation, ho HandoffLauncher) (*RunBootstrapCoordinatorHandler, *coldStartSpies) {
	s := &coldStartSpies{
		probes:    &fakeAcquirer{price: 1000, yard: "X1-HQ-YARD", readable: true},
		haulers:   &fakeHaulerAcquirer{price: 1000, yard: "X1-HQ-YARD", readable: true},
		gateWork:  &fakeGateAcquirer{price: 1000, yard: "X1-HQ-YARD", readable: true},
		construct: &fakeConstruction{},
		contracts: &fakeContractRunner{},
		frigate:   &fakeFrigateLoop{},
		posts:     &fakeDeclarer{},
		refresher: &fakeRefresher{},
	}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(s.refresher)
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(s.probes)
	h.SetHaulerAcquirer(s.haulers)
	h.SetGateWorkerAcquirer(s.gateWork)
	h.SetConstructionManager(s.construct)
	h.SetContractRunner(s.contracts)
	h.SetFrigateContractLoopStarter(s.frigate)
	h.SetScoutPostDeclarer(s.posts)
	h.SetManufacturingController(&fakeManufacturing{})
	h.SetWorkerRepurposer(&fakeRepurposer{})
	if ho != nil {
		h.SetHandoffLauncher(ho)
	}
	return h, s
}

// assertNoColdStartAction fails when any cold-start spend or operation-start fired.
func (s *coldStartSpies) assertNoColdStartAction(t *testing.T) {
	t.Helper()
	if s.probes.buys != 0 {
		t.Fatalf("a mature fleet must buy no probes, got %d", s.probes.buys)
	}
	if s.haulers.buys != 0 || s.haulers.dedicateBuys != 0 {
		t.Fatalf("a mature fleet must buy no haulers, got place=%d dedicate=%d", s.haulers.buys, s.haulers.dedicateBuys)
	}
	if s.gateWork.buys != 0 {
		t.Fatalf("a mature fleet must buy no gate workers, got %d", s.gateWork.buys)
	}
	if s.construct.starts != 0 {
		t.Fatalf("a mature fleet must start no construction pipeline, got %d (sites %v)", s.construct.starts, s.construct.sites)
	}
	if s.contracts.calls != 0 {
		t.Fatalf("a mature fleet must start no batch-contract operation, got %d", s.contracts.calls)
	}
	if s.frigate.calls != 0 {
		t.Fatalf("a mature fleet must start no frigate contract earner loop, got %d", s.frigate.calls)
	}
}

// runToExit drives reconcileOnce exactly the way the Handle loop does — stopping the moment the
// reconciler signals Done — and reports how many ticks ran. maxTicks caps a non-terminating reconciler
// so the test FAILS instead of hanging.
func runToExit(t *testing.T, h *RunBootstrapCoordinatorHandler, cmd *RunBootstrapCoordinatorCommand, maxTicks int) (int, reconcileResult) {
	t.Helper()
	var res reconcileResult
	for tick := 1; tick <= maxTicks; tick++ {
		r, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		res = r
		if r.Done {
			return tick, r
		}
	}
	return maxTicks, res
}

// flakyHandoff fails its autosizer launch for the first failFirst calls, then succeeds — a TRANSIENT
// launcher fault, as opposed to fakeHandoff{standErr} which models a persistent one.
type flakyHandoff struct {
	failFirst int
	standing  int
}

func (f *flakyHandoff) LaunchStandingCoordinators(ctx context.Context, playerID int, agentSymbol string) error {
	f.standing++
	if f.standing <= f.failFirst {
		return errors.New("transient launcher fault")
	}
	return nil
}

func (f *flakyHandoff) LaunchContractScaler(ctx context.Context, playerID int, agentSymbol string) error {
	return nil
}

func (f *flakyHandoff) LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error {
	return nil
}

// THE LIVE BUG: a mature fleet whose hand-off can never be confirmed (the autosizer launch fails every
// tick) held the coordinator in EXPANSION forever — a full paginated fleet re-read every tick, against a
// saturated account-wide request budget, on a world where bootstrap has nothing left to do. A built gate
// is terminal on the WORLD signal; an unconfirmable hand-off is retried at the next daemon boot (bootstrap
// is boot-standing and every launch is idempotent), not every tick until the era ends.
func TestBootstrap_MatureFleet_ExitsWhenHandoffNeverConfirms(t *testing.T) {
	ho := &fakeHandoff{standErr: errors.New("growth launcher down")}
	h, spies := spiedHandler(matureObs(), ho)

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done {
		t.Fatalf("a mature fleet must become terminal even when the hand-off never confirms — ran %d ticks without Done (the live full-fleet re-read loop)", ticks)
	}
	if ticks > 10 {
		t.Fatalf("the unconfirmable hand-off must be retried a BOUNDED number of ticks, took %d", ticks)
	}
	if spies.refresher.calls != ticks {
		t.Fatalf("expected one fleet re-read per tick, got %d over %d ticks", spies.refresher.calls, ticks)
	}
	spies.assertNoColdStartAction(t)
}

// The mature no-op must also hold for the other unconfirmable-hand-off shape: no launcher wired at
// all, which held the loop open forever.
func TestBootstrap_MatureFleet_ExitsWhenHandoffCannotRun(t *testing.T) {
	h, spies := spiedHandler(matureObs(), nil)
	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done {
		t.Fatalf("a mature fleet with no hand-off launcher must still become terminal, ran %d ticks", ticks)
	}
	spies.assertNoColdStartAction(t)
}

// A hand-off that confirms on the first tick exits on that tick with the hand-off recorded — the healthy
// path, and the whole API cost of a mature-fleet restart: ONE fleet re-read.
func TestBootstrap_MatureFleet_ConfirmedHandoffExitsOnFirstTick(t *testing.T) {
	ho := &fakeHandoff{}
	h, spies := spiedHandler(matureObs(), ho)

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if ticks != 1 || !res.Done || !res.HandoffLaunched {
		t.Fatalf("a confirmed hand-off must exit on tick 1 with HandoffLaunched, got ticks=%d Done=%v handoff=%v", ticks, res.Done, res.HandoffLaunched)
	}
	if spies.refresher.calls != 1 {
		t.Fatalf("a mature-fleet restart must cost exactly ONE fleet re-read, got %d", spies.refresher.calls)
	}
	spies.assertNoColdStartAction(t)
}

// A TRANSIENT hand-off fault must still exit with a CONFIRMED hand-off — the retry window is what makes
// the bounded exit safe for a fresh fleet that has just finished its gate, so it must not be shortcut.
func TestBootstrap_MatureFleet_TransientHandoffFaultStillConfirms(t *testing.T) {
	ho := &flakyHandoff{failFirst: 1}
	h, _ := spiedHandler(matureObs(), ho)

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done {
		t.Fatalf("a transient hand-off fault must still reach terminal, ran %d ticks", ticks)
	}
	if !res.HandoffLaunched {
		t.Fatalf("a transient fault must exit with a CONFIRMED hand-off (the retry must not be shortcut), got handoff=%v after %d ticks", res.HandoffLaunched, ticks)
	}
	if ho.standing == 0 {
		t.Fatalf("the standing coordinators must have been launched once the autosizer launch recovered")
	}
}

// --- The direction that matters more: a FRESH fleet must be untouched ---

// freshDataObs is a cold agent: nothing scanned, no probes, no gate anywhere in sight. Every terminal
// signal is at its absent value.
func freshDataObs() Observation {
	return Observation{
		HomeSystem: "X1-HQ", MarketsTotal: 10, MarketsCovered: 0, Treasury: 500_000,
		HasIdlePurchaser: true,
		Readable:         true,
	}
}

// A fresh fleet must NEVER go terminal, must keep re-reading the fleet every tick (the phantom-cache
// guard is load-bearing while roles are being assigned), and must keep running cold start. Era N+1
// depends on this being byte-identical.
func TestBootstrap_FreshFleet_NeverTerminal_ColdStartUnaffected(t *testing.T) {
	h, spies := spiedHandler(freshDataObs(), &fakeHandoff{})

	const ticks = 20
	for tick := 1; tick <= ticks; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: a fresh fleet must NEVER be terminal — cold start would be skipped entirely", tick)
		}
		if res.Phase != PhaseColdStart {
			t.Fatalf("tick %d: a fresh fleet must stay in COLDSTART, got %s", tick, res.Phase)
		}
	}
	if spies.refresher.calls != ticks {
		t.Fatalf("a fresh fleet must re-read the fleet every tick (phantom-cache guard), got %d over %d ticks", spies.refresher.calls, ticks)
	}
	if spies.probes.buys == 0 {
		t.Fatalf("a fresh fleet must still buy probes")
	}
	if spies.posts.calls == 0 {
		t.Fatalf("a fresh fleet must still declare the home scout post")
	}
}

// A fleet mid-gate-construction (pipeline started, gate NOT complete) must never go terminal either —
// the terminal signal is the gate being BUILT, never merely being worked on.
func TestBootstrap_GateUnderConstruction_NeverTerminal(t *testing.T) {
	obs := gateObs() // ConstructionStarted=true, ConstructionComplete=false
	obs.ConstructionPercent = 99
	h, _ := spiedHandler(obs, &fakeHandoff{})

	for tick := 1; tick <= 20; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: a gate at %.0f%% is NOT built — bootstrap must not go terminal", tick, obs.ConstructionPercent)
		}
	}
}

// --- Absent / unreadable evidence must fail toward RUNNING cold start ---

// ADVERSARIAL: the observation reports the terminal value (ConstructionComplete=true) alongside its
// unreadable flag. A terminal check that reads the signal without first honouring Readable would go
// terminal on a world it could not read — the exact overload (one field meaning both "done" and "no
// evidence") that produced this bug. Absent evidence must fail toward cold start, never toward skipping it.
func TestBootstrap_UnreadableWorld_NeverTerminal_EvenWhenSignalReadsComplete(t *testing.T) {
	obs := matureObs()
	obs.Readable = false
	obs.Reason = "fleet read failed"
	h, spies := spiedHandler(obs, &fakeHandoff{})

	for tick := 1; tick <= 20; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: an UNREADABLE world must never be treated as terminal, whatever the stale signal says", tick)
		}
		if res.Blocker != "world_unreadable" {
			t.Fatalf("tick %d: expected the world_unreadable blocker, got %q", tick, res.Blocker)
		}
	}
	spies.assertNoColdStartAction(t)
}

// ADVERSARIAL: a refresher fault (the fleet could not be re-read) reports the terminal value in the
// observation behind it. The tick must fail closed BEFORE observing, so the terminal signal is never
// consulted on a fleet whose state could not be refreshed.
func TestBootstrap_RefreshFailure_NeverTerminal_EvenWhenSignalReadsComplete(t *testing.T) {
	h, spies := spiedHandler(matureObs(), &fakeHandoff{})
	spies.refresher.err = errors.New("fleet refresh failed")

	for tick := 1; tick <= 20; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: a failed fleet refresh must fail the tick CLOSED, never terminal", tick)
		}
	}
	spies.assertNoColdStartAction(t)
}

// The ORIGINAL absent-evidence shape: the gate snapshot came back empty (no site resolved, nothing
// complete). That is "no evidence", NOT "done" — the arc must stay in a cold-start phase and keep
// working. This is the distinction the terminal signal exists to preserve.
func TestBootstrap_GateSnapshotMiss_StaysInColdStart(t *testing.T) {
	obs := matureObs()
	obs.GateSite = "" // nothing resolved
	obs.ConstructionStarted = false
	obs.ConstructionComplete = false // every miss on the snapshot path leaves this false
	obs.ConstructionPercent = 0
	h, _ := spiedHandler(obs, &fakeHandoff{})

	for tick := 1; tick <= 20; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: an unresolved gate site is ABSENT EVIDENCE, not a built gate — bootstrap must keep running cold start", tick)
		}
		if res.Phase == PhaseExpansion {
			t.Fatalf("tick %d: an unresolved gate site must never derive the terminal phase, got %s", tick, res.Phase)
		}
	}
}
