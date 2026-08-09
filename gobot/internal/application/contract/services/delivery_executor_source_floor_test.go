package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sourceFloorFakeMediator drives the contract source-buy path (navigate -> dock ->
// purchase) plus the live-treasury read the proactive reserve floor consults.
// liveCredits is the treasury snapshot returned to that read; treasuryErr makes the
// read fail (drives the fail-closed branch). Each PurchaseCargoCommand's requested
// units are recorded so a HELD buy (zero calls) is distinguishable from an executed
// one AND the floor-sized partial lot (sp-8f8fg) can be pinned to the exact unit count.
type sourceFloorFakeMediator struct {
	common.Mediator

	navShip     *navigation.Ship
	liveCredits int
	treasuryErr error

	purchasedUnits []int
}

func (m *sourceFloorFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch req := request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchasedUnits = append(m.purchasedUnits, req.Units)
		return &shipCargo.PurchaseCargoResponse{TotalCost: 0, UnitsAdded: 0, TransactionCount: 1}, nil
	case *playerQueries.GetPlayerQuery:
		if m.treasuryErr != nil {
			return nil, m.treasuryErr
		}
		return &playerQueries.GetPlayerResponse{Player: &player.Player{Credits: m.liveCredits}}, nil
	default:
		return nil, fmt.Errorf("unexpected mediator command in source-floor test: %T", request)
	}
}

// TestSourceBuyReserveFloor proves the proactive solvency reserve on a contract
// source-buy: a buy whose affordable lot is below the minimum partial is HELD before
// it (and before the flight to market), ample treasury proceeds byte-identical, an
// unreadable treasury fails CLOSED (parks), and the whole guard is INERT unless
// WithSourceBuyFloor is wired (so every existing caller/test is unchanged). One
// behavior, its variations parametrized. The affordable-PARTIAL lot (full breaches,
// partial >= min) lives in TestSourceBuyPartialLotUnwedge below.
func TestSourceBuyReserveFloor(t *testing.T) {
	// The floor in force is the flat common.ContractSolvencyReserve: contract work is exempt
	// from the working-capital floor (RULINGS #5 contract exemption) and answers only to the
	// mobility reserve. affordableSourceBuyLot reads the constant directly, so every case
	// below is a flat check against it.
	cases := []struct {
		name        string
		wireFloor   bool
		liveCredits int
		treasuryErr error
		unitAsk     int   // projected per-unit ask; 18 units wanted per trip
		wantUnits   []int // units of each executed purchase (nil = HELD, zero calls)
		wantPark    bool
	}{
		{
			name:        "held below floor: even the min partial lot would breach the solvency reserve — parks pre-buy",
			wireFloor:   true,
			liveCredits: 12_000, // headroom 12k-12k=0 buys nothing — below the 5-unit min partial -> HOLD
			unitAsk:     2_000,
			wantUnits:   nil,
			wantPark:    true,
		},
		{
			// The whole point of the exemption: the NON-CONTRACT engines hold
			// common.NonContractWorkingCapitalFloor while a contract source-buy answers only
			// to the solvency reserve. A buy landing well inside the band the non-contract
			// engines are fenced out of (100k − 36k = 64k) must PROCEED; parking it would
			// mean the contract engine had picked up a working-capital floor again,
			// recreating the full-economy deadlock the exemption exists to prevent.
			name:        "a buy landing inside the non-contract band proceeds",
			wireFloor:   true,
			liveCredits: 100_000,
			unitAsk:     2_000,
			wantUnits:   []int{18},
			wantPark:    false,
		},
		{
			name:        "ample treasury proceeds byte-identical",
			wireFloor:   true,
			liveCredits: 10_000_000,
			unitAsk:     2_000,
			wantUnits:   []int{18},
			wantPark:    false,
		},
		{
			name:        "fail-closed: an unreadable treasury parks pre-buy",
			wireFloor:   true,
			treasuryErr: errors.New("agent read failed"),
			unitAsk:     2_000,
			wantUnits:   nil,
			wantPark:    true,
		},
		{
			name:        "inert when not wired: byte-identical, the buy proceeds even below the reserve",
			wireFloor:   false,
			liveCredits: 12_000,
			unitAsk:     2_000,
			wantUnits:   []int{18},
			wantPark:    false,
		},
		{
			name:        "zero projected ask below the floor still parks (no partial-lot math on a priceless basis)",
			wireFloor:   true,
			liveCredits: 10_000, // already under the reserve; ask 0 gives no basis to size a partial lot
			unitAsk:     0,
			wantUnits:   nil,
			wantPark:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ship := buildShipWithIronOre(t, 0)
			shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
			med := &sourceFloorFakeMediator{navShip: ship, liveCredits: tc.liveCredits, treasuryErr: tc.treasuryErr}
			cargoManager := NewCargoManager(med, shipRepo)

			opts := []DeliveryExecutorOption{}
			if tc.wireFloor {
				opts = append(opts, WithSourceBuyFloor())
			}
			executor := NewDeliveryExecutor(med, shipRepo, cargoManager, opts...)

			logger := &capturingLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			delivery := domainContract.Delivery{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-TEST-A1",
				UnitsRequired:     18,
				UnitsFulfilled:    0,
			}
			profitResult := &contractQueries.ProfitabilityResult{
				PurchaseCost:           18 * tc.unitAsk,
				CheapestMarketWaypoint: "X1-TEST-M1",
				MarketPrices:           map[string]int{"IRON_ORE": tc.unitAsk},
			}

			_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profitResult, &RunWorkflowResponse{}, nil)

			assertPurchasedUnits(t, med.purchasedUnits, tc.wantUnits)
			var insufficientErr *ErrInsufficientCredits
			parked := errors.As(err, &insufficientErr)
			if parked != tc.wantPark {
				t.Fatalf("park = %v (err %v), want park = %v", parked, err, tc.wantPark)
			}
		})
	}
}

