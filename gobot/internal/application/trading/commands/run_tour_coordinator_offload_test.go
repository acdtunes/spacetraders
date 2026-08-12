package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// offloadFixture is a 3-system world for the sp-2v69u held-cargo offload rescue: the hull
// starts LADEN (80/100 cargo of PARTS) on home X1-O1, a genuinely dead ground with no market
// at all. Neither jump-reachable neighbor offers a fresh-arb tour (the fake planner returns
// infeasible everywhere), so maybeReposition alone finds nothing worth the jump. X1-O2 IMPORTS
// PARTS at a rich bid/volume — the only reachable sink for the held load; X1-O3 has a market but
// never trades PARTS at all. Only a held-cargo-aware ranking can tell X1-O2 apart from X1-O3.
func offloadFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{"PARTS": 80}, location: "X1-O1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-O1": {"X1-O1-A"},
			"X1-O2": {"X1-O2-A"},
			"X1-O3": {"X1-O3-A"},
		},
		ask: map[string]map[string]int{
			"X1-O2-A": {"PARTS": 999},
			"X1-O3-A": {"WIDGETS": 50},
		},
		bid: map[string]map[string]int{
			"X1-O2-A": {"PARTS": 500},
		},
		tv: map[string]map[string]int{
			"X1-O2-A": {"PARTS": 1000},
			"X1-O3-A": {"WIDGETS": 1000},
		},
		tradeType: map[string]map[string]string{
			"X1-O2-A": {"PARTS": "IMPORT"},
		},
		neighbors: map[string][]string{"X1-O1": {"X1-O2", "X1-O3"}},
	}
}

