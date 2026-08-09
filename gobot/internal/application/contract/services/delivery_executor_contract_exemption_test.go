package services

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// floorArithmeticLedger applies the REAL ledger's admission rule
// (spend_reservation_repository.go: liveCredits − Σreservations < reserveFloor → refuse)
// instead of a scripted verdict, so a source-buy driven through it clears BOTH contract money
// guards — the per-buy floor AND the cross-operation cap — exactly as production wires them
// (main.go hands the contract workflow the shared ledger). A fake that always says yes would
// hide a cap still gating on the old floor.
type floorArithmeticLedger struct {
	gotFloor    int
	gotCredits  int64
	reserveCall int
}

func (l *floorArithmeticLedger) Reserve(ctx context.Context, _ int, _ string, projectedCost int, readBudget func(context.Context) (int64, int, error)) (string, bool, error) {
	l.reserveCall++
	credits, floor, err := readBudget(ctx)
	if err != nil {
		return "", false, err
	}
	l.gotCredits, l.gotFloor = credits, floor
	if credits-int64(projectedCost) < int64(floor) {
		return "", false, nil
	}
	return "EXEMPTION-RES-1", true, nil
}

func (l *floorArithmeticLedger) Release(context.Context, int, string) error { return nil }

// runExemptionSourceBuy drives ONE contract source-buy through the real delivery path with
// BOTH production guards armed, and reports the units purchased, whether it parked, and the
// floor the cross-operation cap was asked to judge against.
func runExemptionSourceBuy(t *testing.T, liveCredits, unitAsk, unitsRequired, unitsFulfilled, capacity int) (purchased []int, parked bool, ledger *floorArithmeticLedger) {
	t.Helper()

	ship := buildContractHauler(t, capacity)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	med := &sourceFloorFakeMediator{navShip: ship, liveCredits: liveCredits}
	ledger = &floorArithmeticLedger{}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo),
		WithSourceBuyFloor(), WithConcurrentSpendCap(ledger))

	ctx := common.WithLogger(context.Background(), &capturingLogger{})
	delivery := domainContract.Delivery{
		TradeSymbol:       "CLOTHING",
		DestinationSymbol: "X1-DU34-E53",
		UnitsRequired:     unitsRequired,
		UnitsFulfilled:    unitsFulfilled,
	}
	profit := &contractQueries.ProfitabilityResult{
		PurchaseCost:           (unitsRequired - unitsFulfilled) * unitAsk,
		CheapestMarketWaypoint: "X1-DU34-K89",
		MarketPrices:           map[string]int{"CLOTHING": unitAsk},
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profit, &RunWorkflowResponse{}, nil)
	var insufficient *ErrInsufficientCredits
	return med.purchasedUnits, errors.As(err, &insufficient), ledger
}

// TestContractSourceBuyIsExemptFromTheWorkingCapitalFloor replays the era torwind-2026-08-09
// hour-0 deadlock at its exact live numbers and pins the outcome the Admiral's RULINGS #5
// amendment requires: contract work is EXEMPT from the working-capital floor.
//
// The incident: a PROCUREMENT contract sat 14/24 delivered, the remaining 10 units cost 28,880
// at the cheap source, and that 28,880 unlocked a 109,635 fulfilment payout. Treasury was
// 56,958. 56,958 − 28,880 = 28,078, under the 50,000 working-capital floor, so BOTH contract
// money guards refused: the per-buy floor parked the lot and the cross-operation cap would have
// refused the reservation. The command frigate was the fleet's ONLY earner (haulers were still
// capital-gated), so nothing could earn the 21,922 gap — treasury sat FLAT at 56,958 across six
// heartbeats until an Admiral-authorised waiver broke it by hand.
//
// A contract source-buy is not discretionary spend. It IS the fleet's earning path, so
// floor-blocking it starves the very cash flow the floor exists to protect.
func TestContractSourceBuyIsExemptFromTheWorkingCapitalFloor(t *testing.T) {
	const (
		treasury  = 56_958 // the live figure
		unitAsk   = 2_888  // the cheap source at X1-DU34-K89
		unitsLeft = 10     // 24 required − 14 already delivered
		cost      = unitsLeft * unitAsk
	)

	// Pin the premise, so this test cannot silently stop reproducing the incident: the buy
	// really does land under the old working-capital floor.
	require.Equal(t, 28_880, cost, "the incident's source-buy cost")
	require.Less(t, treasury-cost, int(common.ImmutableReserveFloor),
		"the repro is only meaningful while the buy lands BELOW the working-capital floor")

	purchased, parked, ledger := runExemptionSourceBuy(t, treasury, unitAsk, 24, 14, 40)

	require.False(t, parked,
		"contract work is EXEMPT from the working-capital floor: a 28,880 source-buy that unlocks a 109,635 payout must PROCEED against a 56,958 treasury, not park the fleet's sole earner")
	require.Equal(t, []int{unitsLeft}, purchased,
		"the exempt buy must source the FULL remaining lot, not a floor-shrunk partial")
	require.Equal(t, 1, ledger.reserveCall, "the buy must still take its cross-operation reservation")
	require.Less(t, ledger.gotFloor, int(common.ImmutableReserveFloor),
		"the cross-operation cap must judge the contract buy against the exempt solvency reserve too; leaving it on the working-capital floor would park the buy one guard later and make the exemption inert in production")
}

