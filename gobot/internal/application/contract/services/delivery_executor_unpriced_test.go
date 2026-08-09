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

// sp-gef01: AN UNPRICED SOURCE-BUY MUST FAIL CLOSED.
//
// projectedUnitAsk reaches both money guards as a COST, and a zero unit price makes that
// cost 0 — which is not "cheap", it is UNKNOWN. A cost of 0 does two things at once:
//
//   - affordableSourceBuyLot's floor passes trivially (treasury minus 0 is always above
//     the reserve), so it returns the FULL UNSHRUNK lot rather than a shrunk one; and
//   - reserveConcurrentSpendOrPark returns early on projectedCost <= 0, so NO RESERVATION
//     IS TAKEN AT ALL.
//
// The buy then executes at REAL prices with both armed guards blind. That is a money guard
// failing OPEN on missing data — the opposite of RULINGS #4.
//
// WHICH ZERO IS ACTUALLY REACHABLE. The bead describes a MAP MISS. A miss cannot reach here:
// buildMarketPricesMap writes prices[good] for every unfulfilled delivery, and the domain's
// calculatePurchaseCost comma-ok checks the same map and ERRORS the whole evaluation on an
// absent key, so an unpriced good never yields a ProfitabilityResult at all. The cold/quiet
// regime the bead names already fails closed three layers upstream: an unscanned good makes
// FindCheapestMarketSelling return nil, candidateOrNil nil, and PlanDeliverySourcing an error.
//
// The reachable zero is a PRESENT key holding 0 — FindCheapestMarketSelling orders
// purchase_price ASC with no positive-price filter, so a 0-priced market_data row would sort
// FIRST and be selected. Both shapes are pinned below anyway: they arrive at the guard as the
// same value, and a guard that must fail closed on an unknown price should not care which
// upstream produced it.
const (
	unpricedUnitsRequired = 18      // one hull-sized lot (buildShipWithIronOre holds 40)
	unpricedRealAsk       = 2_000   // the priced control: an 18-unit lot really costs 36,000
	unpricedAmpleTreasury = 500_000 // far above the 50k floor, so a park can ONLY be the refusal
)

// runUnpricedSourceBuy drives one contract source-buy with the caller's EXACT MarketPrices
// map. runContractSourceBuy cannot be reused here: it always writes prices[good] = unitAsk,
// so it structurally cannot express an ABSENT key (the state half these cases assert).
func runUnpricedSourceBuy(t *testing.T, ledger ConcurrentSpendLedger, liveCredits int, prices map[string]int, armFloor bool) (purchased []int, parked bool, logs *capturingLogger) {
	t.Helper()

	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	med := &sourceFloorFakeMediator{navShip: ship, liveCredits: liveCredits}

	opts := []DeliveryExecutorOption{WithConcurrentSpendCap(ledger)}
	if armFloor {
		opts = append(opts, WithSourceBuyFloor())
	}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), opts...)

	logs = &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logs)

	delivery := domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-TEST-A1",
		UnitsRequired:     unpricedUnitsRequired,
	}
	profit := &contractQueries.ProfitabilityResult{
		CheapestMarketWaypoint: "X1-TEST-M1",
		MarketPrices:           prices,
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profit, &RunWorkflowResponse{}, nil)
	var insufficient *ErrInsufficientCredits
	return med.purchasedUnits, errors.As(err, &insufficient), logs
}

// THE DEFECT. An unpriced lot with AMPLE treasury must be REFUSED, not bought at a
// projected cost of zero.
//
// Treasury is deliberately 500,000 against a 50,000 floor: every other reason this path can
// park (headroom below the floor, an unreadable treasury, a cap denial) is excluded by
// construction, so a park here can only be the unpriced refusal — and, on unfixed code, a
// dispatched purchase can only be the fail-open.
func TestSourceBuy_UnpricedLot_RefusesInsteadOfBuyingAtCostZero(t *testing.T) {
	cases := []struct {
		name           string
		prices         map[string]int
		wantKeyPresent bool
	}{
		{
			name:           "present key holding a zero ask - the reachable shape, a 0-priced row sorts first under purchase_price ASC",
			prices:         map[string]int{"IRON_ORE": 0},
			wantKeyPresent: true,
		},
		{
			name:           "absent key - the shape the bead describes",
			prices:         map[string]int{},
			wantKeyPresent: false,
		},
		{
			name:           "nil map - no market data at all",
			prices:         nil,
			wantKeyPresent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// FIXTURE CHECK. A fixture that quietly carried a real price would pass against
			// unfixed code and prove nothing.
			ask, present := tc.prices["IRON_ORE"]
			require.Equal(t, tc.wantKeyPresent, present, "fixture must express the key-presence state this case is named for")
			require.Zero(t, ask, "fixture must deliver a ZERO projected ask to the guard, or this case cannot exercise the defect")

			ledger := &recordingCapLedger{ok: true}
			purchased, parked, logs := runUnpricedSourceBuy(t, ledger, unpricedAmpleTreasury, tc.prices, true)

			// BEHAVIOUR FIRST: require stops the test, so the discriminating assertion leads.
			require.Empty(t, purchased,
				"an unpriced lot must dispatch ZERO purchases. Buying here spends REAL credits against a projected cost of 0, which is exactly the fail-open: the floor saw treasury %d minus 0 and waved the full %d-unit lot through",
				unpricedAmpleTreasury, unpricedUnitsRequired)
			require.True(t, parked,
				"an unpriced lot must PARK (ErrInsufficientCredits), so the coordinator re-projects it next pass rather than the run failing")
			require.Equal(t, 0, ledger.reserveCalls,
				"a refused buy must not reserve: there is no meaningful amount to reserve for a lot nobody can price")
			require.True(t, warningsContain(logs, "unpriced"),
				"the refusal must be legible in the container log - the renderer drops the metadata map, got %v", logs.warnings())
		})
	}
}

