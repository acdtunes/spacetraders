package commands

import (
	"context"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vh1s Part A — the TERMINAL SWITCH (§5.1, the one swap). A gate fill IS a goods-factory run
// whose finished ROOT output is DELIVERED to the construction site instead of SOLD at a resale sink.
// produceNodeOnly's root terminal switches on IsUnifiedGateNode: gate mode → DeliverToConstructionSite;
// everything else (a profit factory, or the toggle off) → the unchanged planner-stock/sell path.

const gateTerminalSiteWP = "X1-TEST-GATE"

// gateTerminalConstructionRepo records SupplyMaterial calls at the driven-port boundary so a test can
// prove the root output was routed to the construction site. Embeds the domain interface so any
// unused method panics (keeping the fake honest about what the terminal actually calls).
type gateTerminalConstructionRepo struct {
	manufacturing.ConstructionSiteRepository
	calls []gateTerminalSupplyCall
}

type gateTerminalSupplyCall struct {
	site, good string
	units      int
}

func (r *gateTerminalConstructionRepo) SupplyMaterial(_ context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units, playerID int) (*manufacturing.ConstructionSupplyResult, error) {
	r.calls = append(r.calls, gateTerminalSupplyCall{site: waypointSymbol, good: tradeSymbol, units: units})
	return &manufacturing.ConstructionSupplyResult{UnitsDelivered: units}, nil
}

// SaveWithRetry models the single-writer CAS the delivery terminal's post-supply cargo write-back
// uses (sp-v5d1): load the stored ship, apply the mutation in place, persist. The stored *Ship is a
// shared pointer, so an in-place RemoveCargo persists for the next FindBySymbol.
func (r *factoryFakeShipRepo) SaveWithRetry(_ context.Context, symbol string, _ shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ship := r.ships[symbol]
	changed, err := mutate(ship)
	return ship, changed, err
}

// newGateTerminalFixture builds a factory coordinator over the shared fakes for a direct
// produceNodeOnly root-terminal test, with a construction repo wired and one docked fabrication hull
// (holding `outputUnits` of the harvested FAB_PLATE output) in the ship repo.
func newGateTerminalFixture(t *testing.T, hullSymbol string, outputUnits int) (*RunFactoryCoordinatorHandler, *factoryFakeMediator, *gateTerminalConstructionRepo, *navigation.Ship) {
	t.Helper()
	marketRepo := &factoryFakeMarketRepo{}
	mediator := &factoryFakeMediator{}
	resolver := mfgServices.NewSupplyChainResolver(map[string][]string{testOutputGood: {testInputGood}}, marketRepo)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)

	outputItem, err := shared.NewCargoItem(testOutputGood, testOutputGood, "", outputUnits)
	if err != nil {
		t.Fatalf("failed to build output cargo item: %v", err)
	}
	hull := newTestHauler(t, hullSymbol, []*shared.CargoItem{outputItem})
	shipRepo := &factoryFakeShipRepo{ships: map[string]*navigation.Ship{hull.ShipSymbol(): hull}}

	handler := NewRunFactoryCoordinatorHandler(mediator, shipRepo, marketRepo, resolver, marketLocator, &factoryFakeClock{}, nil)
	construction := &gateTerminalConstructionRepo{}
	handler.SetConstructionRepo(construction)
	return handler, mediator, construction, hull
}

// rootFabricateNode builds a root FAB_PLATE fabrication node whose sole IRON child is already
// COMPLETED — the precondition produceNodeOnly asserts before harvesting the root output.
func rootFabricateNode() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(testOutputGood, goods.AcquisitionFabricate)
	child := goods.NewSupplyChainNode(testInputGood, goods.AcquisitionBuy)
	child.MarkCompleted(10)
	root.AddChild(child)
	return root
}

// Acceptance core (sp-vh1s §5.1): with the toggle ON and a construction-site delivery target, the
// harvested root output is SUPPLIED TO THE GATE via DeliverToConstructionSite and NEVER sold at a
// resale sink.
func TestProduceNodeOnly_GateNode_DeliversRootOutputToConstructionSite_NotSink(t *testing.T) {
	handler, mediator, construction, hull := newGateTerminalFixture(t, "CRAFTY-GATE", 20)

	ctx := mfgServices.WithUnifiedGateFill(context.Background(), true)
	ctx = mfgServices.WithDeliveryTarget(ctx, mfgServices.ConstructionSiteTarget(gateTerminalSiteWP))

	_, err := handler.produceNodeOnly(ctx, hull, rootFabricateNode(), testSystem, 1, "", nil, false, true)
	if err != nil {
		t.Fatalf("gate-node root production must succeed, got %v", err)
	}

	if len(construction.calls) != 1 {
		t.Fatalf("expected exactly 1 delivery of the root output to the construction site, got %d", len(construction.calls))
	}
	call := construction.calls[0]
	if call.site != gateTerminalSiteWP || call.good != testOutputGood {
		t.Fatalf("root output delivered with wrong target: %+v (want site %s good %s)", call, gateTerminalSiteWP, testOutputGood)
	}
	if sold := mediator.soldUnitsOf(testOutputGood); sold != 0 {
		t.Fatalf("a gate fill must NEVER sell the root output at a resale sink, got %d units of %s sold", sold, testOutputGood)
	}
}

// OFF / profit-factory contract: with the toggle off (default), the SAME node harvests and SELLS the
// root output at its resale sink — the construction terminal is never touched (byte-identical to today).
func TestProduceNodeOnly_ToggleOff_SellsRootOutput_NoConstructionDelivery(t *testing.T) {
	handler, mediator, construction, hull := newGateTerminalFixture(t, "CRAFTY-SINK", 20)

	// No gate stamp: a plain profit-factory run.
	_, err := handler.produceNodeOnly(context.Background(), hull, rootFabricateNode(), testSystem, 1, "", nil, false, true)
	if err != nil {
		t.Fatalf("profit-factory root production must succeed, got %v", err)
	}

	if len(construction.calls) != 0 {
		t.Fatalf("the toggle-off path must NEVER deliver to a construction site, got %d delivery call(s)", len(construction.calls))
	}
	if sold := mediator.soldUnitsOf(testOutputGood); sold == 0 {
		t.Fatal("the toggle-off path must sell the harvested root output at its resale sink (byte-identical), got no sale")
	}
}
