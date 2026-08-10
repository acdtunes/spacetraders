package services

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-ftqgp: capital was a shared POOL guarded by floors, and a floor allocates nothing. Trade caps
// itself at 25% of live treasury; construction had NO proportional cap, so gate-fill consumed
// everything above its 150k floor — including the whole band trade needs to function (measured
// era 5: the gate pipeline drove treasury 1.18M -> 193k and the tour engine was shrunk to 12-unit
// and ONE-unit tranches). This suite pins the construction half of the fix: the SAME buy that the
// flat floor waves through is PARKED once trade's share of deployable capital is reserved, and the
// budget collapses to exactly the old behavior the moment trade goes idle.
//
// The harness is the spend-floor one unchanged: the input buy costs exactly 100 credits,
// so every case below turns purely on where the floor sits, never on affordability.

type fakeCapitalWorkSensor struct {
	tradeWork        bool
	constructionWork bool
	err              error
	tradeCalls       int
}

func (s *fakeCapitalWorkSensor) TradeHasWork(_ context.Context, _ int) (bool, error) {
	s.tradeCalls++
	if s.err != nil {
		return false, s.err
	}
	return s.tradeWork, nil
}

func (s *fakeCapitalWorkSensor) ConstructionHasWork(_ context.Context, _ int) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.constructionWork, nil
}

// The headline regression. At 150,100 credits a 100-credit input buy lands the treasury exactly on
// the 150k non-contract floor, so the pre-budget engine BUYS — and that is the bug: those credits
// are the band trade needs. With trade live, construction's budget is 40% of the 100 deployable,
// so its enforced floor rises to 150,060 and the buy PARKS.
//
// The other rows walk the new boundary (deployable 166 is the first pool where construction's 60%
// covers the 100-credit buy) and pin the two non-proportional resolutions: an idle trade side
// degrades to the flat floor byte-for-byte, and an unreadable sensor fails CONSERVATIVE (assume
// trade is working) rather than handing construction the whole treasury.
func TestBuyGood_CapitalBudget_ReservesTradesShareAboveTheFlatFloor(t *testing.T) {
	cases := []struct {
		name      string
		credits   int
		sensor    *fakeCapitalWorkSensor
		wantBuys  int
		wantFloor int // the reserve the park WARNING must name; 0 when nothing parks
	}{
		{
			// THE BUG: pre-budget this buy proceeds (150,100 − 100 = 150,000 ≥ 150,000).
			name:      "parks a buy the flat floor allows once trade's share is reserved",
			credits:   150_100,
			sensor:    &fakeCapitalWorkSensor{tradeWork: true},
			wantBuys:  0,
			wantFloor: 150_040, // 150,000 + 40% of the 100 deployable
		},
		{
			// GRACEFUL DEGRADATION: the identical treasury, trade idle -> construction holds
			// the whole pool and the enforced floor is the flat 150k again. No capital idles
			// because the other side is stopped.
			name:     "hands construction the whole pool when trade is idle",
			credits:  150_100,
			sensor:   &fakeCapitalWorkSensor{tradeWork: false},
			wantBuys: 1,
		},
		{
			// 150,166 − 100 = 150,066; floor = 150,000 + round(0.4 x 166) = 150,066. Exact
			// boundary -> PROCEED.
			name:     "proceeds when the budget covers the buy exactly",
			credits:  150_166,
			sensor:   &fakeCapitalWorkSensor{tradeWork: true},
			wantBuys: 1,
		},
		{
			// One credit under that boundary: 150,065 < 150,066 -> PARK. Pre-budget this buy
			// proceeds too (150,165 ≥ 150,000), so it is a second independent witness.
			name:      "parks one credit below the budget boundary",
			credits:   150_165,
			sensor:    &fakeCapitalWorkSensor{tradeWork: true},
			wantBuys:  0,
			wantFloor: 150_066,
		},
		{
			// FAIL CONSERVATIVE: a blind sensor read must not be read as "trade is idle" and
			// hand construction 100% — that is the exact failure this bead removes.
			name:      "fails conservative when the sensor cannot be read",
			credits:   150_100,
			sensor:    &fakeCapitalWorkSensor{err: errors.New("container registry unreadable")},
			wantBuys:  0,
			wantFloor: 150_040,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: tc.credits})
			executor.SetCapitalWorkSensor(tc.sensor)
			logger := &dwellCapturingLogger{}
			ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-FTQGP"), logger)

			node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
			if _, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false); err != nil {
				t.Fatalf("a budget decision must never surface as an error: %v", err)
			}

			if mediator.purchaseAttempts() != tc.wantBuys {
				t.Fatalf("credits %d: want %d purchase dispatches, got %d — trade's %d%% share of deployable capital must be reserved (sp-ftqgp)",
					tc.credits, tc.wantBuys, mediator.purchaseAttempts(), common.TradeCapitalSharePct)
			}
			if tc.sensor.tradeCalls == 0 {
				t.Fatal("the trade-side hasWork sensor was never consulted — the budget is not in the guard path at all")
			}
			if tc.wantFloor > 0 {
				want := fmt.Sprintf("reserve %d", tc.wantFloor)
				if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), want) {
					t.Fatalf("credits %d: no park WARNING naming the BUDGETED reserve %d; warnings: %+v",
						tc.credits, tc.wantFloor, logger.entriesWithLevel("WARNING"))
				}
			}
		})
	}
}

