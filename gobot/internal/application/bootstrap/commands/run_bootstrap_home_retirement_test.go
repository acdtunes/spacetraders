package commands

import (
	"context"
	"errors"
	"testing"
)

// --- Home 3→1 at EXPANSION: the hand-off releases the home scout reinforcement ---
//
// Cold start pins probeTarget probes on the home post (the MinHulls floor the sensing coordinator
// honours) so market data flows before the standing economy exists. Once the arc reaches EXPANSION
// the reinforcement's job is done: bootstrap lowers the home floor to expansionHomeMinHulls and the
// probe-sensing coordinator resizes home to the standard rule on its own next tick — that resize IS
// the retirement, and the freed probes become the sensing buyer's supply.
//
// The release is keyed on the WORLD signal (reaching EXPANSION means the home gate reads BUILT),
// not on the hand-off outcome — the same doctrine as the terminal exit itself — so the confirmed
// hand-off AND the bounded-exit WARN path both lower it. And because the runner's terminal contract
// in this exact function has broken before (the container spun, re-entering Handle after Done), the
// write must be idempotent within the run: a second invocation performs ZERO writes.

// lowerCalls counts the declarer calls that carried the EXPANSION floor (the lowering writes), as
// opposed to cold start's probeTarget declarations.
func lowerCalls(d *fakeDeclarer) int {
	n := 0
	for _, m := range d.minHulls {
		if m == expansionHomeMinHulls {
			n++
		}
	}
	return n
}

