package commands

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-c07v: a stocker crash once left 80 FOOD aboard hauler TORWIND-38. The
// MEDICINE factory correctly discovered and claimed the now-idle hull (it was
// never dedicated, so the sp-m92a/sp-sg35 dedication guard didn't apply), then
// spun three zero-unit "Hold full and could not unload existing cargo" BUY
// no-ops before giving up on that hull - no data loss (nothing was jettisoned),
// but wasted API calls and a thrashing run. The fix is a claim-time filter,
// ported from the contract coordinator's own NO-CARGO-DUMP guard (sp-wq7r): a
// hull holding cargo unrelated to this factory's production tree is simply
// never claimed. These tests pin the filter itself (filterUnrelatedCargo) and
// both seams that call it (waitForIdleHaulers' initial discovery,
// refreshShipPoolOnce's mid-run tick).

// (a) The TORWIND-38 shape: a fully-laden hull holding cargo that has nothing
// to do with this factory's tree is excluded entirely and the skip is logged
// with the ship, the held goods, and reason=unrelated_cargo - all in the
// MESSAGE TEXT, since the container-log renderer drops the metadata map
// (sp-iqyq convention).
func TestFilterUnrelatedCargo_LadenWithUnrelatedCargo_SkippedAndLogged(t *testing.T) {
	foodItem, err := shared.NewCargoItem("FOOD", "FOOD", "", 40)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	laden := newTestHauler(t, "TORWIND-38", []*shared.CargoItem{foodItem})

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	claimable, symbols := filterUnrelatedCargo(ctx, []*navigation.Ship{laden}, []string{testOutputGood, testInputGood})

	if len(claimable) != 0 || len(symbols) != 0 {
		t.Fatalf("expected the laden hull holding unrelated cargo to be skipped entirely, got claimable=%v symbols=%v", claimable, symbols)
	}

	found := false
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "TORWIND-38") && strings.Contains(e.message, "FOOD") && strings.Contains(e.message, "unrelated_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a skip log naming ship=TORWIND-38, the held goods (FOOD), and reason=unrelated_cargo in the MESSAGE TEXT, got entries: %+v", entries)
	}
}

// (b) A hull holding the TARGET good itself (the factory's own output) is
// legitimate, already-useful cargo, not a stranger's dump - it must remain
// claimable.
func TestFilterUnrelatedCargo_HoldsTargetGood_StillClaimed(t *testing.T) {
	outputItem, err := shared.NewCargoItem(testOutputGood, testOutputGood, "", 10)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	ship := newTestHauler(t, "CRAFTY-40", []*shared.CargoItem{outputItem})

	claimable, symbols := filterUnrelatedCargo(context.Background(), []*navigation.Ship{ship}, []string{testOutputGood, testInputGood})

	if len(claimable) != 1 || len(symbols) != 1 || symbols[0] != "CRAFTY-40" {
		t.Fatalf("expected a hull holding the target good itself to remain claimable, got claimable=%v symbols=%v", claimable, symbols)
	}
}

// (b2) A hull holding an INPUT good from elsewhere in the tree (not just the
// top-level target) is also legitimate cargo - the exact shape newFactoryFixture
// already relies on incidentally (shipWithIron/IRON); this pins it directly.
// Narrowing the guard to only the top-level target good would wrongly park a
// hull mid-flight on a feed leg.
func TestFilterUnrelatedCargo_HoldsTreeInputGood_StillClaimed(t *testing.T) {
	ironItem, err := shared.NewCargoItem(testInputGood, testInputGood, "", 10)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	ship := newTestHauler(t, "CRAFTY-41", []*shared.CargoItem{ironItem})

	claimable, symbols := filterUnrelatedCargo(context.Background(), []*navigation.Ship{ship}, []string{testOutputGood, testInputGood})

	if len(claimable) != 1 || len(symbols) != 1 || symbols[0] != "CRAFTY-41" {
		t.Fatalf("expected a hull holding a tree INPUT good (not just the top-level target) to remain claimable, got claimable=%v symbols=%v", claimable, symbols)
	}
}

