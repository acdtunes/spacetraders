package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// deliverHeldFakeMediator drives the delivery leg and counts the two calls that
// distinguish the modes: a PURCHASE (the source round trip sp-5jce2 exists to
// avoid) and a DELIVER. The purchase deliberately FAILS so the ordinary control
// run terminates after exactly one attempt instead of looping — the assertion is
// about whether sourcing is attempted at all, not about how it ends.
type deliverHeldFakeMediator struct {
	common.Mediator

	navShip   *navigation.Ship
	delivered *domainContract.Contract

	purchaseCalls int
	deliverCalls  int
}

func (m *deliverHeldFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		return nil, errors.New("stub: source purchase attempted")
	case *DeliverContractCommand:
		m.deliverCalls++
		return &DeliverContractResponse{Contract: m.delivered, UnitsDelivered: 8}, nil
	default:
		return nil, fmt.Errorf("unexpected mediator command in deliver-held test: %T", request)
	}
}

// partialContract builds the live shape: 19 units required, `fulfilled` already
// registered, delivered at the waypoint the hull is standing on.
func partialContract(t *testing.T, good string, required, fulfilled int) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment:    domainContract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []domainContract.Delivery{{TradeSymbol: good, DestinationSymbol: "X1-UM5-K83", UnitsRequired: required, UnitsFulfilled: fulfilled}},
		Deadline:   "2999-01-01T00:00:00Z",
	}
	c, err := domainContract.NewContract("C-5JCE2", shared.MustNewPlayerID(4), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return c
}

func deliverHeldHarness(t *testing.T) (*DeliveryExecutor, *deliverHeldFakeMediator, domainContract.Delivery, *domainContract.Contract) {
	t.Helper()
	// The hull stands ON the delivery waypoint (X1-UM5-K83) holding 8 of the 19
	// needed — the exact partial-holder-at-destination shape sp-5jce2 measured.
	ship := buildDockedShipWithGood(t, "ASSAULT_RIFLES", 8, 80)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &deliverHeldFakeMediator{navShip: ship, delivered: partialContract(t, "ASSAULT_RIFLES", 19, 8)}
	executor := NewDeliveryExecutor(mediator, shipRepo, NewCargoManager(mediator, shipRepo))

	delivery := domainContract.Delivery{
		TradeSymbol:       "ASSAULT_RIFLES",
		DestinationSymbol: "X1-UM5-K83",
		UnitsRequired:     19,
		UnitsFulfilled:    0,
	}
	return executor, mediator, delivery, partialContract(t, "ASSAULT_RIFLES", 19, 0)
}

func deliverHeldProfitability() *contractQueries.ProfitabilityResult {
	return &contractQueries.ProfitabilityResult{
		PurchaseCost:           50000,
		CheapestMarketWaypoint: "X1-UM5-E42",
		MarketPrices:           map[string]int{"ASSAULT_RIFLES": 2500},
	}
}

// KEY REGRESSION (sp-5jce2). DELIVER-HELD mode is what makes the cycle split
// safe: the badly-placed partial holder registers the units it is standing on at
// ZERO travel and STOPS, so the well-placed hull sources the remainder on the
// next coordinator pass. If this run sourced instead, it would fly the very
// round trip the split exists to avoid (measured: 1,346 units where 712 would
// do) — and if it did not deliver, the held load would be stranded for a
// duplicate buy, regressing the sp-1pf0r double-load defense.
func TestProcessSingleDelivery_DeliverHeldOnly_RegistersHeldLoadAndSkipsSourcing(t *testing.T) {
	executor, mediator, delivery, contract := deliverHeldHarness(t)
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	out, err := executor.processSingleDelivery(ctx, "TORWIND-19", shared.MustNewPlayerID(4), contract, delivery,
		deliverHeldProfitability(), &RunWorkflowResponse{}, nil, true)

	if err != nil {
		t.Fatalf("a deliver-held run must return clean, got: %v", err)
	}
	if mediator.purchaseCalls != 0 {
		t.Fatalf("deliver-held must NOT source — that is the round trip sp-5jce2 avoids — but it attempted %d purchase(s)", mediator.purchaseCalls)
	}
	if mediator.deliverCalls != 1 {
		t.Fatalf("deliver-held must register the held load exactly once so it is never stranded, got %d deliver call(s)", mediator.deliverCalls)
	}
	if out == nil {
		t.Fatalf("expected the post-delivery contract back for the coordinator to re-read")
	}
}