// The confirmed hand-off lowers the HOME post's floor to 1 exactly once, and a second invocation —
// the defence-in-depth pacing re-entry the runner performs if the terminal contract ever regresses
// again — performs ZERO further writes (the T6-era hazard assertion).
func TestBootstrap_HomeRetirement_ConfirmedHandoff_LowersOnce_SecondInvocationZeroWrites(t *testing.T) {
	h, spies := spiedHandler(matureObs(), &fakeHandoff{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if !res.Done || !res.HandoffLaunched {
		t.Fatalf("expected the confirmed hand-off exit, got Done=%v HandoffLaunched=%v", res.Done, res.HandoffLaunched)
	}
	if spies.posts.calls != 1 || lowerCalls(spies.posts) != 1 {
		t.Fatalf("the confirmed hand-off must lower the home floor exactly once, got calls=%d minHulls=%v", spies.posts.calls, spies.posts.minHulls)
	}
	if spies.posts.systems[0] != "X1-HQ" {
		t.Fatalf("the lowering must target the HOME system, got %v", spies.posts.systems)
	}
	if !log.has("bootstrap_home_reinforcement_released") {
		t.Fatalf("the release must surface on its own log line (observability)")
	}

	// The pacing re-entry: invoke again. The floor write must NOT repeat — zero further port calls.
	res2, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("re-entry: reconcileOnce: %v", err)
	}
	if !res2.Done {
		t.Fatalf("the terminal exit must be idempotent across a re-entry, got Done=%v", res2.Done)
	}
	if spies.posts.calls != 1 {
		t.Fatalf("a second invocation after the hand-off must perform ZERO writes, got %d total declarer calls (minHulls=%v)", spies.posts.calls, spies.posts.minHulls)
	}
}

// The other confirmed branch: the autosizer was launched EARLY (cold-start scaling), so EXPANSION
// confirms through ensureStandingHandoff — the floor comes down there too.
func TestBootstrap_HomeRetirement_EarlyLaunchedAutosizer_StillLowers(t *testing.T) {
	obs := matureObs()
	obs.AutosizerRunning = true
	ho := &fakeHandoff{}
	h, spies := spiedHandler(obs, ho)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.Done || ho.standing != 1 || ho.autosizer != 0 {
		t.Fatalf("expected the ensureStandingHandoff confirmed exit, got Done=%v standing=%d autosizer=%d", res.Done, ho.standing, ho.autosizer)
	}
	if lowerCalls(spies.posts) != 1 {
		t.Fatalf("the early-launched confirmed hand-off must lower the home floor once, got minHulls=%v", spies.posts.minHulls)
	}
}

// The UNCONFIRMED hand-off (the bounded-exit WARN path) also lowers the floor: the world signal owns
// the phase, not the hand-off — a launcher that is down cannot keep the home reinforcement pinned.
// And across the hold ticks the lowering still fires exactly ONCE (the in-run idempotency).
func TestBootstrap_HomeRetirement_UnconfirmedHandoff_WarnExit_StillLowersExactlyOnce(t *testing.T) {
	h, spies := spiedHandler(matureObs(), &fakeHandoff{autoErr: errors.New("autosizer launcher down")})

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done || res.HandoffLaunched {
		t.Fatalf("expected the bounded UNCONFIRMED exit, got Done=%v HandoffLaunched=%v after %d ticks", res.Done, res.HandoffLaunched, ticks)
	}
	if spies.posts.calls != 1 || lowerCalls(spies.posts) != 1 {
		t.Fatalf("the WARN-path exit must lower the home floor exactly once across the hold ticks, got calls=%d minHulls=%v", spies.posts.calls, spies.posts.minHulls)
	}
}

// The strongest world-signal shape: NO hand-off launcher wired at all. Nothing can launch, the exit
// takes the bounded WARN path — and the home floor still comes down.
func TestBootstrap_HomeRetirement_NoLauncherWired_StillLowers(t *testing.T) {
	h, spies := spiedHandler(matureObs(), nil)

	ticks, res := runToExit(t, h, baseCmd(), 25)
	if !res.Done {
		t.Fatalf("a mature fleet with no launcher must still exit, ran %d ticks", ticks)
	}
	if spies.posts.calls != 1 || lowerCalls(spies.posts) != 1 {
		t.Fatalf("the no-launcher bounded exit must still lower the home floor exactly once, got calls=%d minHulls=%v", spies.posts.calls, spies.posts.minHulls)
	}
}

// A FRESH fleet (no EXPANSION reached) must never touch the floor: cold start keeps declaring the
// home post with the probeTarget floor every tick, byte-identical — never the EXPANSION value.
func TestBootstrap_HomeRetirement_FreshFleet_NeverLowers(t *testing.T) {
	h, spies := spiedHandler(freshDataObs(), &fakeHandoff{})

	const ticks = 20
	for tick := 1; tick <= ticks; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Done {
			t.Fatalf("tick %d: a fresh fleet must never be terminal", tick)
		}
	}
	if lowerCalls(spies.posts) != 0 {
		t.Fatalf("a fresh fleet must NEVER lower the home floor, got minHulls=%v", spies.posts.minHulls)
	}
	if spies.posts.calls != ticks {
		t.Fatalf("cold start must keep declaring the home post every tick (byte-identical), got %d calls over %d ticks", spies.posts.calls, ticks)
	}
	for i, m := range spies.posts.minHulls {
		if m != probeTarget {
			t.Fatalf("cold-start declaration %d must carry the probeTarget floor %d, got %d (%v)", i, probeTarget, m, spies.posts.minHulls)
		}
	}
}

// A fleet mid-gate-construction — even at 99% — has not reached EXPANSION: no declaration of any
// kind fires in GATE, so the floor is untouched until the gate actually reads BUILT.
func TestBootstrap_HomeRetirement_GateUnderConstruction_NeverLowers(t *testing.T) {
	obs := gateObs()
	obs.ConstructionPercent = 99
	h, spies := spiedHandler(obs, &fakeHandoff{})

	for tick := 1; tick <= 5; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: reconcileOnce: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("tick %d: expected GATE, got %s", tick, res.Phase)
		}
	}
	if spies.posts.calls != 0 {
		t.Fatalf("GATE must never touch the home post, got calls=%d minHulls=%v", spies.posts.calls, spies.posts.minHulls)
	}
}

// flakyDeclarer fails its first failFirst calls then succeeds — a TRANSIENT scout-post store fault,
// the adversarial shape for the release's best-effort contract.
type flakyDeclarer struct {
	failFirst int
	calls     int
	minHulls  []int
}

func (f *flakyDeclarer) DeclareHomeScoutPost(ctx context.Context, playerID int, system string, minHulls int) error {
	f.calls++
	f.minHulls = append(f.minHulls, minHulls)
	if f.calls <= f.failFirst {
		return errors.New("scout post store down")
	}
	return nil
}

