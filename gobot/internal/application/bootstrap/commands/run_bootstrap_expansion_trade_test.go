package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// --- sp-hv4f6: the EXPANSION hand-off redirects the gate's construction hulls to trade ---
//
// The gate workers are among the largest-cargo hulls the fleet owns, and the moment the gate reads
// BUILT they stop earning: gate construction is the SOLE consumer of the "manufacturing" dedication
// (the goods-factory coordinator that also consumed it was retired with the factories, sp-hoj8u), so
// a hull left carrying that tag at EXPANSION polls a finished site forever. The hand-off hands them
// to the trade fleet, which is the one place they earn again.
//
// The redirect is an ASSIGNMENT, not a release: clearing the tag to "" would strand them. Nothing in
// the tree adopts an undedicated hull into trade — the trade coordinator only works hulls ALREADY
// tagged "trade" (partitionTradeFleet skips every other tag), the fleet autosizer only tags hulls it
// BUYS, and the capacity reconciler that once auto-pinned idle hulls was deleted (sp-y2ptq). The one
// remaining adopter of an undedicated hull is the contract scaler's reclaim tier, which takes it into
// CONTRACT and only while it has a ramp deficit. So the redirect names "trade" explicitly.

// tradeReleaseObs is a mature (EXPANSION) observation whose gate workers are still manufacturing-tagged
// — what the observer reads on the tick the gate completes, before anything has re-tagged them.
func tradeReleaseObs(hulls ...GateWorkerSnapshot) Observation {
	obs := matureObs()
	obs.GateWorkers = len(hulls)
	obs.GateWorkerHulls = hulls
	return obs
}

// releasedToTrade wires a spied handler over the observation with the trade-release port attached, and
// returns both so a test can assert exactly which hulls were handed over.
func releasedToTrade(obs Observation, ho HandoffLauncher) (*RunBootstrapCoordinatorHandler, *fakeGateReleaser) {
	h, _ := spiedHandler(obs, ho)
	rel := &fakeGateReleaser{}
	h.SetGateSurplusReleaser(rel)
	return h, rel
}

// THE TRANSITION EDGE: the EXPANSION tick hands every IDLE construction hull to the trade fleet, in
// one call, in deterministic symbol order.
func TestBootstrap_Expansion_RedirectsConstructionHullsToTrade(t *testing.T) {
	obs := tradeReleaseObs(
		GateWorkerSnapshot{Symbol: "MFG-9", Idle: true},
		GateWorkerSnapshot{Symbol: "MFG-3", Idle: true},
		GateWorkerSnapshot{Symbol: "MFG-5", Idle: true},
	)
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseExpansion || !res.Done {
		t.Fatalf("expected the terminal EXPANSION exit, got phase=%s Done=%v", res.Phase, res.Done)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("expected exactly one trade-redirect call, got %d (%v)", len(rel.tradeCalls), rel.tradeCalls)
	}
	if want := []string{"MFG-3", "MFG-5", "MFG-9"}; !reflect.DeepEqual(rel.tradeCalls[0], want) {
		t.Fatalf("the redirect must hand over every idle construction hull in deterministic order: got %v want %v", rel.tradeCalls[0], want)
	}
	if res.ConstructionHullsToTrade != 3 {
		t.Fatalf("the tick must tally the redirected hulls, got %d want 3", res.ConstructionHullsToTrade)
	}
	if !log.has("bootstrap_construction_hulls_to_trade") {
		t.Fatalf("the redirect must surface on its own log line (observability)")
	}
	// The redirect must NOT un-dedicate: clearing the tag strands the hull (nothing adopts an
	// undedicated hull into trade), so the surplus-release path must stay untouched here.
	if len(rel.calls) != 0 {
		t.Fatalf("EXPANSION must ASSIGN to trade, never un-dedicate to the idle pool, got surplus calls=%v", rel.calls)
	}
}

