package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindFleetMemberSymbols is the live-membership primitive behind the sp-cmwc homing
// fix: membership is read from the persisted dedicated_fleet tag on every call, so a
// hull `fleet add`ed after launch is a member immediately and a `fleet remove`d hull
// drops out — never dependent on the immutable --dedicated-ships launch snapshot.

// A hull carrying the fleet tag is a member regardless of whether it was in any
// launch list; hulls in the general pool or tagged to a DIFFERENT fleet are not.
func TestFindFleetMemberSymbols_ReturnsOnlyLiveTaggedMembers(t *testing.T) {
	added := newCandidateShip(t, "TORWIND-9", "HAULER", 40, 0, 0) // live-added: tagged contract
	added.SetDedicatedFleet("contract")
	general := newCandidateShip(t, "TORWIND-2", "HAULER", 40, 0, 0) // general pool (untagged)
	other := newCandidateShip(t, "TORWIND-5", "HAULER", 40, 0, 0)   // tagged a different fleet
	other.SetDedicatedFleet("stocker")
	repo := &stubShipRepo{ships: []*navigation.Ship{added, general, other}}

	members, err := FindFleetMemberSymbols(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !containsSymbol(members, "TORWIND-9") {
		t.Fatalf("expected the live-tagged contract member TORWIND-9 in %v", members)
	}
	if containsSymbol(members, "TORWIND-2") {
		t.Fatalf("a general-pool hull must not be a contract member, got %v", members)
	}
	if containsSymbol(members, "TORWIND-5") {
		t.Fatalf("a hull tagged to a different fleet must not be a contract member, got %v", members)
	}
	if len(members) != 1 {
		t.Fatalf("expected exactly one contract member, got %v", members)
	}
}

// The "" fleet has no membership of its own (empty tag means general pool), matching
// FindIdleShipsByFleet / FleetHasMembers.
func TestFindFleetMemberSymbols_EmptyFleetReturnsNothing(t *testing.T) {
	tagged := newCandidateShip(t, "TORWIND-9", "HAULER", 40, 0, 0)
	tagged.SetDedicatedFleet("contract")
	repo := &stubShipRepo{ships: []*navigation.Ship{tagged}}

	members, err := FindFleetMemberSymbols(context.Background(), shared.MustNewPlayerID(1), repo, "")
	if err != nil {
		t.Fatalf("expected no error for the empty fleet, got: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("expected no members for the empty fleet, got %v", members)
	}
}