// A floor-write failure must NEVER hold the terminal exit (the world signal owns termination — a
// mature fleet is never pinned in the per-tick full-fleet re-read over a floor write), must claim no
// blocker, and must leave the release UN-marked so the next invocation (the next daemon boot —
// bootstrap is boot-standing) retries it.
func TestBootstrap_HomeRetirement_DeclarerError_NeverBlocksExit_RetriedNextInvocation(t *testing.T) {
	h, _ := spiedHandler(matureObs(), &fakeHandoff{})
	flaky := &flakyDeclarer{failFirst: 1}
	h.SetScoutPostDeclarer(flaky)

	log1 := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log1), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("a failed floor write must NEVER hold the terminal exit, got Done=%v", res.Done)
	}
	if res.Blocker != "" {
		t.Fatalf("the floor release is best-effort and must claim no blocker, got %q", res.Blocker)
	}
	if flaky.calls != 1 || !log1.has("bootstrap_home_reinforcement_error") {
		t.Fatalf("the failed write must have been attempted and surfaced, got calls=%d", flaky.calls)
	}
	if log1.has("bootstrap_home_reinforcement_released") {
		t.Fatalf("a failed write must not log the release")
	}

	// The next boot's tick: the failure left the release unmarked, so it retries — and succeeds.
	log2 := &capturingLogger{}
	if _, err := h.reconcileOnce(ctxWithLogger(log2), baseCmd()); err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if flaky.calls != 2 || !log2.has("bootstrap_home_reinforcement_released") {
		t.Fatalf("the release must be retried after a failure, got calls=%d", flaky.calls)
	}
}

// DRY-RUN takes no floor action — across the whole bounded exit. The WOULD line is the only trace.
func TestBootstrap_HomeRetirement_DryRun_WritesNothing(t *testing.T) {
	h, spies := spiedHandler(matureObs(), &fakeHandoff{})

	log := &capturingLogger{}
	var last reconcileResult
	for i := 0; i < expansionHandoffRetryTicks; i++ {
		res := reconcileResult{}
		h.actExpansion(ctxWithLogger(log), baseCmd(), bootstrapRunConfig{DryRun: true}, matureObs(), &res)
		last = res
	}
	if !last.Done {
		t.Fatalf("dry-run must still take the bounded exit (Done on the final hold tick), got %+v", last)
	}
	if spies.posts.calls != 0 {
		t.Fatalf("dry-run must write NOTHING, got %d declarer calls (minHulls=%v)", spies.posts.calls, spies.posts.minHulls)
	}
	if !log.has("bootstrap_would_release_home_reinforcement") {
		t.Fatalf("dry-run must log the WOULD line for the floor release")
	}
}

// ADVERSARIAL: the mature observation resolves no home system. The release is skipped (there is no
// post to address), surfaced on its own line, and the exit is untouched.
func TestBootstrap_HomeRetirement_NoHomeSystem_SkipsWithoutBlockingExit(t *testing.T) {
	obs := matureObs()
	obs.HomeSystem = ""
	h, spies := spiedHandler(obs, &fakeHandoff{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("an unresolved home system must not hold the terminal exit, got Done=%v", res.Done)
	}
	if spies.posts.calls != 0 {
		t.Fatalf("no home system ⇒ no declaration, got %d calls", spies.posts.calls)
	}
	if !log.has("bootstrap_home_reinforcement_skipped") {
		t.Fatalf("the skip must surface on its own log line")
	}
}

// ADVERSARIAL: no declarer wired at all (the gateHandler wiring every existing EXPANSION test uses).
// The release degrades to a logged skip — no panic, no blocker, exit untouched.
func TestBootstrap_HomeRetirement_NilDeclarer_SkipsSafely(t *testing.T) {
	h := gateHandler(matureObs(), &fakeConstruction{}, &fakeManufacturing{}, &fakeRepurposer{}, &fakeGateAcquirer{}, &fakeHandoff{})

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.Done || !res.HandoffLaunched {
		t.Fatalf("a nil declarer must not hold the terminal exit, got Done=%v HandoffLaunched=%v", res.Done, res.HandoffLaunched)
	}
	if res.Blocker != "" {
		t.Fatalf("a nil declarer must claim no blocker (best-effort release), got %q", res.Blocker)
	}
	if !log.has("bootstrap_home_reinforcement_skipped") {
		t.Fatalf("the nil-declarer skip must surface on its own log line")
	}
}