// TestContractSolvencyReserveStillBinds pins the OTHER half of the amendment: the exemption
// releases the fleet's WORKING CAPITAL, it never lets a contract buy strand the fleet.
//
// A naked exemption drains treasury to 0, and a fleet at 0 credits cannot refuel — it bricks
// itself with no recovery path, strictly worse than the deadlock the exemption fixes. The
// minimal absolute solvency reserve is what stands between the two, so it gets its own tests:
// a guard with no test is not a guard.
func TestContractSolvencyReserveStillBinds(t *testing.T) {
	floor := int(common.ContractSolvencyReserve)

	t.Run("a buy the fleet could afford outright is still REFUSED when it would strand it", func(t *testing.T) {
		// Treasury 12,000 against an 18-unit lot at 500 = 9,000. Raw affordability is not the
		// question — the fleet HAS the 9,000 — but paying it leaves 3,000, below the solvency
		// reserve, so the largest lot that clears the reserve is 4 units, under the minimum
		// partial. The buy parks with the fleet's mobility intact.
		purchased, parked, _ := runExemptionSourceBuy(t, 12_000, 500, 18, 0, 40)

		require.True(t, parked, "a buy that would leave the fleet below the solvency reserve must PARK")
		require.Empty(t, purchased, "a solvency-parked buy must dispatch ZERO purchases")
	})

	t.Run("the exempt buy spends down to EXACTLY the solvency reserve and never below", func(t *testing.T) {
		// Treasury 20,000, 18 units at 1,000. The full 18,000 lot would leave 2,000; the
		// largest lot that respects the reserve is (20,000−floor)/1,000 = 10 units, spending
		// 10,000 and leaving exactly the reserve. This is the guard's edge, in units.
		const treasury, ask = 20_000, 1_000
		wantUnits := (treasury - floor) / ask

		purchased, parked, _ := runExemptionSourceBuy(t, treasury, ask, 18, 0, 40)

		require.False(t, parked, "an affordable partial lot must proceed, not park")
		require.Equal(t, []int{wantUnits}, purchased,
			"the exempt buy must shrink to the largest lot that leaves the solvency reserve intact")
		require.Equal(t, floor, treasury-wantUnits*ask, "the surviving treasury must be exactly the reserve")
	})

	t.Run("the exemption does not reopen the fail-closed branches", func(t *testing.T) {
		// RULINGS #4 still binds everything the amendment did not name. An unreadable
		// treasury cannot be judged against ANY floor, exempt or not, so it still parks.
		ship := buildContractHauler(t, 40)
		shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
		med := &sourceFloorFakeMediator{navShip: ship, treasuryErr: errors.New("agent read failed")}
		executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithSourceBuyFloor())

		ctx := common.WithLogger(context.Background(), &capturingLogger{})
		_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil,
			domainContract.Delivery{TradeSymbol: "CLOTHING", DestinationSymbol: "X1-DU34-E53", UnitsRequired: 10},
			&contractQueries.ProfitabilityResult{
				PurchaseCost:           28_880,
				CheapestMarketWaypoint: "X1-DU34-K89",
				MarketPrices:           map[string]int{"CLOTHING": 2_888},
			}, &RunWorkflowResponse{}, nil)

		var insufficient *ErrInsufficientCredits
		require.True(t, errors.As(err, &insufficient), "an unreadable treasury must still fail CLOSED and park")
		require.Empty(t, med.purchasedUnits, "a blind buy must dispatch ZERO purchases")
	})
}

// TestContractSolvencyReserveIsSizedForMobilityNotWorkingCapital pins the reserve's SIZE
// against the evidence it was derived from. A number nobody can re-derive drifts into a second
// working-capital floor by accident, which is exactly what the amendment forbids.
func TestContractSolvencyReserveIsSizedForMobilityNotWorkingCapital(t *testing.T) {
	// worstObservedRefuel is the single most expensive REFUEL ever recorded in the ledger
	// (max over 89,282 rows, all eras). It is the pessimistic cost of one hull's one hop.
	const worstObservedRefuel = 5_190

	require.GreaterOrEqual(t, int(common.ContractSolvencyReserve), 2*worstObservedRefuel,
		"the reserve must cover a hull's worst-case OUT AND BACK (%d x2) — reach a market, and reach one more if the first cannot help. Below that a contract buy can leave a hull unable to move, which is worse than the deadlock the exemption fixes",
		worstObservedRefuel)
	require.Less(t, int(common.ContractSolvencyReserve), int(common.ImmutableReserveFloor)/3,
		"the reserve is a MOBILITY floor, not working capital. Letting it climb toward the %d working-capital floor re-imposes by the back door exactly what the Admiral exempted",
		common.ImmutableReserveFloor)
}
