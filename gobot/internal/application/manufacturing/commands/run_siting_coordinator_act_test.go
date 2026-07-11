package commands

import (
	"context"
	"errors"
	"testing"
)

func runningChain(good, system string) RunningChain {
	return RunningChain{FactoryID: "fac-" + good, Good: good, System: system}
}

// actWith runs ACT once against a controller with a given running set.
func actWith(t *testing.T, cmd *RunSitingCoordinatorCommand, ctrl *fakeChainController, desired []ScoredCandidate) (launched, retired int, h *RunSitingCoordinatorHandler) {
	t.Helper()
	h = NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, &fakeChainProjector{}, ctrl, nil)
	l, r := h.act(context.Background(), cmd, resolveSitingConfig(cmd), desired)
	return l, r, h
}

// LAUNCH: a desired chain not yet running is launched through the controller.
func TestAct_LaunchesMissing(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1")}}
	desired := []ScoredCandidate{scoredCand("A", "S1", 900), scoredCand("B", "S1", 800)}

	launched, retired, _ := actWith(t, cmd, ctrl, desired)
	if launched != 1 || retired != 0 {
		t.Fatalf("launched=%d retired=%d, want 1/0", launched, retired)
	}
	if len(ctrl.launched) != 1 || ctrl.launched[0] != "B@S1" {
		t.Errorf("want Launch(B@S1), got %v", ctrl.launched)
	}
}

// A portfolio already matching desired launches/retires nothing.
func TestAct_NoOpWhenMatched(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1")}}
	launched, retired, _ := actWith(t, cmd, ctrl, []ScoredCandidate{scoredCand("A", "S1", 900)})
	if launched != 0 || retired != 0 || len(ctrl.launched) != 0 || len(ctrl.retired) != 0 {
		t.Errorf("matched portfolio must be a no-op; launched=%d retired=%d", launched, retired)
	}
}

// RETIRE with the default hysteresis (2): a fallen chain is held one tick, retired the next.
func TestAct_RetireHonorsHysteresis(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"} // RetireHysteresisTicks default 2
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1"), runningChain("B", "S1")}}
	desired := []ScoredCandidate{scoredCand("A", "S1", 900)} // B fell out
	cfg := resolveSitingConfig(cmd)
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, &fakeChainProjector{}, ctrl, nil)

	// Tick 1: B out (1/2) — held, not retired.
	if _, r := h.act(context.Background(), cmd, cfg, desired); r != 0 {
		t.Fatalf("tick1 retired=%d, want 0 (hysteresis hold)", r)
	}
	if len(ctrl.retired) != 0 {
		t.Fatalf("tick1 must not retire, got %v", ctrl.retired)
	}
	// Tick 2: B out (2/2) — retired.
	if _, r := h.act(context.Background(), cmd, cfg, desired); r != 1 {
		t.Fatalf("tick2 retired=%d, want 1", r)
	}
	if len(ctrl.retired) != 1 || ctrl.retired[0] != "fac-B" {
		t.Errorf("want Retire(fac-B), got %v", ctrl.retired)
	}
}

// Hysteresis resets: a chain that re-enters desired before the window elapses is never retired.
func TestAct_HysteresisResetsOnReentry(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1"), runningChain("B", "S1")}}
	cfg := resolveSitingConfig(cmd)
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, &fakeChainProjector{}, ctrl, nil)

	withB := []ScoredCandidate{scoredCand("A", "S1", 900), scoredCand("B", "S1", 800)}
	withoutB := []ScoredCandidate{scoredCand("A", "S1", 900)}

	h.act(context.Background(), cmd, cfg, withoutB) // tick1: B out (1/2)
	h.act(context.Background(), cmd, cfg, withB)    // tick2: B back → counter cleared
	h.act(context.Background(), cmd, cfg, withoutB) // tick3: B out again (1/2, fresh)
	if len(ctrl.retired) != 0 {
		t.Errorf("re-entry must reset hysteresis; B should never retire, got %v", ctrl.retired)
	}
}

// RetireHysteresisTicks=1 retires on the first tick out of top-K.
func TestAct_HysteresisOneRetiresImmediately(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1", RetireHysteresisTicks: 1}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1"), runningChain("B", "S1")}}
	_, retired, _ := actWith(t, cmd, ctrl, []ScoredCandidate{scoredCand("A", "S1", 900)})
	if retired != 1 || len(ctrl.retired) != 1 {
		t.Errorf("hysteresis 1 must retire immediately; retired=%d %v", retired, ctrl.retired)
	}
}

