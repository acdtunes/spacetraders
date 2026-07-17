package commands

// sp-f1yk Deliverable 5 — OR-Tools placement/relocation black-box acceptance tests.
//
// WAVE-1 / W4 — the fleet-median placement engine (sp-z7ng: score(x)=E_x - beta*D_x with the
// park floor phi = 0.3 * fleet-median, sourced from TourTelemetryRepository) is MERGED. The two
// behavioral scenarios below are un-skipped and driven against the REAL engine.
//
// SEAM CHECK (z7ng put the engine in run_tour_coordinator_placement.go — NOT
// run_tour_coordinator_reposition.go where the W1 scaffold guessed; corrected in W4):
//
//	grep -nE "maybeRepositionPlacement|placement\.Decide|placement\.Score|senseBeta|MedianTourRate|resolvePlacementParkFloorPct" run_tour_coordinator_placement.go
//
// proves the WHOLE seam on one file: the armed entry (maybeRepositionPlacement, dispatched from
// maybeReposition on margins-death when PlacementScoreEnabled), score(x)=E_x-beta*D_x with the
// phi=0.3 park floor (placement.Score / placement.Decide / resolvePlacementParkFloorPct), and
// beta sourced as the fleet rolling-median tour $/hr from the telemetry repo (senseBeta ->
// h.telemetry.ListByPlayer -> trading.MedianTourRate).
//
// OWNERSHIP (resolves the layer/duplication finding): z7ng owns the WHITE-BOX tests of score(x)
// and the phi park-floor math (run_tour_coordinator_placement_z7ng_test.go asserts the decision
// LOG internals — beta=, hops=, ex=, d=, score=, jump/hold verdict, persist ordering). f1yk owns
// ONLY these two BLACK-BOX acceptance scenarios through the Handle port, asserting on OUTCOME
// (relocation committed to the rich pocket vs park held on the current ground) and the driven
// jump/persist side effects — NEVER on internal score values or the decision log.
//
// W4 WIRING (z7ng's real harness):
//   - E_x           : tourFakeRoutingClient{planFn} returns a per-system ProjectedCreditsPerHour
//                     (feasiblePlan/placementPlan); ship.Cargo == nil marks the clean-hold E_x
//                     pre-flight vs the laden productive re-plan.
//   - fleet-median  : seededTelemetry{rows: betaSeedRows(rate)} — ListByPlayer returns rows that
//     (beta, phi)     MedianTourRate folds into beta; phi=0.3 default lifts the park floor phi*beta.
//   - entry         : h := newTourHandler(t, fx, planner, tel); h.Handle(ctx, cmd) with
//                     PlacementScoreEnabled; assert r.Repositions, fx.jumps, fx.location, and
//                     fakeRepositionPersister.recorded() (the driven persistence boundary).

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RED#12 — TestPlacementRelocatesOnExhaustedPocket: an exhausted home pocket (E_s infeasible even
// clean-hold) beside a rich reachable pocket X1-S2 (E_x = 200k) under a moderate fleet beta (150k
// => phi*beta = 45k, far below the charged X1-S2 score) must COMMIT a relocation to the rich
// pocket. Black-box outcome only: exactly one reposition, the jump targets X1-S2, the hull ends at
// X1-S2, and the in-flight destination is persisted to X1-S2 at the driven persistence boundary.
func TestPlacementRelocatesOnExhaustedPocket(t *testing.T) {
	fx := repositionFixture() // X1-S1 home, X1-S2 foreign (1-hop reachable neighbor)
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: the home pocket is exhausted even clean-hold
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive tour, then margins die -> reposition episode
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 { // the E_x pre-flight, then the post-jump productive re-plan
				return placementPlan("X1-S2", 200000, 200000) // the rich reachable pocket
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	persister := &fakeRepositionPersister{}
	h.SetRepositionPersister(persister)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RELOCATE", PlayerID: 1, ContainerID: "ctr-relocate", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("relocate run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	// OUTCOME 1: the relocation is COMMITTED (exactly one reposition — never a thrash).
	if r.Repositions != 1 {
		t.Fatalf("an exhausted pocket beside a rich reachable pocket must relocate exactly once, got %d (%+v)", r.Repositions, r)
	}
	// OUTCOME 2: the hull relocated TO the rich pocket (driven jump port).
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the relocation must target the rich pocket X1-S2, got %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the rich pocket X1-S2, got %q", fx.location)
	}
	// OUTCOME 3: the in-flight destination was committed to the rich pocket at the persistence
	// boundary (a driven-port side effect, not an internal score — Mandate 1).
	committed := false
	for _, ep := range persister.recorded() {
		if ep.InProgress && ep.TargetSystem == "X1-S2" {
			committed = true
		}
	}
	if !committed {
		t.Fatalf("the relocation must persist an in-flight destination targeting X1-S2, got %+v", persister.recorded())
	}
}

// RED#13 — TestPlacementParksOnGloballySaturatedFleet: when the fleet is globally saturated (a hot
// beta = 1M/hr lifts the park floor phi*beta = 300k above every reachable pocket's charged score,
// the current ground included), the hull must PARK — hold its ground rather than chase a marginal
// jump. Black-box outcome only: no reposition, no jump, the hull holds X1-S1, honest exit.
func TestPlacementParksOnGloballySaturatedFleet(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return feasiblePlan(50000, 50000) // E_s: feasible but under the hot-fleet park floor
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // productive, then margins die -> reposition episode
			}
			return infeasibleTour()
		case "X1-S2":
			return feasiblePlan(100000, 100000) // reachable, but its charged score < phi*beta under a hot beta
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(1000000)})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PARK", PlayerID: 1, ContainerID: "ctr-park", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("park run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	// OUTCOME 1: nothing jumps — the hull parks below the phi*beta floor.
	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a globally saturated fleet must PARK (no jump), got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	// OUTCOME 2: the hull held its current ground.
	if shared.ExtractSystemSymbol(fx.location) != "X1-S1" {
		t.Fatalf("a parked hull must hold its current ground X1-S1, got %q", fx.location)
	}
	// OUTCOME 3: the park is an honest starvation exit, not a hang.
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("a park flows to the honest starvation exit, got %q", r.ExitReason)
	}
}