// MID-DELIVERY GUARD: a construction hull that is still working — in transit to the site, or holding a
// construction task — is NEVER handed over. It keeps its dedication and finishes its leg.
func TestBootstrap_Expansion_NeverRedirectsAHullMidDelivery(t *testing.T) {
	obs := tradeReleaseObs(
		GateWorkerSnapshot{Symbol: "MFG-1", Idle: true},
		GateWorkerSnapshot{Symbol: "MFG-2", Idle: false}, // mid-delivery — must be left alone
		GateWorkerSnapshot{Symbol: "MFG-3", Idle: false}, // mid-delivery — must be left alone
	)
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("expected one trade-redirect call, got %d (%v)", len(rel.tradeCalls), rel.tradeCalls)
	}
	if want := []string{"MFG-1"}; !reflect.DeepEqual(rel.tradeCalls[0], want) {
		t.Fatalf("only the IDLE hull may be redirected — a hull mid-delivery must finish its leg: got %v want %v", rel.tradeCalls[0], want)
	}
}

// Every construction hull busy ⇒ NOTHING is handed over, and the port is not called at all (an empty
// hand-over is not a call). The exit is untouched.
func TestBootstrap_Expansion_AllHullsMidDelivery_MakesNoCall(t *testing.T) {
	obs := tradeReleaseObs(
		GateWorkerSnapshot{Symbol: "MFG-1", Idle: false},
		GateWorkerSnapshot{Symbol: "MFG-2", Idle: false},
	)
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("an empty hand-over must make NO port call, got %v", rel.tradeCalls)
	}
	if !res.Done {
		t.Fatalf("busy construction hulls must not hold the terminal exit, got Done=%v", res.Done)
	}
}

// A fleet with no construction hulls at all (the ordinary mature restart) makes no call — the redirect
// is driven entirely by the observation, so "nothing to do" costs nothing.
func TestBootstrap_Expansion_NoConstructionHulls_MakesNoCall(t *testing.T) {
	h, rel := releasedToTrade(matureObs(), &fakeHandoff{})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("a fleet with no construction hulls must make NO port call, got %v", rel.tradeCalls)
	}
}

// IDEMPOTENCE — the property that survives a restart. The redirect carries no stored "already done"
// flag: it is re-derived from the live observation every tick, so once the hulls are trade-tagged the
// observer stops counting them as gate workers and the re-run selects an EMPTY set and calls nothing.
// This is what makes a coordinator restart (which drops all in-memory state) a non-event, and it is
// strictly stronger than a flag: a hull that was mid-delivery on the first tick is still picked up on
// a later one instead of being written off.
func TestBootstrap_Expansion_Redirect_IsIdempotentAcrossReobservation(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("tick 1 must redirect once, got %v", rel.tradeCalls)
	}

	// The world after the write: MFG-1 now carries "trade", so the observer no longer reports it as a
	// gate worker. Re-run (a restart, or the runner re-entering after the terminal exit).
	h.SetWorldObserver(&fakeObserver{obs: matureObs()})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("re-running against the post-redirect world must perform ZERO further calls, got %v", rel.tradeCalls)
	}
}

// A hull that was mid-delivery when the gate completed is picked up on a LATER tick once it goes idle —
// the payoff of deriving from the observation instead of latching a done-flag on the first tick.
func TestBootstrap_Expansion_MidDeliveryHullIsRedirectedOnceItGoesIdle(t *testing.T) {
	busy := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: false})
	h, rel := releasedToTrade(busy, &fakeHandoff{autoErr: errors.New("launcher down")}) // hold, so a second tick runs

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("tick 1 must redirect nothing while the hull is mid-delivery, got %v", rel.tradeCalls)
	}

	// The leg finished: the hull is idle now.
	h.SetWorldObserver(&fakeObserver{obs: tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 1 || !reflect.DeepEqual(rel.tradeCalls[0], []string{"MFG-1"}) {
		t.Fatalf("a hull that finished its leg must be redirected on the later tick, got %v", rel.tradeCalls)
	}
}

// THE PHASE EDGE: while the gate is still under construction the redirect NEVER fires — the workers are
// building. GATE's own surplus re-balance is the only thing that may touch them there.
func TestBootstrap_Gate_NeverRedirectsConstructionHullsToTrade(t *testing.T) {
	obs := gateObs()
	obs.ConstructionPercent = 99
	obs.GateWorkers = gateWorkerTarget
	for i := 0; i < gateWorkerTarget; i++ {
		obs.GateWorkerHulls = append(obs.GateWorkerHulls, GateWorkerSnapshot{Symbol: string(rune('A' + i)), Idle: true})
	}
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	for tick := 1; tick <= 5; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("tick %d: expected GATE, got %s", tick, res.Phase)
		}
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("GATE must never redirect its workers to trade — they are building, got %v", rel.tradeCalls)
	}
}

