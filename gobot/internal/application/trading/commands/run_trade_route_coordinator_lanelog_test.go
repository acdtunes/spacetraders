package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-q1ca: the lane-selection log line ("Selected top disciplined arbitrage
// lane") used to print no structured payload — the captain could not tell
// which lane a daemon picked, or whether cross-system lanes were even scanned,
// without inferring it from nav destinations. These tests pin the fix: the
// SELECTED lane's payload (good, both endpoints' waypoint+system, margin, and
// a cross-system flag) plus a shortlist of the top-ranked CANDIDATES actually
// considered, so a penalized-but-present cross-system lane is verifiable in
// the log rather than invisible.

// laneLogCapturingLogger records every Log call so tests can find and inspect
// the lane-selection entry's metadata payload.
type laneLogCapturingLogger struct {
	entries []laneLogEntry
}

type laneLogEntry struct {
	level    string
	message  string
	metadata map[string]interface{}
}

func (l *laneLogCapturingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.entries = append(l.entries, laneLogEntry{level: level, message: message, metadata: metadata})
}

// selectionEntry returns the single "Selected top disciplined arbitrage lane"
// entry, failing the test if it is missing or duplicated.
func (l *laneLogCapturingLogger) selectionEntry(t *testing.T) laneLogEntry {
	t.Helper()
	var found []laneLogEntry
	for _, e := range l.entries {
		if e.message == "Selected top disciplined arbitrage lane" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one lane-selection log entry, got %d: %+v", len(found), found)
	}
	return found[0]
}

// --- pure helper tests (laneLogPayload / laneLogCandidates) ---

func TestLaneLogPayload_CrossSystemLane_FlagsCrossSystemAndBothSystems(t *testing.T) {
	lane := trading.ArbitrageLane{
		Good:           "WIDGET",
		SourceWaypoint: "X1-HOME-A1",
		DestWaypoint:   "X1-NEAR-B2",
		SpreadPerUnit:  450,
		VolumeCap:      60,
		CappedSpread:   27000,
	}

	payload := laneLogPayload(lane)

	if payload["good"] != "WIDGET" {
		t.Fatalf("expected good=WIDGET, got %+v", payload["good"])
	}
	if payload["source"] != "X1-HOME-A1" || payload["source_system"] != "X1-HOME" {
		t.Fatalf("expected source=X1-HOME-A1 source_system=X1-HOME, got source=%v source_system=%v", payload["source"], payload["source_system"])
	}
	if payload["dest"] != "X1-NEAR-B2" || payload["dest_system"] != "X1-NEAR" {
		t.Fatalf("expected dest=X1-NEAR-B2 dest_system=X1-NEAR, got dest=%v dest_system=%v", payload["dest"], payload["dest_system"])
	}
	if payload["cross_system"] != true {
		t.Fatalf("expected cross_system=true for a lane spanning X1-HOME -> X1-NEAR, got %v", payload["cross_system"])
	}
	if payload["spread_per_u"] != 450 {
		t.Fatalf("expected spread_per_u=450, got %v", payload["spread_per_u"])
	}
}

func TestLaneLogPayload_SameSystemLane_CrossSystemFalse(t *testing.T) {
	lane := trading.ArbitrageLane{
		Good:           "WIDGET",
		SourceWaypoint: "X1-HOME-A1",
		DestWaypoint:   "X1-HOME-A2",
		SpreadPerUnit:  500,
		VolumeCap:      60,
		CappedSpread:   30000,
	}

	payload := laneLogPayload(lane)

	if payload["source_system"] != "X1-HOME" || payload["dest_system"] != "X1-HOME" {
		t.Fatalf("expected both endpoints in X1-HOME, got source_system=%v dest_system=%v", payload["source_system"], payload["dest_system"])
	}
	if payload["cross_system"] != false {
		t.Fatalf("expected cross_system=false for a same-system lane, got %v", payload["cross_system"])
	}
}

func TestLaneLogCandidates_LimitsToTopNInRankOrder(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "G1", SourceWaypoint: "X1-A-1", DestWaypoint: "X1-A-2"},
		{Good: "G2", SourceWaypoint: "X1-A-3", DestWaypoint: "X1-A-4"},
		{Good: "G3", SourceWaypoint: "X1-A-5", DestWaypoint: "X1-A-6"},
		{Good: "G4", SourceWaypoint: "X1-A-7", DestWaypoint: "X1-A-8"},
		{Good: "G5", SourceWaypoint: "X1-A-9", DestWaypoint: "X1-A-10"},
		{Good: "G6", SourceWaypoint: "X1-A-11", DestWaypoint: "X1-A-12"},
		{Good: "G7", SourceWaypoint: "X1-A-13", DestWaypoint: "X1-A-14"},
	}

	candidates := laneLogCandidates(lanes)

	if len(candidates) != laneCandidateLogLimit {
		t.Fatalf("expected candidates capped to %d, got %d", laneCandidateLogLimit, len(candidates))
	}
	for i, c := range candidates {
		want := lanes[i].Good
		if c["good"] != want {
			t.Fatalf("expected candidate %d to preserve rank order (good=%s), got %v", i, want, c["good"])
		}
	}
}

