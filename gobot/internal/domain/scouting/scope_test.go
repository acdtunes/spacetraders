package scouting_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

func systemSet(systems ...string) map[string]bool {
	set := make(map[string]bool, len(systems))
	for _, s := range systems {
		set[s] = true
	}
	return set
}

func assertSet(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("set size = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("set %v missing %s", got, w)
		}
	}
}

func TestTradedFootprint_KeepsSystemsTradedInsideRetention(t *testing.T) {
	visits := []scouting.TradeVisit{
		{System: "X1-AA", AgeSeconds: 60},
		{System: "X1-BB", AgeSeconds: 3600},
	}
	assertSet(t, scouting.TradedFootprint(visits, 86400), "X1-AA", "X1-BB")
}

func TestTradedFootprint_DropsSystemsBeyondRetention(t *testing.T) {
	visits := []scouting.TradeVisit{
		{System: "X1-RECENT", AgeSeconds: 3600},
		{System: "X1-ANCIENT", AgeSeconds: 200000},
	}
	assertSet(t, scouting.TradedFootprint(visits, 86400), "X1-RECENT")
}

// A market the fleet crushed and stopped trading must stay in the footprint for the whole
// reversion window. Priors: bid bounce half-life ~1-1.5h, full reversion ~8-9h, a crushed lane
// dead 12-24h. A 24h retention must therefore still hold a system last traded 20h ago — that
// is exactly the market about to become the best sink again, and it can only be observed
// recovering if it is still being scanned.
func TestTradedFootprint_RetainsCrushedMarketThroughItsReversionWindow(t *testing.T) {
	visits := []scouting.TradeVisit{{System: "X1-CRUSHED", AgeSeconds: 20 * 3600}}
	assertSet(t, scouting.TradedFootprint(visits, 86400), "X1-CRUSHED")
}

func TestTradedFootprint_NonPositiveRetentionKeepsEveryVisit(t *testing.T) {
	visits := []scouting.TradeVisit{{System: "X1-OLD", AgeSeconds: 999999}}
	assertSet(t, scouting.TradedFootprint(visits, 0), "X1-OLD")
}

func TestTradedFootprint_ClockSkewNeverEvicts(t *testing.T) {
	visits := []scouting.TradeVisit{{System: "X1-SKEW", AgeSeconds: -500}}
	assertSet(t, scouting.TradedFootprint(visits, 3600), "X1-SKEW")
}

func TestTradedFootprint_SkipsEmptySystemAndEmptyInput(t *testing.T) {
	assertSet(t, scouting.TradedFootprint([]scouting.TradeVisit{{System: "", AgeSeconds: 1}}, 3600))
	assertSet(t, scouting.TradedFootprint(nil, 3600))
}

func TestSelectDiscovery_TakesRichestOutOfFootprintSystemsUpToAllowance(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{
		{System: "X1-POOR", Weight: 10},
		{System: "X1-RICH", Weight: 900},
		{System: "X1-MID", Weight: 500},
	}
	assertSet(t, scouting.SelectDiscovery(candidates, nil, 2), "X1-RICH", "X1-MID")
}

func TestSelectDiscovery_NeverSelectsAFootprintSystem(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{
		{System: "X1-TRADED", Weight: 9000},
		{System: "X1-OTHER", Weight: 5},
	}
	assertSet(t, scouting.SelectDiscovery(candidates, systemSet("X1-TRADED"), 2), "X1-OTHER")
}

// The allowance is BOUNDED and NON-ZERO: a large candidate pool yields exactly `allowance`
// slots, never more — that bound is the whole cost of keeping discovery alive.
func TestSelectDiscovery_IsBoundedByTheAllowance(t *testing.T) {
	var candidates []scouting.DiscoveryCandidate
	for _, s := range []string{"X1-A", "X1-B", "X1-C", "X1-D", "X1-E", "X1-F"} {
		candidates = append(candidates, scouting.DiscoveryCandidate{System: s, Weight: 100})
	}
	got := scouting.SelectDiscovery(candidates, nil, 3)
	if len(got) != 3 {
		t.Fatalf("selected %d discovery systems (%v), want exactly the allowance 3", len(got), got)
	}
}