// BEST-EFFORT: a redirect failure must NEVER hold the terminal exit and must claim no blocker — a mature
// fleet is never pinned in the per-tick full-fleet re-read over a fleet re-tag. The failure leaves the
// hulls construction-tagged, so the next tick (or the next daemon boot) simply re-derives and retries.
func TestBootstrap_Expansion_RedirectError_NeverBlocksExit_AndRetries(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	h, _ := spiedHandler(obs, &fakeHandoff{})
	flaky := &flakyGateReleaser{failFirst: 1}
	h.SetGateSurplusReleaser(flaky)

	log1 := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log1), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("a failed redirect must NEVER hold the terminal exit, got Done=%v", res.Done)
	}
	if res.Blocker != "" {
		t.Fatalf("the redirect is best-effort and must claim no blocker, got %q", res.Blocker)
	}
	if flaky.calls != 1 || !log1.has("bootstrap_construction_hulls_to_trade_error") {
		t.Fatalf("the failed redirect must have been attempted and surfaced, got calls=%d", flaky.calls)
	}
	if log1.has("bootstrap_construction_hulls_to_trade") {
		t.Fatalf("a wholly failed redirect must not log the success line")
	}
	// The fake returns a NON-ZERO count alongside its error, so a caller that tallied the count while
	// swallowing the error would show a redirect that never happened.
	if res.ConstructionHullsToTrade != 0 {
		t.Fatalf("a failed redirect must tally NOTHING (the count is untrustworthy alongside an error), got %d", res.ConstructionHullsToTrade)
	}

	// The next boot's tick: the hulls are still construction-tagged, so it retries — and succeeds.
	log2 := &capturingLogger{}
	if _, err := h.reconcileOnce(ctxWithLogger(log2), baseCmd()); err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if flaky.calls != 2 || !log2.has("bootstrap_construction_hulls_to_trade") {
		t.Fatalf("the redirect must be retried after a failure, got calls=%d", flaky.calls)
	}
}

// DRY-RUN takes no fleet action — across the whole bounded exit. The WOULD line is the only trace.
func TestBootstrap_Expansion_DryRun_RedirectsNothing(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	log := &capturingLogger{}
	var last reconcileResult
	for i := 0; i < expansionHandoffRetryTicks; i++ {
		res := reconcileResult{}
		h.actExpansion(ctxWithLogger(log), baseCmd(), bootstrapRunConfig{DryRun: true}, obs, &res)
		last = res
	}
	if !last.Done {
		t.Fatalf("dry-run must still take the bounded exit, got %+v", last)
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("dry-run must re-tag NOTHING, got %v", rel.tradeCalls)
	}
	if !log.has("bootstrap_would_redirect_construction_hulls") {
		t.Fatalf("dry-run must log the WOULD line for the redirect")
	}
}

// ADVERSARIAL: no releaser wired at all. The redirect is a logged skip — never a panic, never a blocker,
// and the terminal exit still happens (bootstrap's job is done the moment the gate reads BUILT).
func TestBootstrap_Expansion_NoReleaserWired_SkipsWithoutBlockingExit(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	h, _ := spiedHandler(obs, &fakeHandoff{}) // deliberately no SetGateSurplusReleaser

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("an unwired releaser must not hold the terminal exit, got Done=%v", res.Done)
	}
	if res.Blocker != "" {
		t.Fatalf("the redirect must claim no blocker, got %q", res.Blocker)
	}
	if !log.has("bootstrap_construction_hulls_to_trade_skipped") {
		t.Fatalf("an unwired releaser must surface the skip on its own line")
	}
}