// THE sp-2v69u unlock. A full heavy (80/100 cargo — LADEN) whose margins die with no fresh-arb
// candidate worth the jump (neither X1-O2 nor X1-O3 prices a profitable tour) must still be
// rescued: X1-O2 IMPORTS the PARTS the hull is stuck holding at a rich bid/volume, while X1-O3
// has no sink for it at all. The held-cargo offload pass ranks candidates by reachable sink
// absorption for the CURRENTLY HELD good (margin-EXEMPT — cash recovery, not fresh profit) and
// jumps to X1-O2, NOT X1-O3. Unguarded this hull churns forever: the fresh-arb pre-flight
// (planAtCandidate) clears cargo before ranking, so every candidate priced as a bare infeasible
// tour and the run parked on its own dead ground holding 80 unsellable PARTS.
func TestTour_MarginsDeath_LadenHullOffloadsHeldCargoTowardBestSink(t *testing.T) {
	fx := offloadFixture()
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		return infeasibleTour() // no fresh-arb tour anywhere in this world - only offload can rescue it
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-OFFLOAD", PlayerID: 1, ContainerID: "ctr-offload", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("held-cargo offload run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one held-cargo offload reposition, got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-O2" {
		t.Fatalf("expected the hull to jump to X1-O2 (the only reachable sink for its held PARTS), got %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-O2" {
		t.Fatalf("the hull must end at the sink system X1-O2, got %q", fx.location)
	}
	// This hull came up laden and has not traded since, so the offload is the PROMOTED rung: the
	// fresh-arb ranking never runs. X1-O3 is the witness — the hull never goes there, so the only
	// thing that could price it is the reconnaissance this hull no longer pays for.
	if plannerVisitedSystem(planner.positions, "X1-O3") {
		t.Fatalf("X1-O3 was pre-flighted, so the fresh-arb ranking still ran ahead of the held-cargo jump, positions=%v", planner.positions)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (the destination is also fresh-arb-dead, so the run still exits honestly after the rescue)", r.ExitReason, tourExitStarvation)
	}
}

// The fresh-arb margins-death rescue (maybeReposition) must win over the sp-2v69u held-cargo
// offload pass whenever it finds a floor-clearing candidate, REGARDLESS of whether the hull is
// laden: offload is a FALLBACK, only tried when maybeReposition itself returns
// repositioned=false. A laden hull (80/100 cargo of Z, a good with no sink anywhere in this
// world) still repositions via fresh arb to X1-S2 exactly like the unladen
// TestTour_MarginsDeath_RanksAndRepositionsToFreshGround case — byte-identical outcome — proving
// the offload pass never engages, let alone interferes, once a fresh plan exists.
func TestTour_MarginsDeath_LadenHullWithFreshPlan_UsesFreshArbNotOffload(t *testing.T) {
	fx := repositionFixture()
	fx.cargo = map[string]int{"Z": 80} // laden (80/100); the held good has no sink anywhere here
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LADEN-FRESH", PlayerID: 1, ContainerID: "ctr-laden-fresh", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("laden-with-fresh-plan run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition (fresh-arb, not offload), got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("a laden hull with a floor-clearing fresh-arb candidate must still jump there via the existing rescue, got %v", fx.jumps)
	}
	if r.ToursCompleted != 2 {
		t.Fatalf("expected 2 productive tours exactly as the unladen case, got %d", r.ToursCompleted)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
}

// heldCargoSinkValue is the pure ranking math behind the offload pass: for each held good it
// picks the candidate system's best non-EXPORT bid, caps the absorbed units at that listing's
// volume, and sums the recoverable cash across every held good. An EXPORT-typed listing (a
// sellback price, never a real sink — mirrors bestLaneForGood) contributes nothing even when it
// quotes a positive bid.
func TestHeldCargoSinkValue_SumsBestBidPerGoodCappedByVolumeExcludingExport(t *testing.T) {
	tests := []struct {
		name     string
		listings []trading.GoodListing
		held     map[string]int
		want     int64
	}{
		{
			name:     "import sink pays bid times held units under volume",
			listings: []trading.GoodListing{gl("PARTS", "X1-A", "IMPORT", 500, 999, 1000)},
			held:     map[string]int{"PARTS": 80},
			want:     80 * 500,
		},
		{
			name:     "absorption caps at the listing volume, not the held units",
			listings: []trading.GoodListing{gl("PARTS", "X1-A", "IMPORT", 500, 999, 30)},
			held:     map[string]int{"PARTS": 80},
			want:     30 * 500,
		},
		{
			name:     "an export listing's bid is a sellback, never a sink - contributes zero",
			listings: []trading.GoodListing{gl("PARTS", "X1-A", "EXPORT", 500, 999, 1000)},
			held:     map[string]int{"PARTS": 80},
			want:     0,
		},
		{
			name: "sums recoverable cash across every held good with a reachable sink",
			listings: []trading.GoodListing{
				gl("PARTS", "X1-A", "IMPORT", 500, 999, 1000),
				gl("GEAR", "X1-B", "EXCHANGE", 200, 250, 1000),
			},
			held: map[string]int{"PARTS": 80, "GEAR": 40, "UNSINKABLE": 10},
			want: 80*500 + 40*200,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := heldCargoSinkValue(tt.listings, tt.held)
			if got != tt.want {
				t.Fatalf("heldCargoSinkValue() = %d, want %d", got, tt.want)
			}
		})
	}
}

// isLadenForOffload gates the sp-2v69u rescue on the STUCK-LADEN CONDITION itself, per the
// Admiral's ruling: no separate on/off config flag — the condition IS the gate. Exactly 50% full
// is NOT laden (the bead's threshold is "cargo > 50% capacity", strictly greater); one unit past
// half, or a completely full hold, is.
func TestIsLadenForOffload_ThresholdBoundary(t *testing.T) {
	tests := []struct {
		name     string
		units    int
		capacity int
		want     bool
	}{
		{"empty hold is not laden", 0, 100, false},
		{"exactly half is not laden (strictly greater than 50%)", 50, 100, false},
		{"one unit past half is laden", 51, 100, true},
		{"a full hold is laden", 100, 100, true},
		{"zero capacity never divides by zero and is not laden", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLadenForOffload(tt.units, tt.capacity); got != tt.want {
				t.Fatalf("isLadenForOffload(%d, %d) = %v, want %v", tt.units, tt.capacity, got, tt.want)
			}
		})
	}
}
