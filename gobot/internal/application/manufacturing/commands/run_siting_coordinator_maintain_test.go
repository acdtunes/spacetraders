package commands

import (
	"context"
	"testing"
)

func scoredCand(good, system string, score float64, inputs ...string) ScoredCandidate {
	return ScoredCandidate{
		SitingCandidate: SitingCandidate{Good: good, System: system, InputMarkets: inputs},
		Score:           score,
		Proceed:         true,
	}
}

func handlerWithWorkers(count int, err error) *RunSitingCoordinatorHandler {
	h := newTestHandler(nil, nil, nil)
	h.SetWorkerCounter(&fakeWorkerCounter{count: count, err: err})
	return h
}

// --- resolveK ---

func TestResolveK_TopKOverrideWins(t *testing.T) {
	h := handlerWithWorkers(100, nil) // would derive a large K, but the override wins
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, TopK: 6}
	k, ok := h.resolveK(context.Background(), cmd, resolveSitingConfig(cmd))
	if !ok || k != 6 {
		t.Errorf("k=%d ok=%v, want 6/true (override)", k, ok)
	}
}

func TestResolveK_DerivesFromWorkers(t *testing.T) {
	h := handlerWithWorkers(14, nil)
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1} // TopK unset → derive; WorkersPerChain default 3.5
	k, ok := h.resolveK(context.Background(), cmd, resolveSitingConfig(cmd))
	if !ok || k != 4 { // floor(14 / 3.5) = 4
		t.Errorf("k=%d ok=%v, want 4/true", k, ok)
	}
}

func TestResolveK_NoWorkerCounterNoOverrideIsUnsized(t *testing.T) {
	h := newTestHandler(nil, nil, nil) // no worker counter wired
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	if _, ok := h.resolveK(context.Background(), cmd, resolveSitingConfig(cmd)); ok {
		t.Error("no override + no worker counter must be unsized (ok=false)")
	}
}

func TestResolveK_WorkerErrorIsUnsized(t *testing.T) {
	h := handlerWithWorkers(0, context.DeadlineExceeded)
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	if _, ok := h.resolveK(context.Background(), cmd, resolveSitingConfig(cmd)); ok {
		t.Error("worker read error must be unsized (ok=false) — do not churn on a transient failure")
	}
}

func TestResolveK_ZeroWorkersIsZeroK(t *testing.T) {
	h := handlerWithWorkers(0, nil)
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	k, ok := h.resolveK(context.Background(), cmd, resolveSitingConfig(cmd))
	if !ok || k != 0 {
		t.Errorf("k=%d ok=%v, want 0/true (no hulls → no chains)", k, ok)
	}
}

// --- maintain: top-K + concentration caps ---

func TestMaintain_TopKByScore(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{}
	cfg := resolveSitingConfig(cmd) // default caps 3 / 2
	scored := []ScoredCandidate{
		scoredCand("A", "S1", 900, "M1"),
		scoredCand("B", "S2", 800, "M2"),
		scoredCand("C", "S3", 700, "M3"),
		scoredCand("D", "S4", 600, "M4"),
	}
	got := newTestHandler(nil, nil, nil).maintain(cfg, scored, 2)
	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "B" {
		t.Errorf("want top-2 [A B], got %v", scoredGoods(got))
	}
}

func TestMaintain_PerSystemCap(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{MaxChainsPerSystem: 1}
	cfg := resolveSitingConfig(cmd)
	scored := []ScoredCandidate{
		scoredCand("A", "S1", 900, "M1"), // takes S1's only slot
		scoredCand("B", "S1", 800, "M2"), // same system → skipped
		scoredCand("C", "S2", 700, "M3"), // different system → fits
	}
	got := newTestHandler(nil, nil, nil).maintain(cfg, scored, 3)
	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "C" {
		t.Errorf("per-system cap 1 → [A C], got %v", scoredGoods(got))
	}
}

func TestMaintain_PerInputMarketCap(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{MaxChainsPerInputMarket: 1}
	cfg := resolveSitingConfig(cmd)
	scored := []ScoredCandidate{
		scoredCand("A", "S1", 900, "SHARED"), // takes SHARED's only slot
		scoredCand("B", "S2", 800, "SHARED"), // shares SHARED → skipped
		scoredCand("C", "S3", 700, "OTHER"),  // different feed → fits
	}
	got := newTestHandler(nil, nil, nil).maintain(cfg, scored, 3)
	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "C" {
		t.Errorf("per-input-market cap 1 → [A C], got %v", scoredGoods(got))
	}
}

// A cap-blocked candidate does NOT consume a K slot — a lower-scored candidate that fits takes it.
func TestMaintain_CapSkippedDoesNotConsumeSlot(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{MaxChainsPerSystem: 1}
	cfg := resolveSitingConfig(cmd)
	scored := []ScoredCandidate{
		scoredCand("A", "S1", 900, "M1"),
		scoredCand("B", "S1", 800, "M2"), // skipped (S1 full)
		scoredCand("C", "S2", 700, "M3"),
		scoredCand("D", "S3", 600, "M4"),
	}
	// K=2: A takes a slot, B is skipped (does not consume the 2nd slot), C fills it.
	got := newTestHandler(nil, nil, nil).maintain(cfg, scored, 2)
	if len(got) != 2 || got[0].Good != "A" || got[1].Good != "C" {
		t.Errorf("cap-skip must not consume a slot → [A C], got %v", scoredGoods(got))
	}
}

func TestMaintain_ZeroKIsEmpty(t *testing.T) {
	cfg := resolveSitingConfig(&RunSitingCoordinatorCommand{})
	got := newTestHandler(nil, nil, nil).maintain(cfg, []ScoredCandidate{scoredCand("A", "S1", 900, "M1")}, 0)
	if len(got) != 0 {
		t.Errorf("K=0 → empty, got %v", scoredGoods(got))
	}
}

// --- reconcileOnce: unsized path leaves the portfolio untouched ---

func TestReconcile_UnsizedSkipsActWarnsOnce(t *testing.T) {
	scanner := &fakeSitingScanner{candidates: []SitingCandidate{{Good: "ELEC", System: "S1", InputMarkets: []string{"M1"}}}}
	proj := plProj(map[string]ChainProjection{"ELEC@S1": {ProjectedPL: 1000, Proceed: true}})
	ctrl := &fakeChainController{}
	h := NewRunSitingCoordinatorHandler(scanner, proj, ctrl, nil) // no worker counter → unsized

	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, ContainerID: "c1"} // no TopK
	res, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Launched != 0 || res.Retired != 0 {
		t.Errorf("unsized tick must not launch/retire, got %+v", res)
	}
	if len(ctrl.launched) != 0 {
		t.Errorf("unsized tick must not call Launch, got %v", ctrl.launched)
	}
	// Edge-trigger: the warn latch is set after one unsized tick.
	if st := h.coordinatorState("c1"); !st.unsizedWarned {
		t.Error("unsized WARN must be latched after the first unsized tick")
	}
}

func scoredGoods(cs []ScoredCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Good
	}
	return out
}
