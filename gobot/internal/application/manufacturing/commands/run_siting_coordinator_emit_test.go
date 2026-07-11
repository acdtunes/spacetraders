package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// captureLogger records WARNING messages for self-check assertions.
type captureLogger struct{ warns []string }

func (l *captureLogger) Log(level, message string, metadata map[string]interface{}) {
	if level == "WARNING" {
		l.warns = append(l.warns, message)
	}
}

func ctxWithLog(l *captureLogger) context.Context {
	return common.WithLogger(context.Background(), l)
}

// emitWith runs EMIT with a scout-demand emitter over the given desired portfolio.
func emitWith(cmd *RunSitingCoordinatorCommand, emitter ScoutDemandEmitter, desired []ScoredCandidate) (int, *RunSitingCoordinatorHandler) {
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, &fakeChainProjector{}, &fakeChainController{}, nil)
	if emitter != nil {
		h.SetScoutDemandEmitter(emitter)
	}
	return h.emit(context.Background(), cmd, resolveSitingConfig(cmd), desired), h
}

func staleCand(good, system string, ageSecs float64) ScoredCandidate {
	return ScoredCandidate{SitingCandidate: SitingCandidate{Good: good, System: system, DataAgeSecs: ageSecs}, Proceed: true}
}

// A desired site whose data is older than EmitStaleness emits scout-demand for its system.
func TestEmit_StalePromisingEmitsScoutDemand(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	em := &fakeScoutDemandEmitter{}
	n, _ := emitWith(cmd, em, []ScoredCandidate{staleCand("ELEC", "S1", 3000)})
	if n != 1 || len(em.emitted) != 1 || em.emitted[0] != "S1" {
		t.Errorf("want 1 scout-demand for S1, got n=%d emitted=%v", n, em.emitted)
	}
}

// A fresh desired site emits nothing.
func TestEmit_FreshSiteEmitsNothing(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	em := &fakeScoutDemandEmitter{}
	n, _ := emitWith(cmd, em, []ScoredCandidate{staleCand("ELEC", "S1", 100)})
	if n != 0 || len(em.emitted) != 0 {
		t.Errorf("fresh site must not emit; n=%d emitted=%v", n, em.emitted)
	}
}

// A nil emitter disables EMIT (no panic).
func TestEmit_NilEmitterDisabled(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	n, _ := emitWith(cmd, nil, []ScoredCandidate{staleCand("ELEC", "S1", 3000)})
	if n != 0 {
		t.Errorf("nil emitter → 0, got %d", n)
	}
}

// Two stale sites in one system collapse to a single scout-demand for that system.
func TestEmit_DedupsPerSystem(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	em := &fakeScoutDemandEmitter{}
	n, _ := emitWith(cmd, em, []ScoredCandidate{
		staleCand("ELEC", "S1", 3000),
		staleCand("MACH", "S1", 4000),
	})
	if n != 1 || len(em.emitted) != 1 {
		t.Errorf("two stale sites in one system → one demand; n=%d emitted=%v", n, em.emitted)
	}
}

// The emitter's cooldown suppression (HasSince) is honored — a suppressed system is not counted.
func TestEmit_CooldownSuppression(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	em := &fakeScoutDemandEmitter{suppressed: map[string]bool{"S1": true}}
	n, _ := emitWith(cmd, em, []ScoredCandidate{staleCand("ELEC", "S1", 3000)})
	if n != 0 {
		t.Errorf("cooldown-suppressed system must not count; n=%d", n)
	}
}

// An emit error is logged and skipped (not counted).
func TestEmit_ErrorSkipped(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, EmitStalenessSecs: 1800}
	em := &fakeScoutDemandEmitter{err: errors.New("event store down")}
	n, _ := emitWith(cmd, em, []ScoredCandidate{staleCand("ELEC", "S1", 3000)})
	if n != 0 {
		t.Errorf("emit error must not count; n=%d", n)
	}
}

// --- Effect self-check ---

// vetoProjector vetoes every candidate (scored == 0).
func vetoProjector(candidates []SitingCandidate) *fakeChainProjector {
	byKey := map[string]ChainProjection{}
	for _, c := range candidates {
		byKey[c.Key()] = ChainProjection{Proceed: false, Reason: "negative_chain_margin"}
	}
	return &fakeChainProjector{byKey: byKey}
}

