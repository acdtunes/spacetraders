package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE SEAM sp-bvf20 ROUTES THROUGH, pinned from the POOL's side. Bootstrap's starved-trade fallback
// does exactly one thing: it clears the command frigate's "trade" tag. It adds no contract path of its
// own — it relies on this file's subject, FindIdleLightHaulers, being UNCHANGED, so these tests are
// written against the shipped function with no new option, no new argument and no new branch.
//
// Three facts make that one write both necessary and sufficient, and together they are also the
// RULINGS #7 proof: the frigate is admitted ONLY as a last resort, never ahead of a real hauler.

const starvedTradeFleetTag = "trade"

// tradeDedicated is the frigate as sp-tt3j4 leaves it: idle, capable, and pinned to the trade fleet.
func tradeDedicated(ship *navigation.Ship) *navigation.Ship {
	ship.SetDedicatedFleet(starvedTradeFleetTag)
	return ship
}

// FACT 1 (why the fallback is NEEDED). A trade-dedicated frigate is invisible to this pool, however
// starved trade is and however empty the contract fleet — the claim-filter drops every tagged hull
// before candidacy is even considered. Without the tag write there is no contract work for it, ever.
func TestFindIdleLightHaulers_TradeDedicatedCommandFrigateIsInvisible(t *testing.T) {
	frigate := tradeDedicated(newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0))
	repo := &stubShipRepo{ships: []*navigation.Ship{frigate}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("a trade-dedicated frigate must stay invisible to the general pool, got %v", symbols)
	}
}

// FACT 2 (why clearing the tag is SUFFICIENT). The same hull, same idle state, tag cleared: the
// last-resort admission takes it because no regular hauler is idle. Nothing else about the fleet
// changed, so the dedication write is the entire mechanism.
func TestFindIdleLightHaulers_UntaggedCommandFrigateIsAdmittedLastResort(t *testing.T) {
	frigate := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0)
	if frigate.DedicatedFleet() != "" {
		t.Fatal("fixture must be UNDEDICATED — that is the state bootstrap's fallback writes")
	}
	repo := &stubShipRepo{ships: []*navigation.Ship{frigate}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if !containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("with no hauler idle the untagged frigate is the last resort and must enter the pool, got %v", symbols)
	}
}

// FACT 3 (RULINGS #7 still holds). Releasing the frigate does NOT promote it: with a regular hauler
// idle the frigate is held back exactly as before, so the fallback can never turn the command hull
// into a routine contract worker — it only stops it idling when nothing else can do the job.
func TestFindIdleLightHaulers_UntaggedFrigateStillYieldsToAnIdleHauler(t *testing.T) {
	frigate := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0)
	hauler := newCandidateShip(t, "TORWIND-7", "HAULER", 80, 10, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{frigate, hauler}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("RULINGS #7: the frigate hauls only as a LAST resort — with %v idle it must stay out", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-7") {
		t.Fatalf("the real hauler must still be selected, got %v", symbols)
	}
}