func TestSelectDiscovery_NonPositiveAllowanceSelectsNothing(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{{System: "X1-RICH", Weight: 900}}
	assertSet(t, scouting.SelectDiscovery(candidates, nil, 0))
	assertSet(t, scouting.SelectDiscovery(candidates, nil, -1))
}

func TestSelectDiscovery_TieBreaksDeterministicallyBySystemSymbol(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{
		{System: "X1-ZZ", Weight: 100},
		{System: "X1-AA", Weight: 100},
	}
	for i := 0; i < 5; i++ {
		assertSet(t, scouting.SelectDiscovery(candidates, nil, 1), "X1-AA")
	}
}

func TestBuildScanScope_UnionsTradedAndOccupiedIntoTheFootprint(t *testing.T) {
	scope := scouting.BuildScanScope(systemSet("X1-TRADED"), systemSet("X1-OCCUPIED"), nil, 0)
	if !scope.Narrowed {
		t.Fatal("scope with a non-empty footprint must be narrowed")
	}
	assertSet(t, scope.Footprint, "X1-TRADED", "X1-OCCUPIED")
	if !scope.Includes("X1-TRADED") || !scope.Includes("X1-OCCUPIED") {
		t.Fatal("footprint systems must be included")
	}
	if scope.Includes("X1-ELSEWHERE") {
		t.Fatal("a system outside footprint and discovery must be excluded")
	}
}

// Cold start: no trades yet and no hull placed. The scope must NOT narrow — a fleet with no
// history would otherwise sense nothing and could never earn a footprint.
func TestBuildScanScope_EmptyFootprintIsNotNarrowed(t *testing.T) {
	scope := scouting.BuildScanScope(nil, nil, []scouting.DiscoveryCandidate{{System: "X1-RICH", Weight: 5}}, 4)
	if scope.Narrowed {
		t.Fatal("an empty footprint must yield an un-narrowed scope")
	}
	if !scope.Includes("X1-ANYTHING-AT-ALL") {
		t.Fatal("an un-narrowed scope must include every system")
	}
	if scope.IsDiscovery("X1-RICH") {
		t.Fatal("an un-narrowed scope has no discovery tier — everything is sized normally")
	}
}

func TestBuildScanScope_AddsDiscoveryTierOutsideTheFootprint(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{
		{System: "X1-RICH", Weight: 900},
		{System: "X1-POOR", Weight: 1},
	}
	scope := scouting.BuildScanScope(systemSet("X1-HOME"), nil, candidates, 1)
	if !scope.Includes("X1-RICH") {
		t.Fatal("the discovery slot must be sensed")
	}
	if !scope.IsDiscovery("X1-RICH") {
		t.Fatal("X1-RICH holds a discovery slot, not a footprint slot")
	}
	if scope.Includes("X1-POOR") {
		t.Fatal("a candidate beyond the allowance must be excluded")
	}
	if scope.IsDiscovery("X1-HOME") {
		t.Fatal("a footprint system is never discovery-tier")
	}
}

// A system that is BOTH a discovery candidate and in the footprint is a footprint system: it
// gets the full sizing model, not the cheap discovery watch.
func TestBuildScanScope_FootprintWinsOverDiscoveryForTheSameSystem(t *testing.T) {
	candidates := []scouting.DiscoveryCandidate{{System: "X1-BOTH", Weight: 900}}
	scope := scouting.BuildScanScope(systemSet("X1-BOTH"), nil, candidates, 4)
	if !scope.Includes("X1-BOTH") {
		t.Fatal("X1-BOTH must be sensed")
	}
	if scope.IsDiscovery("X1-BOTH") {
		t.Fatal("a footprint system must never be sized as discovery-tier")
	}
}

func TestScanScope_ZeroValueIsUnNarrowedAndIncludesEverything(t *testing.T) {
	var scope scouting.ScanScope
	if !scope.Includes("X1-ANY") {
		t.Fatal("the zero ScanScope must include every system (fail-open on scope)")
	}
	if scope.IsDiscovery("X1-ANY") {
		t.Fatal("the zero ScanScope has no discovery tier")
	}
}
