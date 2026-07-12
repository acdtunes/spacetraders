package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// TestPipelineStatusAdopted pins the executor-adoption classification to the REAL manufacturing
// pipeline enum (domain/manufacturing/pipeline.go): PLANNING -> EXECUTING -> {COMPLETED,FAILED,
// CANCELLED}. st-drm.19 BUG A: the old vocabulary (PENDING/CREATED/NEW) never matched, so a fresh
// PLANNING pipeline fell through `default` to "adopted" -> the coordinator SKIPPED the L57 adoption
// bounce -> construction.adopted never flipped. A fresh PLANNING pipeline MUST read as NOT adopted so
// the bounce runs; every status the executor has already engaged (EXECUTING + the terminal states)
// reads as adopted so a healthy/finished pipeline is never needlessly bounced.
func TestPipelineStatusAdopted(t *testing.T) {
	strPtr := func(s manufacturing.PipelineStatus) *string { v := string(s); return &v }

	cases := []struct {
		name   string
		status *string
		want   bool
	}{
		{"PLANNING is NOT adopted (fresh pipeline -> bounce)", strPtr(manufacturing.PipelineStatusPlanning), false},
		{"EXECUTING is adopted (executor working it)", strPtr(manufacturing.PipelineStatusExecuting), true},
		{"COMPLETED is adopted (executor already engaged it)", strPtr(manufacturing.PipelineStatusCompleted), true},
		{"FAILED is adopted (executor already engaged it)", strPtr(manufacturing.PipelineStatusFailed), true},
		{"CANCELLED is adopted (executor already engaged it)", strPtr(manufacturing.PipelineStatusCancelled), true},
		{"nil status is NOT adopted", nil, false},
		{"empty status is NOT adopted", func() *string { s := ""; return &s }(), false},
		{"unrecognized status is NOT adopted (fail-safe: allow the bounce)", func() *string { s := "WHATEVER"; return &s }(), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pipelineStatusAdopted(tc.status); got != tc.want {
				t.Fatalf("pipelineStatusAdopted(%v) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// TestSelectGateTarget pins the GATE-target selection that readBootstrapGateSnapshot maps onto the
// observation. st-drm.19 BUG B: a finished gate used to be `continue`d and dropped, so when the drive
// gate completed the snapshot went EMPTY and derivePhase regressed out of GATE instead of -> COMPLETE.
// The fix must still PREFER an under-construction gate (a prior era's built gate is not the target) yet
// surface a solely-complete scan as complete=true so obs.ConstructionComplete stays observable.
func TestSelectGateTarget(t *testing.T) {
	cases := []struct {
		name         string
		scans        []gateSiteScan
		wantSymbol   string
		wantComplete bool
		wantFound    bool
	}{
		{"no gate site at all -> not found (holds GATE fail-safe)", nil, "", false, false},
		{"single under-construction gate -> active target", []gateSiteScan{{"GATE-A", false}}, "GATE-A", false, true},
		// The regression case: the ONLY gate is built -> must be observed complete, not dropped.
		{"single built gate -> observed COMPLETE (BUG B regression)", []gateSiteScan{{"GATE-A", true}}, "GATE-A", true, true},
		// Prefer under-construction even when a built (prior-era) gate is scanned first.
		{"built gate then under-construction gate -> prefers the under-construction one", []gateSiteScan{{"GATE-BUILT", true}, {"GATE-LIVE", false}}, "GATE-LIVE", false, true},
		{"first under-construction gate wins over later ones", []gateSiteScan{{"GATE-A", false}, {"GATE-B", true}}, "GATE-A", false, true},
		{"all gates built -> first built gate observed COMPLETE", []gateSiteScan{{"GATE-A", true}, {"GATE-B", true}}, "GATE-A", true, true},
		{"empty-symbol scans are skipped", []gateSiteScan{{"", false}, {"GATE-A", true}}, "GATE-A", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSymbol, gotComplete, gotFound := selectGateTarget(tc.scans)
			if gotSymbol != tc.wantSymbol || gotComplete != tc.wantComplete || gotFound != tc.wantFound {
				t.Fatalf("selectGateTarget(%v) = (%q, %v, %v), want (%q, %v, %v)",
					tc.scans, gotSymbol, gotComplete, gotFound, tc.wantSymbol, tc.wantComplete, tc.wantFound)
			}
		})
	}
}
