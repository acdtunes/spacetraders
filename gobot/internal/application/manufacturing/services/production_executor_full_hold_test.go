package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-mu6u: a goods_factory feeder crashed with "no cargo space available for
// purchase" when an input BUY was attempted against an already-full hold — e.g.
// SHIP_PARTS-ff2078ee, ADVANCED_CIRCUITRY-b69aabe7/07391f2f, MEDICINE-5cbd2bce,
// SHIP_PLATING-7cefb592, all terminal-crashed on this exact string across
// 2026-07-08/09. Unlike sp-vsfn's refuel-500 (a transient API failure), a full
// hold is a DETERMINISTIC condition that persists unchanged across every
// container restart, so the generic restart-budget (MaxRestartAttempts=3)
// guarantees eventual "crashed (unrecoverable)" rather than ever recovering.
//
// Fix: when the hold is full, sell whatever is already onboard at the current
// market (we're already docked there) to make room, then proceed with the
// purchase. If nothing sells (the market won't buy any of it, or the hold
// reports full with nothing in it), skip this input with a 0-unit result
// instead of a terminal error, mirroring the established empty-tranche-skip
// pattern so the feeder survives.
//
// Reuses the dock-race harness (newDockRaceExecutor): the ONLY faked
// collaborator is the ShipRepository/market/mediator boundary.

const dockRaceStaleGood = "STALE_CARGO"

func fullStaleCargo(units int) []*shared.CargoItem {
	item, err := shared.NewCargoItem(dockRaceStaleGood, dockRaceStaleGood, "", units)
	if err != nil {
		panic(err)
	}
	return []*shared.CargoItem{item}
}

// A full hold carrying cargo the market WILL buy must be unloaded, freeing
// space, so the input purchase proceeds — not a terminal crash.
func TestBuyGood_FullHold_UnloadsExistingCargoThenPurchases(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	repo.fillCargo(fullStaleCargo(40))

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a full hold that CAN be unloaded must not crash the feeder: got %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful purchase after unloading, got %+v", result)
	}
	if mediator.sellAttempts() != 1 {
		t.Fatalf("expected exactly 1 sell of the stale cargo to free space, got %d", mediator.sellAttempts())
	}
}

// A full hold carrying cargo the market WON'T buy must be skipped gracefully
// (0-unit result, no error) rather than crash — mirroring the empty-tranche
// skip pattern.
func TestBuyGood_FullHold_UnsellableCargo_SkipsGracefully(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	repo.fillCargo(fullStaleCargo(40))
	mediator.sellShouldFail = true

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an unsellable full hold must be skipped, not crash the feeder: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("a skipped full-hold purchase must yield a 0-unit result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("no purchase should be attempted when the hold could not be freed, got %d", mediator.purchaseAttempts())
	}
}
