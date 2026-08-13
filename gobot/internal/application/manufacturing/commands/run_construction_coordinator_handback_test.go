package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// handedBackHull is an UNDEDICATED hull the drain worked a gate leg with and released seconds ago —
// the state its opportunistic crew sits in between legs. The general pool now HOLDS these hulls so
// no other coordinator takes the crew the drain is mid-campaign with; this file pins that the drain
// itself is exempt, because a hold that also hid the bench from the drain would starve it between
// every pair of legs and turn a poaching bug into a deadlock.
func handedBackHull(t *testing.T, symbol string, ago time.Duration) *navigation.Ship {
	t.Helper()
	ship := gateTestHull(t, symbol, "") // undedicated: the only hulls the hold can reach
	releasedAt := time.Now().UTC().Add(-ago)
	reason := gate.ConstructionReleaseReason
	ship.SetAssignment(navigation.ReconstructAssignment(
		"", navigation.AssignmentStatusIdle, releasedAt.Add(-time.Hour), &releasedAt, &reason,
		navigation.AssignmentOwnerContainer, nil,
	))
	return ship
}

func TestSelectHaulers_StillSeesTheHullsTheDrainItselfJustHandedBack(t *testing.T) {
	shipRepo := newDrainShipRepo(handedBackHull(t, "FREE-8", 20*time.Second))
	h := &RunConstructionCoordinatorHandler{shipRepo: shipRepo}

	ships, err := h.selectHaulers(context.Background(), gateTestCmd(), shared.MustNewPlayerID(1), gateTestSystem)
	if err != nil {
		t.Fatalf("selectHaulers returned error: %v", err)
	}

	for _, ship := range ships {
		if ship.ShipSymbol() == "FREE-8" {
			return
		}
	}
	t.Fatalf("the drain cannot see FREE-8, a hull it released seconds ago (got %d hull(s)) — the pool's gate hand-back hold is meant to reserve that hull FOR the drain, so applying it here starves the drain on its own crew", len(ships))
}

// The drain's release must keep writing the reason the pool reads. Both sides now reference one
// constant, so this pins the value against a silent edit at either end.
func TestReleaseClaim_StampsTheHandbackReasonThePoolReads(t *testing.T) {
	if gate.ConstructionReleaseReason != "construction_supply_complete" {
		t.Fatalf("the drain's hand-back reason changed to %q; every reader of the general pool keys on it, so a rename here silently stops them recognising the drain's hulls", gate.ConstructionReleaseReason)
	}
}
