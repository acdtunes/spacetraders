package commands

import (
	"context"
	"errors"
	"testing"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The era-5 X1-KP23 geometry behind sp-5jce2, laid out on one axis so the
// distances are the measured ones exactly: contract cms41jtz0 sourced
// ASSAULT_RIFLES at E42 for delivery at J56.
const (
	placementSource      = "X1-KP23-E42" // source market, origin
	placementDestination = "X1-KP23-J56" // delivery — where a finished cycle leaves a hull
	placementNearA1      = "X1-KP23-A1"  // idle hull, 39.0 from source
	placementNearF46     = "X1-KP23-F46" // idle hull, 41.9 from source
	placementForeign     = "X1-ZZ99-B1"  // another system entirely
)

type placementStubGraphProvider struct {
	system.ISystemGraphProvider
	graph *system.NavigationGraph
	err   error
}

func (s *placementStubGraphProvider) GetGraph(_ context.Context, _ string, _ bool, _ int) (*system.GraphLoadResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &system.GraphLoadResult{Graph: s.graph, Source: "database"}, nil
}

func placementGraph(t *testing.T) *system.NavigationGraph {
	t.Helper()
	waypoints := map[string]*shared.Waypoint{}
	for symbol, x := range map[string]float64{
		placementSource:      0,
		placementNearA1:      39.0,
		placementNearF46:     41.9,
		placementDestination: 673.3,
	} {
		wp, err := shared.NewWaypoint(symbol, x, 0)
		if err != nil {
			t.Fatalf("NewWaypoint(%s): %v", symbol, err)
		}
		waypoints[symbol] = wp
	}
	return &system.NavigationGraph{Waypoints: waypoints}
}

// placementShip builds a hull parked at a named waypoint holding `units` of the
// contract good (0 = empty).
func placementShip(t *testing.T, symbol, waypointSymbol string, x float64, units int, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint(waypointSymbol, x, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(600, 600)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem("ASSAULT_RIFLES", "Assault Rifles", "", units)
		if err != nil {
			t.Fatalf("NewCargoItem: %v", err)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(80, units, inventory)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), location, fuel, 600, 80, cargo, 9,
		"FRAME_HAULER", "HAULER", nil, status)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// liveKP23Fleet is the measured fleet: the incumbent standing on the delivery
// with a partial load, and two idle empty hulls next to the source.
func liveKP23Fleet(t *testing.T, heldUnits int) []*navigation.Ship {
	t.Helper()
	return []*navigation.Ship{
		placementShip(t, "TORWIND-7", placementDestination, 673.3, heldUnits, navigation.NavStatusInOrbit),
		placementShip(t, "TORWIND-5", placementNearA1, 39.0, 0, navigation.NavStatusInOrbit),
		placementShip(t, "TORWIND-8", placementNearF46, 41.9, 0, navigation.NavStatusInOrbit),
	}
}

func placementHandler(t *testing.T, ships []*navigation.Ship) *RunFleetCoordinatorHandler {
	t.Helper()
	return &RunFleetCoordinatorHandler{
		shipRepo:      &singleHullFakeShipRepo{ships: ships},
		graphProvider: &placementStubGraphProvider{graph: placementGraph(t)},
	}
}

// KEY REGRESSION (sp-5jce2). The measured live pass: the coordinator's own log
// showed it choosing TORWIND-7 at 673.3 from the source "to complete its load"
// while TORWIND-5 (39.0) and TORWIND-8 (41.9) sat idle, fuelled and empty. The
// measurement must surface that gap, and the weighed rule must split the cycle
// instead of buying a ~1,346-unit round trip where ~712 would do.
func TestWeighHolderPlacement_LiveKP23Case_SplitsTheCycle(t *testing.T) {
	handler := placementHandler(t, liveKP23Fleet(t, 8))

	placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
	if err != nil {
		t.Fatalf("weighHolderPlacement: %v", err)
	}

	if placement.HeldUnits != 8 {
		t.Fatalf("expected the holder's 8 partial units measured, got %d", placement.HeldUnits)
	}
	if !placement.HolderAtDestination {
		t.Fatalf("the holder is standing on the delivery waypoint — that is what makes its load registerable at zero travel")
	}
	if diff := placement.HolderSourceDist - 673.3; diff > 0.05 || diff < -0.05 {
		t.Fatalf("expected the holder measured at 673.3 from the source, got %.2f", placement.HolderSourceDist)
	}
	if placement.NearestHull != "TORWIND-5" {
		t.Fatalf("expected the source-nearest candidate TORWIND-5 (39.0), got %q at %.2f", placement.NearestHull, placement.NearestSourceDist)
	}
	if diff := placement.NearestSourceDist - 39.0; diff > 0.05 || diff < -0.05 {
		t.Fatalf("expected the near hull measured at 39.0 from the source, got %.2f", placement.NearestSourceDist)
	}

	if decision := domainContract.WeighHolderAgainstSource(placement); !decision.DeliverHeldFirst {
		t.Fatalf("the measured live case MUST split the cycle (673.3 vs 39.0 on an 8-of-19 partial); got keep: %q", decision.Reason)
	}
}

// sp-zve2q PRESERVED: a holder whose load already covers the requirement keeps
// the run. Its worker computes zero units to purchase and delivers where it
// stands — no source trip at all — which is precisely the duplicate-sourcing
// defense the single-hull rule was written for. This is the case sp-zve2q was
// built to fix (idle TORWIND-15 holding the exact need while the closest empty
// hull double-sourced) and it must still resolve the same way.
func TestWeighHolderPlacement_FullHolderKeepsTheRun_ZveTwoQPreserved(t *testing.T) {
	handler := placementHandler(t, liveKP23Fleet(t, 19))

	placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
	if err != nil {
		t.Fatalf("weighHolderPlacement: %v", err)
	}

	if decision := domainContract.WeighHolderAgainstSource(placement); decision.DeliverHeldFirst {
		t.Fatalf("a holder already covering the requirement must keep the run — splitting it would re-source a duplicate onto an empty hull (sp-zve2q / sp-1pf0r regression)")
	}
}

// Fleet dedication is respected: only hulls the pass already qualified as
// spawnable candidates are ranked. A closer hull that is NOT in that pool (another
// fleet's, cargo-parked, or in spawn backoff) must be invisible here.
func TestWeighHolderPlacement_RanksOnlySpawnableCandidates(t *testing.T) {
	ships := append(liveKP23Fleet(t, 8),
		placementShip(t, "OTHERFLEET-1", placementSource, 0, 0, navigation.NavStatusInOrbit)) // sitting ON the source
	handler := placementHandler(t, ships)

	placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
	if err != nil {
		t.Fatalf("weighHolderPlacement: %v", err)
	}

	if placement.NearestHull == "OTHERFLEET-1" {
		t.Fatalf("ranked a hull outside the spawnable pool — contract fleet dedication must not be reached through")
	}
	if placement.NearestHull != "TORWIND-5" {
		t.Fatalf("expected TORWIND-5, got %q", placement.NearestHull)
	}
}

// A hull IN TRANSIT has a stale position — ranking it would compare against a
// coordinate it has already left.
func TestWeighHolderPlacement_SkipsInTransitCandidates(t *testing.T) {
	ships := []*navigation.Ship{
		placementShip(t, "TORWIND-7", placementDestination, 673.3, 8, navigation.NavStatusInOrbit),
		placementShip(t, "TORWIND-5", placementNearA1, 39.0, 0, navigation.NavStatusInTransit),
		placementShip(t, "TORWIND-8", placementNearF46, 41.9, 0, navigation.NavStatusInOrbit),
	}
	handler := placementHandler(t, ships)

	placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
	if err != nil {
		t.Fatalf("weighHolderPlacement: %v", err)
	}

	if placement.NearestHull != "TORWIND-8" {
		t.Fatalf("expected the in-transit TORWIND-5 skipped in favour of the parked TORWIND-8, got %q", placement.NearestHull)
	}
}

// Waypoint.DistanceTo is a plain Euclidean coordinate distance, so a hull in
// ANOTHER system would produce a meaningless (and here, deceptively small)
// number. It could not reach the source anyway (RULINGS #14 home locality).
func TestWeighHolderPlacement_IgnoresForeignSystemHulls(t *testing.T) {
	ships := append(liveKP23Fleet(t, 8),
		placementShip(t, "TORWIND-99", placementForeign, 1.0, 0, navigation.NavStatusInOrbit))
	handler := placementHandler(t, ships)

	placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8", "TORWIND-99"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
	if err != nil {
		t.Fatalf("weighHolderPlacement: %v", err)
	}

	if placement.NearestHull != "TORWIND-5" {
		t.Fatalf("a foreign-system hull was ranked by raw coordinates; expected TORWIND-5, got %q at %.2f", placement.NearestHull, placement.NearestSourceDist)
	}
}

// FAIL-CLOSED: any measurement failure (graph unavailable, no provider wired)
// must leave sp-zve2q's behaviour untouched rather than guess.
func TestWeighHolderPlacement_MeasurementFailure_KeepsTheHolder(t *testing.T) {
	t.Run("graph error", func(t *testing.T) {
		handler := &RunFleetCoordinatorHandler{
			shipRepo:      &singleHullFakeShipRepo{ships: liveKP23Fleet(t, 8)},
			graphProvider: &placementStubGraphProvider{err: errors.New("graph unavailable")},
		}
		if _, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1}); err == nil {
			t.Fatalf("expected the graph failure surfaced so the caller keeps the holder")
		}
	})

	t.Run("no graph provider wired", func(t *testing.T) {
		handler := &RunFleetCoordinatorHandler{shipRepo: &singleHullFakeShipRepo{ships: liveKP23Fleet(t, 8)}}
		placement, err := handler.weighHolderPlacement(context.Background(), holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1})
		if err != nil {
			t.Fatalf("a nil provider must degrade quietly, got: %v", err)
		}
		if decision := domainContract.WeighHolderAgainstSource(placement); decision.DeliverHeldFirst {
			t.Fatalf("with nothing measured the holder must keep the run")
		}
	})
}

// The one-shot guard: a hull gets exactly ONE zero-travel deliver-held run per
// contract. If it comes back still holding its load (an API refusal, or a restart
// that rebuilt the worker as an ordinary run), the next pass must run the FULL
// leg instead of re-dispatching the same no-op forever.
func TestDecideDeliverHeldFirst_OneShotPerContractAndHull(t *testing.T) {
	handler := placementHandler(t, liveKP23Fleet(t, 8))
	attempted := map[string]bool{}
	ctx := context.Background()

	first := handler.decideDeliverHeldFirst(ctx, "cms41jtz0", holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1}, attempted)
	if !first {
		t.Fatalf("the measured live case must split the cycle on the first pass")
	}

	second := handler.decideDeliverHeldFirst(ctx, "cms41jtz0", holderRun{Holder: "TORWIND-7", Candidates: []string{"TORWIND-5", "TORWIND-8"}, SourceWaypoint: placementSource, Destination: placementDestination, Good: "ASSAULT_RIFLES", UnitsNeeded: 19, PlayerID: 1}, attempted)
	if second {
		t.Fatalf("a second deliver-held dispatch for the same hull+contract would livelock — the full leg must run instead")
	}
}