// A PARTIAL redirect (the port re-guards and hands over fewer than asked) is reported honestly: the tally
// is what actually landed, not what was requested.
func TestBootstrap_Expansion_PartialRedirect_TalliesWhatLanded(t *testing.T) {
	obs := tradeReleaseObs(
		GateWorkerSnapshot{Symbol: "MFG-1", Idle: true},
		GateWorkerSnapshot{Symbol: "MFG-2", Idle: true},
		GateWorkerSnapshot{Symbol: "MFG-3", Idle: true},
	)
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetGateSurplusReleaser(&partialGateReleaser{redirect: 2})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.ConstructionHullsToTrade != 2 {
		t.Fatalf("the tally must report what actually landed, got %d want 2", res.ConstructionHullsToTrade)
	}
}

// --- The destination must be MANAGED: ensure the trade-fleet coordinator at EXPANSION ---
//
// Tagging a hull "trade" only earns if something is working the trade fleet. Nothing else guarantees
// that at EXPANSION: LaunchStandingCoordinators is a no-op since the factory retirement,
// ContainerTypeTradeFleetCoordinator is NOT in bootStandingCoordinatorTypes, and the only other caller
// of LaunchTradeFleetCoordinator is the INCOME-phase trade-seed — which a mature fleet restarting into
// EXPANSION never reaches. So the hand-off ensures it here, beside the redirect.

// The EXPANSION tick ensures the standing trade-fleet coordinator, so the hulls it hands over are
// picked up and put on continuous tours rather than pinned to an unmanaged fleet.
func TestBootstrap_Expansion_EnsuresTheTradeFleetCoordinator(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	ho := &fakeHandoff{}
	h, rel := releasedToTrade(obs, ho)

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ho.tradeCoord != 1 {
		t.Fatalf("EXPANSION must ensure the trade-fleet coordinator, got %d launches", ho.tradeCoord)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("the redirect must still run alongside the ensure, got %v", rel.tradeCalls)
	}
	if !res.Done {
		t.Fatalf("the ensure must not hold the terminal exit, got Done=%v", res.Done)
	}
	if !log.has("bootstrap_trade_coordinator_ensured_expansion") {
		t.Fatalf("the ensure must surface on its own log line (observability)")
	}
}

// A mature fleet with NOTHING left to redirect still ensures the coordinator: the hulls were tagged
// "trade" by an earlier run, and this is the restart that must confirm someone is working them. Gating
// the ensure on "are we redirecting?" would miss exactly the case it exists for.
func TestBootstrap_Expansion_EnsuresTradeCoordinator_EvenWithNothingToRedirect(t *testing.T) {
	ho := &fakeHandoff{}
	h, rel := releasedToTrade(matureObs(), ho)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 0 {
		t.Fatalf("nothing to redirect on this fleet, got %v", rel.tradeCalls)
	}
	if ho.tradeCoord != 1 {
		t.Fatalf("the ensure must fire on its own, independent of the redirect, got %d launches", ho.tradeCoord)
	}
}

// idempotentTradeHandoff models the REAL adapter: LaunchTradeFleetCoordinator skips when a coordinator
// is already RUNNING/PENDING, so repeated calls launch exactly one. calls counts invocations, launched
// counts the ones that actually started a container.
//
// It SHADOWS the embedded fakeHandoff's LaunchTradeFleetCoordinator, so the embedded tradeCoord counter
// stays 0 here — assert on calls/launched, never on tradeCoord, which would silently read 0 and mislead.
type idempotentTradeHandoff struct {
	fakeHandoff
	calls    int
	launched int
	running  bool
}

func (f *idempotentTradeHandoff) LaunchTradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) error {
	f.calls++
	if f.running {
		return nil // already RUNNING/PENDING — the adapter's containerTypeRunning guard
	}
	f.running = true
	f.launched++
	return nil
}

