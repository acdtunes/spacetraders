package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-q8bon (Admiral 2026-07-24: "We can't go down 150K! Or else we risk contracts
// stalling"): gate-fill margin-blind buys dragged the treasury 638k→142k, and the contract
// engine — the sole earner — parked its next 106,400 source-buy against the 50k floor: a
// full-economy deadlock. The fix raises the construction engine's working-capital reserve
// default to effectiveReserveFloor() (150k) so the 50k–150k band stays
// contract-exclusive. This suite pins BOTH construction buy paths — the factory INPUT buy
// (buyGood via ProduceGood) and the fabricated-OUTPUT harvest (purchaseFabricatedOutput) —
// at the raised floor, reusing the spend_floor / output_spend_floor harnesses unchanged
// (input buy costs exactly 100, harvest costs exactly 430).

// The factory INPUT buy parks anywhere inside the contract-exclusive band and proceeds
// the moment the post-buy treasury clears 150k. The 100,000 case is the sp-q8bon delta:
// under the old 50k default it bought (99,900 headroom) — exactly the class of buy that
// dragged the treasury into the band and stalled contracts.
func TestBuyGood_NonContractFloor_ParksInContractBandProceedsAbove(t *testing.T) {
	cases := []struct {
		name        string
		credits     int
		wantBuys    int
		wantParkLog bool
	}{
		// Below the contract cushion: clears the immutable base, breaches this floor → PARK.
		{name: "parks inside the contract-exclusive band", credits: common.ContractReserveCushion - 50_000, wantBuys: 0, wantParkLog: true},
		// One credit under the boundary → PARK.
		{name: "parks one credit below the floor boundary", credits: effectiveReserveFloor() + 99, wantBuys: 0, wantParkLog: true},
		// Exactly the boundary → PROCEED.
		{name: "proceeds when the post-buy treasury meets the floor exactly", credits: effectiveReserveFloor() + 100, wantBuys: 1, wantParkLog: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: tc.credits})
			logger := &dwellCapturingLogger{}
			ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-Q8BON"), logger)

			node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
			result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
			if err != nil {
				t.Fatalf("a floor decision must never surface as an error: %v", err)
			}
			if mediator.purchaseAttempts() != tc.wantBuys {
				t.Fatalf("credits %d: want %d purchase dispatches, got %d — the non-contract input-buy floor is %d (sp-q8bon)", tc.credits, tc.wantBuys, mediator.purchaseAttempts(), effectiveReserveFloor())
			}
			if tc.wantBuys == 0 && (result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0) {
				t.Fatalf("a parked buy must yield a zero-spend result, got %+v", result)
			}
			gotParkLog := spendFloorWarnContains(logger.entriesWithLevel("WARNING"), fmt.Sprintf("reserve %d", effectiveReserveFloor()))
			if gotParkLog != tc.wantParkLog {
				t.Fatalf("credits %d: park WARNING naming reserve %d present=%v, want %v; warnings: %+v", tc.credits, effectiveReserveFloor(), gotParkLog, tc.wantParkLog, logger.entriesWithLevel("WARNING"))
			}
		})
	}
}

// The fabricated-OUTPUT harvest (the two LARGEST gate purchases — the observed parked
// 157,976 FAB_MATS harvest) is floored IDENTICALLY: parked inside the band, executed once
// the post-buy treasury clears 150k. Same effectiveReserveFloor primitive as the input buy,
// so a drift between the two paths fails exactly one of these suites.
func TestPurchaseFabricatedOutput_NonContractFloor_ParksInContractBandProceedsAbove(t *testing.T) {
	cases := []struct {
		name      string
		credits   int
		wantUnits int
		wantBuys  int
	}{
		// 100,000 − 430 = 99,570: clears the old 50k floor, breaches the 150k one → PARK.
		{name: "parks inside the contract-exclusive band", credits: common.ContractReserveCushion - 50_000, wantUnits: 0, wantBuys: 0},
		// 150,430 − 430 = 150,000 ≥ 150,000 → PROCEED (exact boundary), full tranche.
		{name: "proceeds when the post-buy treasury meets the floor exactly", credits: effectiveReserveFloor()+430, wantUnits: outputFloorTradeVolume, wantBuys: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, mediator := newOutputFloorExecutor(t, &spendFloorFakeAPIClient{credits: tc.credits})
			logger := &dwellCapturingLogger{}
			ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-Q8BON"), logger)

			units, cost, err := buyFabricatedOutput(executor, ctx)
			if err != nil {
				t.Fatalf("a floor decision must never surface as an error: %v", err)
			}
			if units != tc.wantUnits {
				t.Fatalf("credits %d: want %d harvested units, got %d — the non-contract harvest floor is %d (sp-q8bon)", tc.credits, tc.wantUnits, units, effectiveReserveFloor())
			}
			if tc.wantBuys == 0 && cost != 0 {
				t.Fatalf("a parked harvest must spend nothing, got cost %d", cost)
			}
			if mediator.purchaseAttempts() != tc.wantBuys {
				t.Fatalf("credits %d: want %d purchase dispatches, got %d", tc.credits, tc.wantBuys, mediator.purchaseAttempts())
			}
		})
	}
}
