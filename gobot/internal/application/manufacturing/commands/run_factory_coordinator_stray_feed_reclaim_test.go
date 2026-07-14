package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-b3ou: when a gate feed/supply run is interrupted (daemon restart, container stop,
// pipeline swap) a hull is left idle holding a gate-chain INTERMEDIATE — a non-root input
// good ANYWHERE in the active supply tree (EXPLOSIVES / MICROPROCESSORS / SILICON_CRYSTALS,
// the exact FAB/ADV feed inputs). filterUnrelatedCargo correctly refuses to SKIP such a hull
// (the good IS in the tree), but nothing then offloads the intermediate: assigned to some
// node's fresh BUY it only wedges (a full hold it can never free for the buy), so the units
// sit stranded — not fed, not sold, not freed — and the drain starves to ~1 usable hull.
//
// The fix RECLAIMS the hull and REDIRECTS the held intermediate to its consuming factory
// (delivers it as a feed, advancing the gate AND freeing the hull), while a hull holding the
// run's own ROOT output, or nothing, is left for normal production, and a good absent from the
// tree stays the skip/liquidation problem of filterUnrelatedCargo (unchanged). These tests pin
// the tree-membership map (buildFeedConsumerMap) and the reclaim+redirect step
// (reclaimStrayFeedCargo) and its gate-only scoping.

// deepFabTree builds root FAB_PLATE(fab) <- IRON(buy). buildFeedConsumerMap must map the input
// IRON to its consumer FAB_PLATE, and must NEVER key the root output (it is delivered to the
// gate/sink terminal, never fed into a parent).
func deepFabTree() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(testOutputGood, goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode(testInputGood, goods.AcquisitionBuy))
	return root
}

// (a) buildFeedConsumerMap maps every non-root INPUT good to the good of the node that
// consumes it, and excludes the root output entirely.
func TestBuildFeedConsumerMap_MapsInputsToConsumer_ExcludesRoot(t *testing.T) {
	consumers := buildFeedConsumerMap(deepFabTree())

	if got := consumers[testInputGood]; got != testOutputGood {
		t.Fatalf("expected input %s to map to its consuming factory good %s, got %q", testInputGood, testOutputGood, got)
	}
	if _, isKey := consumers[testOutputGood]; isKey {
		t.Fatalf("the root output %s must never be a feed-consumer key (it is delivered to the gate/sink, not fed), got %v", testOutputGood, consumers)
	}
}

// (a2) A deeper tree proves membership is "ANYWHERE in the tree": a grandchild input maps to
// its own parent (the mid-tree fabricate node), not the root.
func TestBuildFeedConsumerMap_GrandchildInput_MapsToItsOwnParent(t *testing.T) {
	root := goods.NewSupplyChainNode("ADVANCED_CIRCUITRY", goods.AcquisitionFabricate)
	micro := goods.NewSupplyChainNode("MICROPROCESSORS", goods.AcquisitionFabricate)
	micro.AddChild(goods.NewSupplyChainNode("SILICON_CRYSTALS", goods.AcquisitionBuy))
	root.AddChild(micro)

	consumers := buildFeedConsumerMap(root)

	if got := consumers["SILICON_CRYSTALS"]; got != "MICROPROCESSORS" {
		t.Fatalf("expected grandchild SILICON_CRYSTALS to map to its own parent MICROPROCESSORS, got %q", got)
	}
	if got := consumers["MICROPROCESSORS"]; got != "ADVANCED_CIRCUITRY" {
		t.Fatalf("expected MICROPROCESSORS to map to its consumer ADVANCED_CIRCUITRY, got %q", got)
	}
}

// reclaimFixture wires the standard FAB_PLATE <- IRON fakes and registers the given stray hulls
// in the ship repo so the redirect's navigate+deliver can run.
func reclaimFixture(t *testing.T, gate bool, strays ...*navigation.Ship) (*factoryFixture, *RunFactoryCoordinatorCommand) {
	t.Helper()
	f := newFactoryFixture(t)
	for _, s := range strays {
		f.shipRepo.ships[s.ShipSymbol()] = s
	}
	cmd := &RunFactoryCoordinatorCommand{
		PlayerID:        1,
		TargetGood:      testOutputGood,
		SystemSymbol:    testSystem,
		ContainerID:     testContainerID,
		UnifiedGateFill: gate,
	}
	return f, cmd
}