// Dry-run evaluates but takes no action (no launch, no retire).
func TestAct_DryRunTakesNoAction(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1", DryRun: true, RetireHysteresisTicks: 1}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1")}}
	desired := []ScoredCandidate{scoredCand("B", "S1", 800)} // A should retire, B should launch

	launched, retired, _ := actWith(t, cmd, ctrl, desired)
	if launched != 0 || retired != 0 {
		t.Errorf("dry-run must count zero effect; launched=%d retired=%d", launched, retired)
	}
	if len(ctrl.launched) != 0 || len(ctrl.retired) != 0 {
		t.Errorf("dry-run must not call Launch/Retire; launched=%v retired=%v", ctrl.launched, ctrl.retired)
	}
}

// A launch error is logged and skipped (not counted), not fatal to the rest of the tick.
func TestAct_LaunchErrorSkipped(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	ctrl := &fakeChainController{launchErr: errors.New("yard busy")}
	launched, _, _ := actWith(t, cmd, ctrl, []ScoredCandidate{scoredCand("B", "S1", 800)})
	if launched != 0 {
		t.Errorf("launch error must not count; launched=%d", launched)
	}
}

// If the running set cannot be read, ACT does nothing (never launch a duplicate on a blind view).
func TestAct_RunningReadErrorSkipsAct(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}
	ctrl := &fakeChainController{runningErr: errors.New("db down")}
	launched, retired, _ := actWith(t, cmd, ctrl, []ScoredCandidate{scoredCand("B", "S1", 800)})
	if launched != 0 || retired != 0 || len(ctrl.launched) != 0 {
		t.Errorf("running-read error must skip ACT entirely; launched=%d retired=%d", launched, retired)
	}
}

// A retire error leaves the counter armed so the next tick retries (no silent give-up).
func TestAct_RetireErrorRetriesNextTick(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1", RetireHysteresisTicks: 1}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("B", "S1")}, retireErr: errors.New("stop failed")}
	cfg := resolveSitingConfig(cmd)
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, &fakeChainProjector{}, ctrl, nil)
	desired := []ScoredCandidate{} // B fell out

	if _, r := h.act(context.Background(), cmd, cfg, desired); r != 0 {
		t.Fatalf("retire error must not count; retired=%d", r)
	}
	// Counter stayed armed → next tick attempts retire again.
	ctrl.retireErr = nil
	if _, r := h.act(context.Background(), cmd, cfg, desired); r != 1 {
		t.Errorf("next tick must retry the retire; retired=%d", r)
	}
}

// Launch and retire in the same tick.
func TestAct_LaunchAndRetireSameTick(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1", RetireHysteresisTicks: 1}
	ctrl := &fakeChainController{running: []RunningChain{runningChain("A", "S1"), runningChain("B", "S1")}}
	desired := []ScoredCandidate{scoredCand("A", "S1", 900), scoredCand("C", "S1", 700)} // launch C, retire B

	launched, retired, _ := actWith(t, cmd, ctrl, desired)
	if launched != 1 || retired != 1 {
		t.Fatalf("want 1 launch 1 retire, got %d/%d", launched, retired)
	}
	if ctrl.launched[0] != "C@S1" || ctrl.retired[0] != "fac-B" {
		t.Errorf("want Launch(C@S1)+Retire(fac-B), got %v / %v", ctrl.launched, ctrl.retired)
	}
}

// End-to-end: a candidate vetoed by the launch guard at SCORE is never launched (zero cost).
func TestReconcile_VetoedCandidateNeverLaunched(t *testing.T) {
	scanner := &fakeSitingScanner{candidates: []SitingCandidate{
		{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}},
		{Good: "MACH", System: "S1", InputMarkets: []string{"M2"}},
	}}
	proj := plProj(map[string]ChainProjection{
		"ELEC@S1": {ProjectedPL: 1000, Proceed: true},
		"MACH@S1": {ProjectedPL: 5000, Proceed: false, Reason: "negative_chain_margin"}, // vetoed
	})
	ctrl := &fakeChainController{running: nil}
	h := NewRunSitingCoordinatorHandler(scanner, proj, ctrl, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: 100}) // K large enough for both

	if _, err := h.reconcileOnce(context.Background(), &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctrl.launched) != 1 || ctrl.launched[0] != "ELEC@S1" {
		t.Errorf("only the guard-approved chain must launch; got %v (the vetoed MACH must be absent)", ctrl.launched)
	}
}
