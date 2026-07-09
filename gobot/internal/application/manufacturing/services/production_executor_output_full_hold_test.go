package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-wwhu: the sibling of sp-mu6u's crash, on the OUTPUT-harvest path instead of
// the input-purchase path. purchaseFabricatedOutput (called from
// PollForProduction once production is confirmed) crashed with "no cargo space
// available for output" when the harvest was attempted against an already-full
// hold — e.g. a ship that skipped straight to harvest via
// collectExistingFactorySupply (factory already HIGH/ABUNDANT, so no
// deliverInputs ever ran to empty the hold first) while still carrying residue
// from an earlier, unrelated purchase task. Same terminal-crash-not-park failure
// mode as the input-side bug sp-mu6u already fixed via freeCargoSpace.
//
// Fix: reuse freeCargoSpace (introduced in sp-mu6u, 73ec39b) — sell whatever is
// already onboard at the current market (we're already docked there there to
// harvest) to make room, then proceed with the harvest purchase. If nothing
// sells, skip this harvest with a 0-unit result instead of a terminal error.
// Unlike a skipped INPUT purchase (which loses an ingredient the recipe still
// needs), a skipped output harvest loses nothing: the fabricated good stays in
// the factory's export stock and can be collected on a later pass.
//
// Reuses the dock-race harness (newDockRaceExecutor) exactly as the existing
// inputs-only tests do: the fake market serves dockRaceGood as an export
// immediately, so PollForProduction reaches purchaseFabricatedOutput on the
// first poll with no waiting.

const dockRaceStaleOutputGood = "STALE_CARGO_OUT"

func fullStaleOutputCargo(units int) []*shared.CargoItem {
	item, err := shared.NewCargoItem(dockRaceStaleOutputGood, dockRaceStaleOutputGood, "", units)
	if err != nil {
		panic(err)
	}
	return []*shared.CargoItem{item}
}

// A full hold carrying cargo the market WILL buy must be unloaded, freeing
// space, so the output harvest proceeds — not a terminal crash.
func TestPurchaseFabricatedOutput_FullHold_UnloadsExistingCargoThenPurchases(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	repo.fillCargo(fullStaleOutputCargo(40))

	quantity, cost, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false, // harvest (not inputs-only)
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("a full hold that CAN be unloaded must not crash the harvest: got %v", err)
	}
	if quantity <= 0 {
		t.Fatalf("expected a successful harvest after unloading, got %d units (cost %d)", quantity, cost)
	}
	if mediator.sellAttempts() != 1 {
		t.Fatalf("expected exactly 1 sell of the stale cargo to free space, got %d", mediator.sellAttempts())
	}
}

// A full hold carrying cargo the market WON'T buy must be skipped gracefully
// (0-unit result, no error) rather than crash — the fabricated output stays in
// factory stock for a later harvest.
func TestPurchaseFabricatedOutput_FullHold_UnsellableCargo_SkipsGracefully(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	repo.fillCargo(fullStaleOutputCargo(40))
	mediator.sellShouldFail = true

	quantity, cost, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false, // harvest (not inputs-only)
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("an unsellable full hold must be skipped, not crash the harvest: got %v", err)
	}
	if quantity != 0 || cost != 0 {
		t.Fatalf("a skipped harvest must yield (0 units, 0 cost), got (%d, %d)", quantity, cost)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("no purchase should be attempted when the hold could not be freed, got %d", mediator.purchaseAttempts())
	}
}
