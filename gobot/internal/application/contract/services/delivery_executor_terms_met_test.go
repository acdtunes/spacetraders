package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// termsMetFakeMediator drives the delivery leg for an over-fetched hull whose
// contract terms the server already considers MET. The deliver command returns a
// reconciled terminal-success DeliverContractResponse (the deliver handler's
// sp-1pf0r 4509 reconciliation, tested separately at the command port), so this
// test proves the delivery-executor half: it must NOT retry the delivery and
// must NOT re-source — it accepts the reconciled contract and returns clean.
type termsMetFakeMediator struct {
	common.Mediator

	navShip    *navigation.Ship
	reconciled *domainContract.Contract

	deliverCalls  int
	purchaseCalls int
}

func (m *termsMetFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		return &shipCargo.PurchaseCargoResponse{}, nil
	case *DeliverContractCommand:
		m.deliverCalls++
		return &DeliverContractResponse{Contract: m.reconciled, UnitsDelivered: 64}, nil
	default:
		return nil, fmt.Errorf("unexpected mediator command in terms-met test: %T", request)
	}
}

func buildDockedShipWithGood(t *testing.T, good string, units, capacity int) *navigation.Ship {
	t.Helper()
	waypoint, err := shared.NewWaypoint("X1-UM5-K83", 1, 1)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem(good, good, "goods", units)
		if err != nil {
			t.Fatalf("cargo item: %v", err)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(capacity, units, inventory)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-19", shared.MustNewPlayerID(4), waypoint, fuel, 100, capacity, cargo, 30,
		"FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

func mustReconciledContract(t *testing.T, good string, required int) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment:    domainContract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []domainContract.Delivery{{TradeSymbol: good, DestinationSymbol: "X1-UM5-K83", UnitsRequired: required, UnitsFulfilled: required}},
		Deadline:   "2999-01-01T00:00:00Z",
	}
	c, err := domainContract.NewContract("C-1", shared.MustNewPlayerID(4), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return c
}

// End-to-end loop-break proof at the delivery-executor port: a hull over-fetched
// 64 FERTILIZER for a contract whose local row diverged at 0/64 while the server
// had it met (64/64). The deliver call reconciles to terminal success. The
// executor must exit cleanly (no error), deliver exactly ONCE (no 4509 retry
// loop), and re-read the reconciled contract as fulfilled — the behaviour that
// unwedges the fleet. It must also NOT re-source (the hull already holds the
// load).
func TestProcessSingleDelivery_TermsMet_ReturnsCleanNoRetryNoResource(t *testing.T) {
	ship := buildDockedShipWithGood(t, "FERTILIZER", 64, 80)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	reconciled := mustReconciledContract(t, "FERTILIZER", 64)
	mediator := &termsMetFakeMediator{navShip: ship, reconciled: reconciled}
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	// Local view diverged at 0/64; the hull holds the full over-fetched 64.
	delivery := domainContract.Delivery{
		TradeSymbol:       "FERTILIZER",
		DestinationSymbol: "X1-UM5-K83",
		UnitsRequired:     64,
		UnitsFulfilled:    0,
	}

	out, err := executor.ProcessSingleDelivery(ctx, "TORWIND-19", shared.MustNewPlayerID(4), reconciled, delivery, nil, &RunWorkflowResponse{}, nil)
	if err != nil {
		t.Fatalf("a terms-met delivery must return clean (no crash to retry), got: %v", err)
	}
	if mediator.deliverCalls != 1 {
		t.Fatalf("expected exactly one deliver attempt (no 4509 retry loop), got %d", mediator.deliverCalls)
	}
	if mediator.purchaseCalls != 0 {
		t.Fatalf("hull already holds the load; expected no re-sourcing purchase, got %d", mediator.purchaseCalls)
	}
	if out == nil {
		t.Fatalf("expected the reconciled contract back")
	}
	if d := out.Terms().Deliveries[0]; d.UnitsFulfilled < d.UnitsRequired {
		t.Fatalf("expected the delivery reconciled to fulfilled (%d/%d), so the workflow fulfils and the hull is released", d.UnitsFulfilled, d.UnitsRequired)
	}
}