// The fabricated-OUTPUT harvest — the two LARGEST gate purchases (FAB_MATS,
// ADVANCED_CIRCUITRY) — funnels through the SAME spendFloorBreached primitive, so it must be
// budgeted identically. A drift between the two construction spend paths fails exactly this test.
// The harvest costs exactly 430 credits.
func TestPurchaseFabricatedOutput_CapitalBudget_ReservesTradesShareAboveTheFlatFloor(t *testing.T) {
	cases := []struct {
		name      string
		credits   int
		tradeWork bool
		wantUnits int
	}{
		// 150,430 − 430 = 150,000: clears the flat floor exactly, so pre-budget it harvests.
		// Trade live -> floor rises to 150,000 + round(0.4 x 430) = 150,172 -> PARK.
		{name: "parks a harvest the flat floor allows once trade's share is reserved", credits: 150_430, tradeWork: true, wantUnits: 0},
		// Same treasury, trade idle -> the whole pool is construction's -> the flat floor -> harvest.
		{name: "hands construction the whole pool when trade is idle", credits: 150_430, tradeWork: false, wantUnits: outputFloorTradeVolume},
		// deployable 716: round(0.4 x 716) = 286, floor 150,286; 150,716 − 430 = 150,286 -> PROCEED.
		{name: "proceeds when the budget covers the harvest exactly", credits: 150_716, tradeWork: true, wantUnits: outputFloorTradeVolume},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor, mediator := newOutputFloorExecutor(t, &spendFloorFakeAPIClient{credits: tc.credits})
			executor.SetCapitalWorkSensor(&fakeCapitalWorkSensor{tradeWork: tc.tradeWork})
			logger := &dwellCapturingLogger{}
			ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-FTQGP"), logger)

			units, cost, err := buyFabricatedOutput(executor, ctx)
			if err != nil {
				t.Fatalf("a budget decision must never surface as an error: %v", err)
			}
			if units != tc.wantUnits {
				t.Fatalf("credits %d (trade live=%v): want %d harvested units, got %d — the harvest path must honor the same capital budget as the input buy",
					tc.credits, tc.tradeWork, tc.wantUnits, units)
			}
			if tc.wantUnits == 0 {
				if cost != 0 {
					t.Fatalf("a parked harvest must spend nothing, got cost %d", cost)
				}
				if mediator.purchaseAttempts() != 0 {
					t.Fatalf("a parked harvest must dispatch no purchase, got %d", mediator.purchaseAttempts())
				}
			}
		})
	}
}

// An executor with no sensor wired behaves EXACTLY as it did before this bead — the flat
// non-contract floor and nothing else. This is the contract every existing fixture in this package
// relies on (they wire no sensor), and it is what makes the budget purely additive.
func TestBuyGood_CapitalBudget_UnwiredSensorKeepsTheFlatFloor(t *testing.T) {
	// 150,100 − 100 = 150,000: parks under the budget with trade live (above), buys without it.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 150_100})
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-FTQGP"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	if _, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("want 1 purchase dispatch with no sensor wired (the pre-budget flat %d floor), got %d",
			common.NonContractWorkingCapitalFloor, mediator.purchaseAttempts())
	}
}