// THE MONEY INVARIANT, stated directly: every purchase this path dispatches must be covered
// by a reservation on the aggregate cap.
//
// This is the assertion that discriminates old from new for the CAP, which the refusal test
// alone cannot: unfixed code takes zero reservations for an unpriced lot AND for a refused
// one. The difference is the purchase. Unfixed: 1 purchase against 0 reservations — spend no
// guard ever saw. Fixed: 0 against 0, and the priced control 1 against 1.
func TestSourceBuy_EveryDispatchedPurchaseIsCoveredByAReservation(t *testing.T) {
	cases := []struct {
		name          string
		prices        map[string]int
		wantPurchases int
	}{
		{
			name:          "priced lot buys once and is covered once",
			prices:        map[string]int{"IRON_ORE": unpricedRealAsk},
			wantPurchases: 1,
		},
		{
			name:          "unpriced lot is refused, so there is nothing to cover",
			prices:        map[string]int{"IRON_ORE": 0},
			wantPurchases: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &recordingCapLedger{ok: true}
			purchased, _, _ := runUnpricedSourceBuy(t, ledger, unpricedAmpleTreasury, tc.prices, true)

			require.Equal(t, len(purchased), ledger.reserveCalls,
				"MONEY INVARIANT: %d purchase(s) dispatched against %d reservation(s). An uncovered purchase is spend the aggregate cap never saw, at real prices, while it believed the cost was 0",
				len(purchased), ledger.reserveCalls)
			require.Len(t, purchased, tc.wantPurchases, "purchases dispatched = %v", purchased)
		})
	}
}

// ACCEPTANCE 3: the aggregate cap must take a reservation for the REAL projected cost, not a
// placeholder. 18 units at 2,000 is 36,000 — the figure the shared ledger arbitrates on.
func TestSourceBuy_PricedLot_ReservesTheRealProjectedCost(t *testing.T) {
	ledger := &recordingCapLedger{ok: true}
	purchased, parked, _ := runUnpricedSourceBuy(t, ledger, unpricedAmpleTreasury, map[string]int{"IRON_ORE": unpricedRealAsk}, true)

	require.Equal(t, []int{unpricedUnitsRequired}, purchased, "a priced lot must still buy the full lot - the refusal must not touch the priced path")
	require.False(t, parked, "a priced, affordable lot must not park")
	require.Equal(t, 1, ledger.reserveCalls, "the priced buy must consult the shared cap exactly once")
	require.Equal(t, unpricedUnitsRequired*unpricedRealAsk, ledger.gotCost,
		"the cap must reserve the REAL projected cost (%d units x %d), which is the whole point: reserving 0 records an intent that constrains nobody",
		unpricedUnitsRequired, unpricedRealAsk)
	require.Equal(t, int(common.ContractSolvencyReserve), ledger.gotFloor, "the contract side still reserves against its own exempt solvency reserve")
}

// NON-INTERFERENCE. The refusal rides the floor's OWN arming and adds no new seam.
//
// This is not a default-off feature flag: run_contract_workflow.go appends WithSourceBuyFloor()
// unconditionally, and that is the only production construction of a DeliveryExecutor, so the
// refusal is ARMED on deploy. The unarmed path exists solely for the positional-constructor
// tests, whose byte-identical contract this pins - including
// TestPurchaseLoop_NoProjectedBasis_LadderCapDisabled, where a missing basis legitimately means
// only "the ladder cap has nothing to compare against".
func TestSourceBuy_UnpricedLot_UnarmedFloorIsUnchanged(t *testing.T) {
	ledger := &recordingCapLedger{ok: true}
	purchased, parked, _ := runUnpricedSourceBuy(t, ledger, unpricedAmpleTreasury, map[string]int{"IRON_ORE": 0}, false)

	require.False(t, parked, "with the floor unwired the guard is inert, exactly as before")
	require.NotEmpty(t, purchased, "an executor built without WithSourceBuyFloor must be byte-identical")
}
