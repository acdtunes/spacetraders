package contract

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE DEFECT THIS FILE PINS. The construction drain works UNDEDICATED hulls on purpose — its
// opportunistic pool IS FindIdleLightHaulers — and it releases the claim between legs. So a hull
// mid-gate-campaign spends the gap between legs carrying no fleet tag and no assignment, which is
// exactly what a hull nobody is using looks like. The general pool then hands the drain's crew to
// the next asker one hull at a time, and the only thing that ever stopped it was an operator
// re-pinning those hulls by hand every era.
//
// The signal here is the release the DRAIN ITSELF wrote (gate.ConstructionReleaseReason +
// released_at, both daemon-written), so the pool classifies on what the hull was DOING rather than
// on whether anyone remembered to label it.

// poolTestNow is the instant every test in this file measures its hand-back window from, injected
// via PoolClock so the assertions never race the wall clock.
var poolTestNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func poolTestClock() PoolOption { return PoolClock{Clock: &shared.MockClock{CurrentTime: poolTestNow}} }

// releasedBy puts ship in the IDLE state an operation leaves behind when it hands a hull back:
// no container, the releasing operation's reason, and a released_at `ago` before poolTestNow.
func releasedBy(ship *navigation.Ship, reason string, ago time.Duration) *navigation.Ship {
	releasedAt := poolTestNow.Add(-ago)
	ship.SetAssignment(navigation.ReconstructAssignment(
		"",
		navigation.AssignmentStatusIdle,
		releasedAt.Add(-time.Hour),
		&releasedAt,
		&reason,
		navigation.AssignmentOwnerContainer,
		nil,
	))
	return ship
}

// ACCEPTANCE 1. An UNTAGGED hull the drain finished a gate leg with moments ago is gate crew, and
// the general pool must not hand it out. This is the poach that starved the gate drain.
func TestFindIdleLightHaulers_HoldsUndedicatedHullTheGateDrainJustHandedBack(t *testing.T) {
	gateCrew := releasedBy(newCandidateShip(t, "TORWIND-8", "HAULER", 80, 0, 0), gate.ConstructionReleaseReason, 20*time.Second)
	if gateCrew.DedicatedFleet() != "" {
		t.Fatal("fixture must be UNDEDICATED — a tagged hull is already excluded by the claim-filter and would prove nothing")
	}
	repo := &stubShipRepo{ships: []*navigation.Ship{gateCrew}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-8") {
		t.Fatalf("TORWIND-8 is between gate-construction legs and was offered to the general pool %v — the drain loses its crew one hull at a time and only a manual re-pin stops it", symbols)
	}
}