// CONTROL: the same partial holder on an ORDINARY run still sources. This is what
// proves the assertion above is caused by the mode and not by the fixture (an
// 8-of-19 hull has a real remainder to buy), and it pins that the default leg is
// unchanged for every caller that did not ask for deliver-held.
func TestProcessSingleDelivery_OrdinaryRun_StillSourcesTheRemainder(t *testing.T) {
	executor, mediator, delivery, contract := deliverHeldHarness(t)
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	_, err := executor.processSingleDelivery(ctx, "TORWIND-19", shared.MustNewPlayerID(4), contract, delivery,
		deliverHeldProfitability(), &RunWorkflowResponse{}, nil, false)

	if err == nil {
		t.Fatalf("the control run's stub purchase fails, so an error is expected — a nil error means sourcing was never attempted")
	}
	if mediator.purchaseCalls == 0 {
		t.Fatalf("an ordinary run on a partial holder MUST still source the remainder; got no purchase attempt")
	}
}

// The exported entry point keeps its original signature and its original
// behaviour (ordinary leg) for every existing caller — deliver-held is opt-in
// through ProcessAllDeliveries only.
func TestProcessSingleDelivery_ExportedEntryPoint_IsAnOrdinaryRun(t *testing.T) {
	executor, mediator, delivery, contract := deliverHeldHarness(t)
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	_, _ = executor.ProcessSingleDelivery(ctx, "TORWIND-19", shared.MustNewPlayerID(4), contract, delivery,
		deliverHeldProfitability(), &RunWorkflowResponse{}, nil)

	if mediator.purchaseCalls == 0 {
		t.Fatalf("ProcessSingleDelivery must remain the ordinary source+deliver leg for existing callers")
	}
}

// An EMPTY hull dispatched in deliver-held mode has nothing to register: it must
// return immediately without sourcing and without spinning the delivery loop.
// This is the livelock guard at the executor — the coordinator's one-shot map is
// the guard at the dispatch side.
func TestProcessSingleDelivery_DeliverHeldOnly_EmptyHull_TerminatesWithoutSourcing(t *testing.T) {
	ship := buildDockedShipWithGood(t, "ASSAULT_RIFLES", 0, 80)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &deliverHeldFakeMediator{navShip: ship, delivered: partialContract(t, "ASSAULT_RIFLES", 19, 0)}
	executor := NewDeliveryExecutor(mediator, shipRepo, NewCargoManager(mediator, shipRepo))
	ctx := common.WithLogger(context.Background(), &capturingLogger{})

	delivery := domainContract.Delivery{
		TradeSymbol:       "ASSAULT_RIFLES",
		DestinationSymbol: "X1-UM5-K83",
		UnitsRequired:     19,
		UnitsFulfilled:    0,
	}

	done := make(chan error, 1)
	go func() {
		_, err := executor.processSingleDelivery(ctx, "TORWIND-19", shared.MustNewPlayerID(4),
			partialContract(t, "ASSAULT_RIFLES", 19, 0), delivery,
			deliverHeldProfitability(), &RunWorkflowResponse{}, nil, true)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an empty deliver-held run must return clean, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("deliver-held on an empty hull spun instead of returning — livelock")
	}

	if mediator.purchaseCalls != 0 {
		t.Fatalf("deliver-held must never source, got %d purchase(s)", mediator.purchaseCalls)
	}
}