// All candidates vetoed for N consecutive ticks → exactly one WARN (edge-triggered).
func TestSelfCheck_AllVetoedFiresOnce(t *testing.T) {
	cands := []SitingCandidate{{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}}}
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{candidates: cands}, vetoProjector(cands), &fakeChainController{}, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: 100})
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"} // EffectSelfcheckTicks default 4
	log := &captureLogger{}
	ctx := ctxWithLog(log)

	for i := 0; i < 6; i++ {
		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	selfChecks := countContains(log.warns, "produced no effect")
	if selfChecks != 1 {
		t.Errorf("all-vetoed over 6 ticks must WARN exactly once, got %d: %v", selfChecks, log.warns)
	}
	if selfChecks == 1 && !strings.Contains(firstContaining(log.warns, "produced no effect"), "vetoed") {
		t.Errorf("veto WARN must name the veto cause: %q", firstContaining(log.warns, "produced no effect"))
	}
}

// A productive tick between veto streaks resets the counter, so N is never reached.
func TestSelfCheck_ProductiveTickResets(t *testing.T) {
	cands := []SitingCandidate{{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}}}
	proj := vetoProjector(cands)
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{candidates: cands}, proj, &fakeChainController{}, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: 100})
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	log := &captureLogger{}
	ctx := ctxWithLog(log)

	// 3 vetoed (streak 3, < 4), 1 productive (reset), 3 vetoed (streak 3 again).
	for i := 0; i < 3; i++ {
		h.reconcileOnce(ctx, cmd)
	}
	proj.byKey = map[string]ChainProjection{"ELEC@S1": {ProjectedPL: 1000, Proceed: true}} // productive
	h.reconcileOnce(ctx, cmd)
	proj.byKey = map[string]ChainProjection{"ELEC@S1": {Proceed: false}} // vetoed again
	for i := 0; i < 3; i++ {
		h.reconcileOnce(ctx, cmd)
	}
	if n := countContains(log.warns, "produced no effect"); n != 0 {
		t.Errorf("a productive tick must reset the streak → no WARN, got %d: %v", n, log.warns)
	}
}

// Dry-run suppressing a real desired portfolio fires the self-check naming dry_run.
func TestSelfCheck_DryRunSuppressingFires(t *testing.T) {
	cands := []SitingCandidate{{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}}}
	proj := plProj(map[string]ChainProjection{"ELEC@S1": {ProjectedPL: 1000, Proceed: true}})
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{candidates: cands}, proj, &fakeChainController{}, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: 100})
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1", DryRun: true}
	log := &captureLogger{}
	ctx := ctxWithLog(log)

	for i := 0; i < 5; i++ {
		h.reconcileOnce(ctx, cmd)
	}
	w := firstContaining(log.warns, "produced no effect")
	if w == "" || !strings.Contains(w, "dry_run") {
		t.Errorf("dry-run suppression must WARN naming dry_run, got: %v", log.warns)
	}
}

// A healthy satisfied portfolio (candidates score, running already matches desired) never warns.
func TestSelfCheck_HealthySatisfiedNeverWarns(t *testing.T) {
	cands := []SitingCandidate{{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}}}
	proj := plProj(map[string]ChainProjection{"ELEC@S1": {ProjectedPL: 1000, Proceed: true}})
	ctrl := &fakeChainController{running: []RunningChain{{FactoryID: "fac-ELEC", Good: "ELEC", System: "S1"}}}
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{candidates: cands}, proj, ctrl, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: 100})
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	log := &captureLogger{}
	ctx := ctxWithLog(log)

	for i := 0; i < 8; i++ {
		h.reconcileOnce(ctx, cmd)
	}
	if n := countContains(log.warns, "produced no effect"); n != 0 {
		t.Errorf("a satisfied steady-state portfolio must never WARN, got %d: %v", n, log.warns)
	}
}

func countContains(ss []string, sub string) int {
	n := 0
	for _, s := range ss {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func firstContaining(ss []string, sub string) string {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return s
		}
	}
	return ""
}