// ACCEPTANCE 2 (no regression). A hull with no gate-construction involvement is still a candidate:
// the hold must not quietly become "the contract pool is empty".
func TestFindIdleLightHaulers_UndedicatedHullWithNoGateWorkIsStillSelected(t *testing.T) {
	free := newCandidateShip(t, "TORWIND-3", "HAULER", 80, 0, 0)
	otherOp := releasedBy(newCandidateShip(t, "TORWIND-4", "HAULER", 80, 0, 0), "contract_delivery_complete", 20*time.Second)
	repo := &stubShipRepo{ships: []*navigation.Ship{free, otherOp}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("a never-worked idle hauler must stay a candidate, got %v", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-4") {
		t.Fatalf("a hull another operation handed back must stay a candidate — the hold is scoped to gate-construction work, got %v", symbols)
	}
}

// The hold LAPSES. It is a hand-back, not a second dedication: a drain that has stopped using a
// hull gives it up on its own, so no operator action is ever needed to free one.
func TestFindIdleLightHaulers_GateHandbackLapsesAfterTheWindow(t *testing.T) {
	longIdle := releasedBy(newCandidateShip(t, "TORWIND-8", "HAULER", 80, 0, 0), gate.ConstructionReleaseReason, GateHandbackWindow+time.Minute)
	repo := &stubShipRepo{ships: []*navigation.Ship{longIdle}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-8") {
		t.Fatalf("the hand-back window has lapsed and TORWIND-8 is still withheld from %v — a lapsing hold that never lapses is an ownership claim no operator can clear", symbols)
	}
}

// The DRAIN ITSELF must still see its own bench. The hold reserves those hulls FOR the drain;
// applying it to the drain's own opportunistic discovery would starve it between every pair of legs.
func TestFindIdleLightHaulers_ConstructionDrainStillSeesItsOwnHandback(t *testing.T) {
	gateCrew := releasedBy(newCandidateShip(t, "TORWIND-8", "HAULER", 80, 0, 0), gate.ConstructionReleaseReason, 20*time.Second)
	repo := &stubShipRepo{ships: []*navigation.Ship{gateCrew}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", ReleaseGateHandback, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-8") {
		t.Fatalf("the construction drain opted out of the hold and still cannot see its own hull in %v — it would starve on the crew it just used", symbols)
	}
}

// ACCEPTANCE 3 (regression pin). The DedicatedFleet-string exclusion is untouched: a tagged hull is
// still invisible to the general pool whatever its release history.
func TestFindIdleLightHaulers_DedicatedExclusionUnchangedByTheHandbackHold(t *testing.T) {
	tagged := newCandidateShip(t, "TORWIND-9", "HAULER", 80, 0, 0)
	tagged.SetDedicatedFleet("manufacturing")
	taggedAndHandedBack := releasedBy(newCandidateShip(t, "TORWIND-10", "HAULER", 80, 0, 0), gate.ConstructionReleaseReason, GateHandbackWindow+time.Hour)
	taggedAndHandedBack.SetDedicatedFleet("manufacturing")
	repo := &stubShipRepo{ships: []*navigation.Ship{tagged, taggedAndHandedBack}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if len(symbols) != 0 {
		t.Fatalf("dedicated hulls must stay invisible to the general pool regardless of the hand-back hold, got %v", symbols)
	}
}

// ACCEPTANCE 4 (RULINGS #7). The last-resort merge is untouched — it still runs on whatever hauler
// pool discovery produced. With the only hauler held on the gate bench there is no idle hauler, so
// the command frigate is admitted exactly as it is when no hauler exists at all: the hold never
// benches a usable frigate, and never promotes it while a genuinely free hauler is available.
func TestFindIdleLightHaulers_CommandLastResortUnchangedByTheHandbackHold(t *testing.T) {
	gateCrew := releasedBy(newCandidateShip(t, "TORWIND-8", "HAULER", 80, 0, 0), gate.ConstructionReleaseReason, 20*time.Second)
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 0, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{gateCrew, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if !containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("no idle hauler remains, so the command frigate is the last resort and must be admitted, got %v", symbols)
	}

	// ...and it is still held out the moment a genuinely free hauler exists.
	free := newCandidateShip(t, "TORWIND-3", "HAULER", 80, 700, 0)
	repo = &stubShipRepo{ships: []*navigation.Ship{gateCrew, free, command}}
	_, symbols, err = FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("an idle hauler is available, so the command frigate must stay benched (RULINGS #7), got %v", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("the free hauler must be the candidate, got %v", symbols)
	}
}

// A hull the drain is STILL hauling with is excluded by the assignment check, as before — the
// hand-back hold covers the gap either side of a claim, it does not replace the claim.
func TestFindIdleLightHaulers_ActivelyClaimedGateHullIsStillExcluded(t *testing.T) {
	working := newCandidateShip(t, "TORWIND-8", "HAULER", 80, 0, 0)
	if err := working.AssignToContainer("construction_coordinator-player-1", &shared.MockClock{CurrentTime: poolTestNow}); err != nil {
		t.Fatalf("assign to container: %v", err)
	}
	repo := &stubShipRepo{ships: []*navigation.Ship{working}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip, poolTestClock())
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if containsSymbol(symbols, "TORWIND-8") {
		t.Fatalf("a hull holding a live container claim must never be a candidate, got %v", symbols)
	}
}
