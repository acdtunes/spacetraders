package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newHomeLocalityTestShip builds an idle, in-orbit hauler at the given waypoint so a test can
// place contract-worker candidates in different systems and exercise the home-system locality
// scope (sp-ue1s). Its system is derived from the waypoint symbol exactly as production does
// (shared.ExtractSystemSymbol), mirroring newOrphanReclaimTestShip but parameterizing location.
func newHomeLocalityTestShip(t *testing.T, symbol, waypointSymbol string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		loc,
		fuel,
		100,
		80,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// The live sp-ue1s incident: the contract fleet coordinator's idle-hauler grab had NO
// home-system locality constraint, so it selected TORWIND-E — idle in the FOREIGN arb system
// X1-DF86, a gate hop from home X1-VB74 — as the worker for a COPPER_ORE -> X1-VB74-H51
// contract, stalling 80min reaching cross-gate. Contract sourcing+delivery is HOME-system
// only (zero-jump worker, RULINGS #14; the same scope PlanSourcing/market_finder use, derived
// from the delivery destination), so scopeCandidatesToContractHome MUST exclude the foreign
// hull from the general worker pool while keeping the home hull. Mutation guard: drop the
// FilterToHomeSystem call (return the pool unscoped) and the foreign hull re-enters — the exact
// live regression.
func TestScopeCandidatesToContractHome_ExcludesForeignSystemHull(t *testing.T) {
	homeHull := newHomeLocalityTestShip(t, "TORWIND-H", "X1-VB74-A1")    // idle in the home system
	foreignHull := newHomeLocalityTestShip(t, "TORWIND-E", "X1-DF86-B7") // idle in the foreign arb system
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{homeHull, foreignHull}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	// COPPER_ORE -> X1-VB74-H51: the contract's delivery destination sits in the home system.
	scoped, err := handler.scopeCandidatesToContractHome(
		context.Background(),
		shared.MustNewPlayerID(1),
		[]string{"TORWIND-H", "TORWIND-E"},
		"X1-VB74-H51",
		false, // no dedicated fleet: the GENERAL grab pool, where the live bug lives
	)
	if err != nil {
		t.Fatalf("scopeCandidatesToContractHome: %v", err)
	}

	for _, s := range scoped {
		if s == "TORWIND-E" {
			t.Fatalf("foreign-system hull TORWIND-E (X1-DF86) must NEVER be a contract worker for an X1-VB74 delivery, got %v", scoped)
		}
	}
	if len(scoped) != 1 || scoped[0] != "TORWIND-H" {
		t.Fatalf("expected only the home-system hull [TORWIND-H], got %v", scoped)
	}
}

// EXCLUSIVE MODE untouched (sp-wq7r composition): a dedicated contract fleet is the operator's
// explicit, sealed choice, and its "draw ONLY from dedicated members, even when empty" contract
// must not be silently narrowed by locality. Home-system scoping is the GENERAL grab pool's
// constraint (the sp-ue1s bug class — an undedicated hull the pool poached cross-gate), so with
// dedicatedFleetActive the pool passes through unscoped. This keeps the fix disjoint from the
// dedicated-fleet feature (zero regression) and composes with the sp-mzdk reserve floor, whose
// reserved hulls are UNDEDICATED + home and therefore ride the general (scoped) path where they
// stay eligible.
func TestScopeCandidatesToContractHome_ExclusiveModePassesThrough(t *testing.T) {
	foreignHull := newHomeLocalityTestShip(t, "TORWIND-E", "X1-DF86-B7")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{foreignHull}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	scoped, err := handler.scopeCandidatesToContractHome(
		context.Background(),
		shared.MustNewPlayerID(1),
		[]string{"TORWIND-E"},
		"X1-VB74-H51",
		true, // exclusive mode: a dedicated fleet is active
	)
	if err != nil {
		t.Fatalf("scopeCandidatesToContractHome: %v", err)
	}
	if len(scoped) != 1 || scoped[0] != "TORWIND-E" {
		t.Fatalf("exclusive-mode dedicated pool must pass through unscoped (operator's explicit choice), got %v", scoped)
	}
}
