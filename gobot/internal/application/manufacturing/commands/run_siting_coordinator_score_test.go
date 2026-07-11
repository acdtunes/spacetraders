package commands

import (
	"context"
	"errors"
	"testing"
)

// scoreWith runs the SCORE step with a projector (PLs/vetoes by key) and an optional
// alignment provider, over the given candidates, using the command's resolved config.
func scoreWith(cmd *RunSitingCoordinatorCommand, projector ChainProjector, alignment TourAlignmentProvider, candidates []SitingCandidate) []ScoredCandidate {
	h := NewRunSitingCoordinatorHandler(&fakeSitingScanner{}, projector, &fakeChainController{}, nil)
	if alignment != nil {
		h.SetTourAlignmentProvider(alignment)
	}
	return h.score(context.Background(), cmd, resolveSitingConfig(cmd), candidates)
}

func plProj(byKey map[string]ChainProjection) *fakeChainProjector {
	return &fakeChainProjector{byKey: byKey}
}

// Base math: no alignment, fresh data, private inputs → Score = ProjectedPL × 1.0.
func TestScore_BaseMathIsProjectedPL(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	cands := []SitingCandidate{{Good: "ELEC", System: "X1-AA", DataAgeSecs: 0, InputMarkets: []string{"M1"}}}
	proj := plProj(map[string]ChainProjection{"ELEC@X1-AA": {ProjectedPL: 1000, Proceed: true}})

	got := scoreWith(cmd, proj, nil, cands)
	if len(got) != 1 {
		t.Fatalf("want 1 scored, got %d", len(got))
	}
	if got[0].ProjectedPL != 1000 || got[0].TourAlignment != 1.0 || got[0].Score != 1000 {
		t.Errorf("base score = %+v, want PL 1000, factor 1.0, score 1000", got[0])
	}
}

// The launch-guard veto (Proceed=false) drops the candidate at zero cost — never scored.
func TestScore_GuardVetoExcluded(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	cands := []SitingCandidate{
		{Good: "ELEC", System: "X1-AA", InputMarkets: []string{"M1"}},
		{Good: "MACH", System: "X1-AA", InputMarkets: []string{"M2"}},
	}
	proj := plProj(map[string]ChainProjection{
		"ELEC@X1-AA": {ProjectedPL: 1000, Proceed: true},
		"MACH@X1-AA": {ProjectedPL: 5000, Proceed: false, Reason: "negative_chain_margin"}, // vetoed
	})
	got := scoreWith(cmd, proj, nil, cands)
	if len(got) != 1 || got[0].Good != "ELEC" {
		t.Fatalf("vetoed candidate must be excluded; got %+v", got)
	}
}

// A candidate the projector cannot price is excluded (fail-closed).
func TestScore_ProjectorErrorExcluded(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	cands := []SitingCandidate{{Good: "ELEC", System: "X1-AA", InputMarkets: []string{"M1"}}}
	got := scoreWith(cmd, &fakeChainProjector{err: errors.New("unpriceable")}, nil, cands)
	if len(got) != 0 {
		t.Errorf("unpriceable candidate must be excluded; got %+v", got)
	}
}

// Alignment boosts the score: factor = 1 + WeightTourAlignment × signal.
func TestScore_AlignmentBoost(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, WeightTourAlignment: 1.0}
	cands := []SitingCandidate{{Good: "ELEC", System: "X1-AA", InputMarkets: []string{"M1"}}}
	proj := plProj(map[string]ChainProjection{"ELEC@X1-AA": {ProjectedPL: 1000, Proceed: true}})
	align := &fakeAlignmentProvider{byKey: map[string]float64{"ELEC@X1-AA": 2.0}}

	got := scoreWith(cmd, proj, align, cands)
	// factor = 1 + 1.0×2.0 = 3.0; score = 1000×3.0 = 3000.
	if got[0].TourAlignment != 3.0 || got[0].Score != 3000 {
		t.Errorf("alignment boost = %+v, want factor 3.0, score 3000", got[0])
	}
}

// An alignment read error does NOT drop the candidate — the signal falls back to 0 (neutral).
func TestScore_AlignmentErrorIsNeutralNotDrop(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, WeightTourAlignment: 1.0}
	cands := []SitingCandidate{{Good: "ELEC", System: "X1-AA", InputMarkets: []string{"M1"}}}
	proj := plProj(map[string]ChainProjection{"ELEC@X1-AA": {ProjectedPL: 1000, Proceed: true}})
	align := &fakeAlignmentProvider{err: errors.New("telemetry down")}

	got := scoreWith(cmd, proj, align, cands)
	if len(got) != 1 || got[0].TourAlignment != 1.0 || got[0].Score != 1000 {
		t.Errorf("alignment error must be neutral (factor 1.0), candidate kept; got %+v", got)
	}
}