// (b) THE FIX: a gate hull holding a tree INTERMEDIATE (IRON, an input consumed by the
// FAB_PLATE factory) is RECLAIMED and its cargo DELIVERED to that consuming factory — not left
// in the production pool to wedge. The reclaimed hull is excluded from the returned production
// list (it fed the gate this pass) and its held good is SOLD (delivered as feed), never
// re-bought.
func TestReclaimStrayFeedCargo_GateHullHoldingIntermediate_DeliveredToConsumer_NotPooled(t *testing.T) {
	ironItem, err := shared.NewCargoItem(testInputGood, testInputGood, "", 20)
	if err != nil {
		t.Fatalf("cargo item: %v", err)
	}
	stray := newTestHauler(t, "TORWIND-F", []*shared.CargoItem{ironItem})
	f, cmd := reclaimFixture(t, true, stray)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	remaining := f.handler.reclaimStrayFeedCargo(ctx, cmd, deepFabTree(), []*navigation.Ship{stray})

	for _, s := range remaining {
		if s.ShipSymbol() == "TORWIND-F" {
			t.Fatalf("the reclaimed feed hull must be excluded from the production pool this pass, got it back in %v", remaining)
		}
	}
	if sold := f.mediator.soldUnitsOf(testInputGood); sold == 0 {
		t.Fatalf("the held intermediate %s must be DELIVERED (sold) to its consuming factory as a feed, got 0 units sold", testInputGood)
	}
	if bought := f.mediator.purchasedUnitsOf(testInputGood); bought != 0 {
		t.Fatalf("the reclaimed hull must free up by DELIVERING its held cargo, never re-BUYING it, got %d units of %s purchased", bought, testInputGood)
	}
	found := false
	for _, e := range logger.snapshot() {
		if strings.Contains(e.message, "TORWIND-F") && strings.Contains(e.message, "reclaim_feed_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a reclaim/redirect log naming the ship and reason=reclaim_feed_cargo, got %+v", logger.snapshot())
	}
}

// (c) A gate hull holding the run's own ROOT output (FAB_PLATE) is NOT a feed — it is delivered
// to the gate/sink by the terminal, not fed into a parent — so it passes through to normal
// production untouched and nothing is sold here.
func TestReclaimStrayFeedCargo_GateHullHoldingRootOutput_PassesThroughUntouched(t *testing.T) {
	outItem, err := shared.NewCargoItem(testOutputGood, testOutputGood, "", 20)
	if err != nil {
		t.Fatalf("cargo item: %v", err)
	}
	hull := newTestHauler(t, "CRAFTY-ROOT", []*shared.CargoItem{outItem})
	f, cmd := reclaimFixture(t, true, hull)

	remaining := f.handler.reclaimStrayFeedCargo(context.Background(), cmd, deepFabTree(), []*navigation.Ship{hull})

	if len(remaining) != 1 || remaining[0].ShipSymbol() != "CRAFTY-ROOT" {
		t.Fatalf("a hull holding the ROOT output must pass through to normal production, got %v", remaining)
	}
	if sold := f.mediator.soldUnitsOf(testOutputGood); sold != 0 {
		t.Fatalf("a hull holding the root output must not be redirected/sold as a feed, got %d units of %s sold", sold, testOutputGood)
	}
}

// (d) A hull holding a good ABSENT from the tree is not a feed either: the reclaim step leaves
// it untouched (and never sells it) — it remains filterUnrelatedCargo's skip/liquidation
// concern, preserved exactly.
func TestReclaimStrayFeedCargo_GateHullHoldingUnrelatedGood_NotRedirected(t *testing.T) {
	foodItem, err := shared.NewCargoItem("FOOD", "FOOD", "", 20)
	if err != nil {
		t.Fatalf("cargo item: %v", err)
	}
	hull := newTestHauler(t, "CRAFTY-FOOD", []*shared.CargoItem{foodItem})
	f, cmd := reclaimFixture(t, true, hull)

	remaining := f.handler.reclaimStrayFeedCargo(context.Background(), cmd, deepFabTree(), []*navigation.Ship{hull})

	if len(remaining) != 1 || remaining[0].ShipSymbol() != "CRAFTY-FOOD" {
		t.Fatalf("a hull holding a good absent from the tree must pass through untouched (still filterUnrelatedCargo's concern), got %v", remaining)
	}
	if sold := f.mediator.soldUnitsOf("FOOD"); sold != 0 {
		t.Fatalf("a good absent from the tree must never be delivered as a feed, got %d units of FOOD sold", sold)
	}
}

// (e) OFF / profit-factory contract: with the unified gate-fill toggle off (default), the
// reclaim step is dark — a hull holding an intermediate passes straight through and nothing is
// delivered (byte-identical to today).
func TestReclaimStrayFeedCargo_ToggleOff_PassesThroughUntouched(t *testing.T) {
	ironItem, err := shared.NewCargoItem(testInputGood, testInputGood, "", 20)
	if err != nil {
		t.Fatalf("cargo item: %v", err)
	}
	hull := newTestHauler(t, "CRAFTY-OFF", []*shared.CargoItem{ironItem})
	f, cmd := reclaimFixture(t, false, hull) // gate off

	remaining := f.handler.reclaimStrayFeedCargo(context.Background(), cmd, deepFabTree(), []*navigation.Ship{hull})

	if len(remaining) != 1 || remaining[0].ShipSymbol() != "CRAFTY-OFF" {
		t.Fatalf("with the gate toggle off the reclaim step must be a no-op passthrough, got %v", remaining)
	}
	if sold := f.mediator.soldUnitsOf(testInputGood); sold != 0 {
		t.Fatalf("with the gate toggle off nothing may be delivered as a feed (byte-identical), got %d units sold", sold)
	}
}
