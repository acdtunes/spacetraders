package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// A hull reclaimed from a crashed contract worker is force-released to IDLE
// (unassigned) while still physically holding its contract load. That load is
// DISPATCHABLE now (cargo-priority selection picks the holder), so the
// coordinator must be able to SEE it to complete it with that same hull instead
// of sourcing a duplicate onto a second hull — the sp-1pf0r double-load defense.
// calculateInFlightCargo deliberately excludes idle cargo from its WAIT total
// (waiting on an idle hull's load would stall), so this companion surfaces it
// separately.
func TestIdleReclaimedContractCargoHeld_CountsIdleHoldingHull(t *testing.T) {
	idle := newInFlightCargoTestShip(t, "TORWIND-19", 64, "")               // reclaimed → idle, holding its load
	onWorker := newInFlightCargoTestShip(t, "TORWIND-13", 30, "cw-running") // on a worker → NOT idle
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{idle, onWorker}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	got, err := handler.idleReclaimedContractCargoHeld(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("idleReclaimedContractCargoHeld: %v", err)
	}
	if got != 64 {
		t.Fatalf("expected only the IDLE hull's 64 units counted (the on-worker hull is excluded), got %d", got)
	}
}

// A hull assigned to any container (a live worker) must NOT be counted here: its
// cargo is either genuinely in-flight (calculateInFlightCargo's wait total) or
// will be reclaimed — counting it as idle-dispatchable would double-count and
// could mis-drive the dispatch decision.
func TestIdleReclaimedContractCargoHeld_ExcludesAssignedHulls(t *testing.T) {
	onWorker := newInFlightCargoTestShip(t, "TORWIND-13", 40, "cw-running")
	repo := &multiOrphanFakeShipRepo{ships: []*navigation.Ship{onWorker}}
	handler := &RunFleetCoordinatorHandler{shipRepo: repo}

	got, err := handler.idleReclaimedContractCargoHeld(context.Background(), "LIQUID_NITROGEN", 1)
	if err != nil {
		t.Fatalf("idleReclaimedContractCargoHeld: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected assigned (on-worker) hulls excluded from the idle count, got %d", got)
	}
}