// Input competition: two candidates sharing a feed market are penalized; a candidate with a
// private feed is not. Same PL, same freshness, weight 1.0.
func TestScore_InputCompetitionPenalty(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, WeightInputCompetition: 1.0}
	cands := []SitingCandidate{
		{Good: "A", System: "S", DataAgeSecs: 0, InputMarkets: []string{"SHARED"}},
		{Good: "B", System: "S", DataAgeSecs: 0, InputMarkets: []string{"SHARED"}},
		{Good: "C", System: "S", DataAgeSecs: 0, InputMarkets: []string{"PRIVATE"}},
	}
	proj := plProj(map[string]ChainProjection{
		"A@S": {ProjectedPL: 1000, Proceed: true},
		"B@S": {ProjectedPL: 1000, Proceed: true},
		"C@S": {ProjectedPL: 1000, Proceed: true},
	})
	got := scoreWith(cmd, proj, nil, cands)

	byGood := map[string]ScoredCandidate{}
	for _, s := range got {
		byGood[s.Good] = s
	}
	// A and B share SHARED → overlap 1.0 → competition = 1.0×1000×1.0 = 1000 → score 0.
	if byGood["A"].Competition != 1000 || byGood["A"].Score != 0 {
		t.Errorf("A competition=%v score=%v, want 1000/0", byGood["A"].Competition, byGood["A"].Score)
	}
	// C's PRIVATE feed is uncontended → no penalty → score 1000.
	if byGood["C"].Competition != 0 || byGood["C"].Score != 1000 {
		t.Errorf("C competition=%v score=%v, want 0/1000", byGood["C"].Competition, byGood["C"].Score)
	}
}

// Staleness: older data discounts the score. Same PL, private inputs, weight 1.0.
func TestScore_StalenessDiscount(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1, WeightStaleness: 1.0, FreshnessMaxSecs: 1000}
	cands := []SitingCandidate{
		{Good: "FRESH", System: "S", DataAgeSecs: 0, InputMarkets: []string{"M1"}},
		{Good: "STALE", System: "S", DataAgeSecs: 500, InputMarkets: []string{"M2"}}, // half the ceiling
	}
	proj := plProj(map[string]ChainProjection{
		"FRESH@S": {ProjectedPL: 1000, Proceed: true},
		"STALE@S": {ProjectedPL: 1000, Proceed: true},
	})
	got := scoreWith(cmd, proj, nil, cands)

	byGood := map[string]ScoredCandidate{}
	for _, s := range got {
		byGood[s.Good] = s
	}
	if byGood["FRESH"].Staleness != 0 || byGood["FRESH"].Score != 1000 {
		t.Errorf("FRESH staleness=%v score=%v, want 0/1000", byGood["FRESH"].Staleness, byGood["FRESH"].Score)
	}
	// ageFraction = 500/1000 = 0.5 → staleness = 1.0×1000×0.5 = 500 → score 500.
	if byGood["STALE"].Staleness != 500 || byGood["STALE"].Score != 500 {
		t.Errorf("STALE staleness=%v score=%v, want 500/500", byGood["STALE"].Staleness, byGood["STALE"].Score)
	}
}

// SCORE returns candidates ranked highest-first.
func TestScore_RankedDescending(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{PlayerID: 1}
	cands := []SitingCandidate{
		{Good: "LOW", System: "S", InputMarkets: []string{"M1"}},
		{Good: "HIGH", System: "S", InputMarkets: []string{"M2"}},
		{Good: "MID", System: "S", InputMarkets: []string{"M3"}},
	}
	proj := plProj(map[string]ChainProjection{
		"LOW@S":  {ProjectedPL: 100, Proceed: true},
		"HIGH@S": {ProjectedPL: 9000, Proceed: true},
		"MID@S":  {ProjectedPL: 3000, Proceed: true},
	})
	got := scoreWith(cmd, proj, nil, cands)
	if len(got) != 3 || got[0].Good != "HIGH" || got[1].Good != "MID" || got[2].Good != "LOW" {
		t.Errorf("want HIGH>MID>LOW, got %v/%v/%v", got[0].Good, got[1].Good, got[2].Good)
	}
}

// --- Helper unit tests ---

func TestOverlapFraction(t *testing.T) {
	contention := map[string]int{"SHARED": 2, "PRIVATE": 1}
	if f := overlapFraction([]string{"SHARED", "PRIVATE"}, contention); f != 0.5 {
		t.Errorf("overlap = %v, want 0.5", f)
	}
	if f := overlapFraction([]string{"PRIVATE"}, contention); f != 0 {
		t.Errorf("overlap = %v, want 0", f)
	}
	if f := overlapFraction(nil, contention); f != 0 {
		t.Errorf("empty overlap = %v, want 0", f)
	}
}

func TestAgeFraction(t *testing.T) {
	if f := ageFraction(0, 1000); f != 0 {
		t.Errorf("fresh = %v, want 0", f)
	}
	if f := ageFraction(500, 1000); f != 0.5 {
		t.Errorf("half = %v, want 0.5", f)
	}
	if f := ageFraction(5000, 1000); f != 1 {
		t.Errorf("beyond ceiling = %v, want clamped 1", f)
	}
}
