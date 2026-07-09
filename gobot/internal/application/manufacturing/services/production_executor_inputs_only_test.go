package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// PART A (sp-q02m): --inputs-only mode. A construction-support factory must FEED the
// dependency tree but NOT harvest the fabricated output — the finished good is left
// in the factory's export stock for the construction pipeline to be the sole buyer.
// The era-2 gate fill froze at 898/1600 for ~6h because the goods_factory-FAB_MATS
// run bought back its own 149 FAB_MATS. The skip point is PollForProduction: once the
// output appears in exports (production confirmed) it must NOT call
// purchaseFabricatedOutput when inputsOnly is set.
//
// These reuse the dock-race harness (newDockRaceExecutor): the market serves
// dockRaceGood as an export, so PollForProduction finds it immediately, and every
// PurchaseCargoCommand is counted by the mediator — so "harvest skipped" is provable
// as "zero purchase attempts".

func TestPollForProduction_InputsOnly_LeavesOutputInFactory(t *testing.T) {
	// script=nil → the mediator models the real docked-precondition and would happily
	// sell; the ONLY reason no purchase happens is the inputs-only skip.
	executor, _, mediator := newDockRaceExecutor(t, nil)

	quantity, cost, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		true, // inputsOnly
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("inputs-only poll must succeed (production confirmed), got error: %v", err)
	}
	if quantity != 0 || cost != 0 {
		t.Fatalf("inputs-only must NOT harvest: expected (0 units, 0 cost), got (%d units, %d cost)", quantity, cost)
	}
	// The decisive assertion: the output was left in factory stock, never pulled onto
	// a hull — i.e. purchaseFabricatedOutput was skipped, so zero buys were issued.
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("inputs-only must issue ZERO purchases (leave output in factory), got %d", mediator.purchaseAttempts())
	}
}

func TestPollForProduction_Default_HarvestsOutput(t *testing.T) {
	// Default (inputsOnly=false) must preserve the original behavior: harvest the
	// fabricated output. No existing caller may regress.
	executor, _, mediator := newDockRaceExecutor(t, nil)

	quantity, cost, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false, // default: harvest
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("default poll must succeed and harvest, got error: %v", err)
	}
	if quantity <= 0 {
		t.Fatalf("default must harvest the output: expected >0 units, got %d", quantity)
	}
	if cost <= 0 {
		t.Fatalf("default harvest must record a purchase cost, got %d", cost)
	}
	if mediator.purchaseAttempts() < 1 {
		t.Fatalf("default must issue at least one purchase, got %d", mediator.purchaseAttempts())
	}
}