// TestSourceBuyPartialLotUnwedge pins the partial-lot fallback: an all-or-nothing
// reserve park deadlocks the sole earner over a small gap, because nothing else can
// refill the treasury it is waiting on. When the FULL lot would breach the reserve,
// the executor must instead buy the largest affordable lot, halt further sourcing
// this pass, and deliver that partial through the existing sourcing-halt flow so
// fulfillment keeps advancing. The reserve itself is never crossed: the partial's
// projected cost still leaves treasury >= it.
func TestSourceBuyPartialLotUnwedge(t *testing.T) {
	cases := []struct {
		name          string
		cargoCapacity int
		wantUnits     []int // units of each executed purchase, in order
		wantHaltPark  bool  // partial delivered via the sourcing-halt park (not an error park)
	}{
		{
			// Ask 1,520, 70 units wanted in one hull-sized lot against the treasury below.
			// Affordable = floor((104320-12000)/1520) = 60 units (cost 91,200 leaves
			// 13,120 >= the reserve; 61 units would cost 92,720 and breach). Exactly one
			// purchase of exactly 60, then sourcing halts and the delivery leg parks the
			// remainder honestly.
			name:          "full lot breaches -> buys exactly the affordable partial lot and halts sourcing",
			cargoCapacity: 80,
			wantUnits:     []int{60},
			wantHaltPark:  true,
		},
		{
			// Bound pin: the affordable-lot formula never overrides the smaller
			// hull-sized lot. With a 40-capacity hull each trip's lot (40, then 30)
			// clears the reserve on the static treasury, so both trips buy FULL lots —
			// the 60-unit affordability figure must not leak into either trip.
			name:          "per-trip lots within the floor stay full-sized (capacity caps the lot, not the formula)",
			cargoCapacity: 40,
			wantUnits:     []int{40, 30},
			wantHaltPark:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ship := buildContractHauler(t, tc.cargoCapacity)
			shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
			med := &sourceFloorFakeMediator{navShip: ship, liveCredits: 104_320}
			cargoManager := NewCargoManager(med, shipRepo)
			executor := NewDeliveryExecutor(med, shipRepo, cargoManager, WithSourceBuyFloor())

			logger := &capturingLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			delivery := domainContract.Delivery{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-TEST-A1",
				UnitsRequired:     70,
				UnitsFulfilled:    0,
			}
			profitResult := &contractQueries.ProfitabilityResult{
				PurchaseCost:           70 * 1_520, // the full-lot projection the reserve has to shrink
				CheapestMarketWaypoint: "X1-TEST-M1",
				MarketPrices:           map[string]int{"IRON_ORE": 1_520},
			}

			_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profitResult, &RunWorkflowResponse{}, nil)

			if err != nil {
				t.Fatalf("expected the partial lot to proceed without a park error, got %v", err)
			}
			assertPurchasedUnits(t, med.purchasedUnits, tc.wantUnits)

			haltParked := logContains(logger, "parked partial after sourcing halt")
			if haltParked != tc.wantHaltPark {
				t.Errorf("sourcing-halt park logged = %v, want %v (a reserve-sized partial must deliver-then-park via the existing halt flow)", haltParked, tc.wantHaltPark)
			}
		})
	}
}

// assertPurchasedUnits pins each executed purchase to its exact unit count —
// the observable outcome the reserve floor sizes.
func assertPurchasedUnits(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("purchases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("purchase %d = %d units, want %d (full purchases %v, want %v)", i, got[i], want[i], got, want)
		}
	}
}

// logContains reports whether any captured log line contains the substring.
func logContains(logger *capturingLogger, substring string) bool {
	for _, e := range logger.entries {
		if strings.Contains(e.message, substring) {
			return true
		}
	}
	return false
}

// buildContractHauler builds an empty docked hauler with the given cargo capacity —
// the partial-lot tests need a hull big enough to want the incident's whole 70-unit
// lot in one trip (buildShipWithIronOre is fixed at 40).
func buildContractHauler(t *testing.T, capacity int) *navigation.Ship {
	t.Helper()

	waypoint, err := shared.NewWaypoint("X1-PZ28-H63", 1, 1)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	cargo, err := shared.NewCargo(capacity, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}

	ship, err := navigation.NewShip(
		"TORWIND-1",
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_FRIGATE",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}
