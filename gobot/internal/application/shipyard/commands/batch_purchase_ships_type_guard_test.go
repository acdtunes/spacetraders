package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the money-integrity floor for sp-e7je: a batch purchase must
// NEVER silently accept a ship the yard substituted for the requested type.
// The production incident asked for 3x SHIP_LIGHT_HAULER against a yard that
// sells only PROBE + SIPHON_DRONE and the batch loop happily counted 3
// wrong-typed ships (~105k credits) with zero error. The batch handler is the
// money-integrity boundary: it must verify every purchased ship matches the
// requested type and abort loudly on the first substitution instead of
// accumulating wrong assets.

const (
	typeGuardRequestedType = "SHIP_LIGHT_HAULER"
	typeGuardSubstituted   = "SHIP_SIPHON_DRONE" // what the buyer's yard actually stocks
	typeGuardPinnedYard    = "X1-GZ7-C37"
	typeGuardShipPrice     = 35000
)

// typeGuardFakeMediator stands in for the per-ship PurchaseShipCommand dispatch.
// It embeds common.Mediator so any request other than a PurchaseShipCommand
// nil-panics, keeping the fake honest about what executePurchaseLoop dispatches.
// respShipType models the type the yard actually delivered (which may differ
// from the requested type — the substitution the guard must catch).
type typeGuardFakeMediator struct {
	common.Mediator

	respShipType string
	price        int
	credits      int
	sends        int
}

func (m *typeGuardFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*PurchaseShipCommand); !ok {
		return nil, nil
	}
	m.sends++
	return &PurchaseShipResponse{
		Ship:          nil, // pinned-yard path never dereferences Ship
		PurchasePrice: m.price,
		AgentCredits:  m.credits,
		ShipType:      m.respShipType,
	}, nil
}

func typeGuardCommand() *BatchPurchaseShipsCommand {
	return &BatchPurchaseShipsCommand{
		PurchasingShipSymbol: "TORWIND-9",
		ShipType:             typeGuardRequestedType,
		Quantity:             3,
		MaxBudget:            0,
		PlayerID:             shared.MustNewPlayerID(1),
		ShipyardWaypoint:     typeGuardPinnedYard,
	}
}

// The bug: when the yard delivers a substitute type, the batch loop must NOT
// count it as a fulfilled purchase. It must abort with ZERO ships and a loud
// error, and it must stop immediately — never dispatching further purchases
// (no partial spend beyond the first anomaly).
func TestBatchPurchase_SubstitutedType_AbortsWithZeroPurchases(t *testing.T) {
	med := &typeGuardFakeMediator{
		respShipType: typeGuardSubstituted,
		price:        typeGuardShipPrice,
		credits:      500000,
	}
	handler := &BatchPurchaseShipsHandler{mediator: med}
	cmd := typeGuardCommand()

	ships, totalSpent, err := handler.executePurchaseLoop(
		context.Background(), cmd, cmd.Quantity, typeGuardPinnedYard, typeGuardShipPrice,
	)

	if err == nil {
		t.Fatalf("expected a loud money-integrity error when the yard delivers %s instead of %s, got nil",
			typeGuardSubstituted, typeGuardRequestedType)
	}
	if len(ships) != 0 {
		t.Fatalf("expected ZERO ships accepted on a type substitution, got %d", len(ships))
	}
	if totalSpent != 0 {
		t.Fatalf("expected ZERO spend reported on the substitution abort, got %d", totalSpent)
	}
	if med.sends != 1 {
		t.Fatalf("expected the loop to abort after the FIRST substitution (1 dispatch), got %d — it kept buying wrong ships", med.sends)
	}
}

// No regression: when the yard delivers exactly the requested type, the batch
// loop fulfills the full requested quantity as before.
func TestBatchPurchase_RequestedType_PurchasesFullQuantity(t *testing.T) {
	med := &typeGuardFakeMediator{
		respShipType: typeGuardRequestedType, // yard delivers what was asked for
		price:        typeGuardShipPrice,
		credits:      500000,
	}
	handler := &BatchPurchaseShipsHandler{mediator: med}
	cmd := typeGuardCommand()

	ships, totalSpent, err := handler.executePurchaseLoop(
		context.Background(), cmd, cmd.Quantity, typeGuardPinnedYard, typeGuardShipPrice,
	)

	if err != nil {
		t.Fatalf("matching type must not error, got: %v", err)
	}
	if len(ships) != cmd.Quantity {
		t.Fatalf("expected %d ships purchased for a matching type, got %d", cmd.Quantity, len(ships))
	}
	if totalSpent != typeGuardShipPrice*cmd.Quantity {
		t.Fatalf("expected total spend %d, got %d", typeGuardShipPrice*cmd.Quantity, totalSpent)
	}
}