// Across the WHOLE bounded exit (every hold tick re-invokes the hand-off), the coordinator is launched
// exactly ONCE — the per-tick call is safe because the launcher is idempotent, the same contract the
// standing-coordinator launch beside it relies on.
func TestBootstrap_Expansion_TradeCoordinatorEnsure_NeverDoubleLaunches(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	ho := &idempotentTradeHandoff{fakeHandoff: fakeHandoff{autoErr: errors.New("autosizer down")}} // hold → many ticks
	h, _ := releasedToTrade(obs, ho)

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done {
		t.Fatalf("expected the bounded exit, ran %d ticks", ticks)
	}
	if ho.calls < 2 {
		t.Fatalf("the hold ticks must each re-invoke the idempotent ensure, got %d calls over %d ticks", ho.calls, ticks)
	}
	if ho.launched != 1 {
		t.Fatalf("the trade-fleet coordinator must be launched exactly ONCE across %d calls, got %d", ho.calls, ho.launched)
	}
}

// ADVERSARIAL: the trade-coordinator launch fails. The redirect must STILL run (a trade-tagged hull with
// a late coordinator is strictly better than a construction-tagged one that idles forever), the failure
// must claim no blocker, and the terminal exit must be untouched.
func TestBootstrap_Expansion_TradeCoordinatorLaunchError_NeverBlocksRedirectOrExit(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	ho := &fakeHandoff{tradeErr: errors.New("trade coordinator launcher down")}
	h, rel := releasedToTrade(obs, ho)

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(rel.tradeCalls) != 1 {
		t.Fatalf("a failed coordinator launch must NOT suppress the redirect, got %v", rel.tradeCalls)
	}
	if !res.Done {
		t.Fatalf("a failed coordinator launch must not hold the terminal exit, got Done=%v", res.Done)
	}
	if res.Blocker != "" {
		t.Fatalf("the ensure is best-effort and must claim no blocker, got %q", res.Blocker)
	}
	if !log.has("bootstrap_trade_coordinator_expansion_error") {
		t.Fatalf("the failed ensure must surface on its own ERROR line")
	}
}

// DRY-RUN launches nothing.
func TestBootstrap_Expansion_DryRun_EnsuresNoTradeCoordinator(t *testing.T) {
	obs := tradeReleaseObs(GateWorkerSnapshot{Symbol: "MFG-1", Idle: true})
	ho := &fakeHandoff{}
	h, _ := releasedToTrade(obs, ho)

	res := reconcileResult{}
	h.actExpansion(ctxWithLogger(&capturingLogger{}), baseCmd(), bootstrapRunConfig{DryRun: true}, obs, &res)
	if ho.tradeCoord != 0 {
		t.Fatalf("dry-run must launch NOTHING, got %d trade-coordinator launches", ho.tradeCoord)
	}
}

// flakyGateReleaser fails its first failFirst trade redirects then succeeds — a TRANSIENT fleet-store
// fault, and ADVERSARIAL: the failing call returns a NON-ZERO count alongside its error, so a caller
// that tallied the count while swallowing the error would over-report and fail the test.
type flakyGateReleaser struct {
	failFirst int
	calls     int
}

func (f *flakyGateReleaser) ReleaseSurplusGateWorkers(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	return len(shipSymbols), nil
}

func (f *flakyGateReleaser) ReleaseGateWorkersToTrade(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	f.calls++
	if f.calls <= f.failFirst {
		return 99, errors.New("fleet store down")
	}
	return len(shipSymbols), nil
}

// partialGateReleaser hands over only `redirect` of the requested hulls — the shape the real adapter
// produces when a hull was re-tagged or started a task between the observation and the write.
type partialGateReleaser struct{ redirect int }

func (p *partialGateReleaser) ReleaseSurplusGateWorkers(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	return len(shipSymbols), nil
}

func (p *partialGateReleaser) ReleaseGateWorkersToTrade(ctx context.Context, playerID int, shipSymbols []string) (int, error) {
	return p.redirect, nil
}