// (c) An empty hull must keep claiming exactly as before the guard existed -
// regression protection for the overwhelmingly common case.
func TestFilterUnrelatedCargo_EmptyHull_StillClaimed(t *testing.T) {
	ship := newTestHauler(t, "CRAFTY-42", nil)

	claimable, symbols := filterUnrelatedCargo(context.Background(), []*navigation.Ship{ship}, []string{testOutputGood, testInputGood})

	if len(claimable) != 1 || len(symbols) != 1 || symbols[0] != "CRAFTY-42" {
		t.Fatalf("expected an empty hull to remain claimable (regression), got claimable=%v symbols=%v", claimable, symbols)
	}
}

// (d) Seam 1/2: the initial discovery path (waitForIdleHaulers) must apply the
// guard too, not just the pure filter function in isolation - proves the
// wiring, not just the logic. A laden TORWIND-38-shaped hull sits alongside a
// clean hull; only the clean one comes back claimable, and the skip is logged.
func TestWaitForIdleHaulers_SkipsLadenHullWithUnrelatedCargo_ClaimsOnlyCleanHull(t *testing.T) {
	handler, shipRepo := newFactoryHandlerAndShipRepo(t, &factoryFakeClock{})

	foodItem, err := shared.NewCargoItem("FOOD", "FOOD", "", 40)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	laden := newTestHauler(t, "TORWIND-38", []*shared.CargoItem{foodItem})
	clean := newTestHauler(t, "CRAFTY-43", nil)
	shipRepo.ships = map[string]*navigation.Ship{
		laden.ShipSymbol(): laden,
		clean.ShipSymbol(): clean,
	}
	shipRepo.order = []string{laden.ShipSymbol(), clean.ShipSymbol()}

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	idle, symbols, err := handler.waitForIdleHaulers(ctx, shared.MustNewPlayerID(1), testSystem, []string{testOutputGood, testInputGood}, "factory-unrelated-cargo-test")
	if err != nil {
		t.Fatalf("waitForIdleHaulers: %v", err)
	}

	if len(idle) != 1 || len(symbols) != 1 || symbols[0] != "CRAFTY-43" {
		t.Fatalf("expected only the clean hull claimed and the laden hull with unrelated cargo skipped, got %v", symbols)
	}

	found := false
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "TORWIND-38") && strings.Contains(e.message, "unrelated_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a skip log for TORWIND-38 naming reason=unrelated_cargo in the message text, got entries: %+v", entries)
	}
}

// (d) Seam 2/2: the mid-run refresh path (refreshShipPoolOnce) must apply the
// same guard on every discovery tick, not just at initial launch - otherwise a
// hull that goes idle-with-foreign-cargo mid-run would slip into the pool on
// the very next tick.
func TestRefreshShipPoolOnce_SkipsLadenHullWithUnrelatedCargo_LogsAndDoesNotPool(t *testing.T) {
	handler, shipRepo := newFactoryHandlerAndShipRepo(t, &factoryFakeClock{})

	foodItem, err := shared.NewCargoItem("FOOD", "FOOD", "", 40)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	laden := newTestHauler(t, "TORWIND-38", []*shared.CargoItem{foodItem})
	shipRepo.ships = map[string]*navigation.Ship{laden.ShipSymbol(): laden}
	shipRepo.order = []string{laden.ShipSymbol()}

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	shipPool := make(chan *navigation.Ship, 2)
	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	discoveryCount := handler.refreshShipPoolOnce(ctx, shared.MustNewPlayerID(1), testSystem, []string{testOutputGood, testInputGood}, shipPool, shipsUsed, &mu, 0)

	if discoveryCount != 0 {
		t.Fatalf("expected discoveryCount to stay 0 - the only idle hull holds unrelated cargo and must not be pooled, got %d", discoveryCount)
	}
	if len(shipPool) != 0 {
		t.Fatalf("a laden hull holding unrelated cargo must never be added to the ship pool, got pool len %d", len(shipPool))
	}
	if shipsUsed["TORWIND-38"] {
		t.Fatal("a skipped hull must not be recorded in shipsUsed")
	}

	found := false
	entries := logger.snapshot()
	for _, e := range entries {
		if strings.Contains(e.message, "TORWIND-38") && strings.Contains(e.message, "unrelated_cargo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a skip log for TORWIND-38 naming reason=unrelated_cargo, got entries: %+v", entries)
	}
}