func TestLaneLogCandidates_FewerThanLimit_ReturnsAll(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "G1", SourceWaypoint: "X1-A-1", DestWaypoint: "X1-A-2"},
		{Good: "G2", SourceWaypoint: "X1-A-3", DestWaypoint: "X1-A-4"},
	}

	candidates := laneLogCandidates(lanes)

	if len(candidates) != 2 {
		t.Fatalf("expected all 2 lanes returned when under the limit, got %d", len(candidates))
	}
}

// --- integration test: the log line emitted by the running coordinator ---

// A cross-system lane (GOOD_B, X1-TR -> X1-TR2) is ranked but loses to a
// same-system lane (GOOD_A) once the gate penalty applies. Both lanes clear
// the discipline floor (trading.MinBidMargin=1000) on their own real numbers
// — GOOD_B is a legitimately disciplined lane that simply loses the RANKING
// to the penalty, not a sub-floor lane excluded outright — so this exercises
// the full execute() path (FirstDisciplinedLane must select something) rather
// than just the pure ranking helper. The captain must be able to see, from
// the SELECTED lane's own log line, that GOOD_A stayed within X1-TR
// (cross_system=false) AND, from the candidates shortlist, that GOOD_B was
// actually scanned and ranked (cross_system=true) rather than missing
// entirely — this is what makes cross-system scanning verifiable instead of
// inferred (sp-q1ca).
func TestTradeRouteCoordinator_SelectedLaneLog_CarriesPayloadAndCrossSystemCandidate(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-LOG")
	marketRepo := &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-TR":  {"X1-TR-A1", "X1-TR-A2", "X1-TR-B1"},
			"X1-TR2": {"X1-TR2-B2"},
		},
		goods: map[string]msGood{
			// GOOD_A: same-system, spread 1500 (1600-100) - clears the 1000 floor.
			"X1-TR-A1": {symbol: "GOOD_A", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-TR-A2": {symbol: "GOOD_A", bid: 1600, ask: 1650, volume: 60, tradeType: market.TradeTypeImport},
			// GOOD_B: cross-system, raw spread 1650 (1750-100) - ALSO clears the
			// 1000 floor on its own real numbers - but penalized to 1450 once the
			// 200/unit gate penalty applies (sp-wlev), losing the ranking to
			// GOOD_A's unpenalized 1500 (both lanes share VolumeCap=60, so the
			// penalty alone decides the ranking here).
			"X1-TR-B1":  {symbol: "GOOD_B", bid: 50, ask: 100, volume: 60, tradeType: market.TradeTypeExport},
			"X1-TR2-B2": {symbol: "GOOD_B", bid: 1750, ask: 1800, volume: 60, tradeType: market.TradeTypeImport},
		},
	}
	mediator := &msMediator{connections: map[string][]string{"X1-TR": {"X1-TR2"}}}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if _, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: "X1-TR",
		PlayerID:     1,
	}); err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}

	entry := logger.selectionEntry(t)
	meta := entry.metadata

	if meta["good"] != "GOOD_A" {
		t.Fatalf("expected GOOD_A to win the selection (1500 same-system beats GOOD_B's penalized 1450), got %v", meta["good"])
	}
	if meta["source"] != "X1-TR-A1" || meta["source_system"] != "X1-TR" {
		t.Fatalf("expected source=X1-TR-A1 source_system=X1-TR, got source=%v source_system=%v", meta["source"], meta["source_system"])
	}
	if meta["dest"] != "X1-TR-A2" || meta["dest_system"] != "X1-TR" {
		t.Fatalf("expected dest=X1-TR-A2 dest_system=X1-TR, got dest=%v dest_system=%v", meta["dest"], meta["dest_system"])
	}
	if meta["cross_system"] != false {
		t.Fatalf("expected the winning lane's cross_system=false (it never left X1-TR), got %v", meta["cross_system"])
	}
	if meta["spread_per_u"] != 1500 {
		t.Fatalf("expected spread_per_u=1500, got %v", meta["spread_per_u"])
	}

	candidates, ok := meta["candidates"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected candidates to be a []map[string]interface{}, got %T", meta["candidates"])
	}
	var sawCrossSystemCandidate bool
	for _, c := range candidates {
		if c["good"] == "GOOD_B" {
			if c["cross_system"] != true {
				t.Fatalf("expected GOOD_B candidate to be flagged cross_system=true, got %v", c["cross_system"])
			}
			sawCrossSystemCandidate = true
		}
	}
	if !sawCrossSystemCandidate {
		t.Fatalf("expected the penalized cross-system lane GOOD_B to still appear among the logged candidates, got %+v", candidates)
	}
}
