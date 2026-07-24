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

// TestSourceBuyReserveFloor proves the proactive working-capital reserve floor
// (sp-zq635 §4b) on a contract source-buy: a buy whose affordable lot is below the
// minimum partial (sp-8f8fg) is HELD before it (and before the flight to market),
// ample treasury proceeds byte-identical, an unreadable treasury fails CLOSED
// (parks), and the whole guard is INERT unless WithSourceBuyFloor is wired (so
// every existing caller/test is unchanged). One behavior, its variations
// parametrized. The affordable-PARTIAL lot (full breaches, partial >= min) lives in
// TestSourceBuyPartialLotUnwedge below.
func TestSourceBuyReserveFloor(t *testing.T) {
	// The floor in force is the flat common.ImmutableReserveFloor (50k) — sp-05glh scrapped
	// the old EffectiveReserveFloor(50k, 40%, treasury) proportional formula this comment used
	// to describe. affordableSourceBuyLot reads the constant directly (delivery_executor.go),
	// so every case below is a flat-50k check, not a proportional-clamp regime.
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
			name:        "held below floor: even the min partial lot would breach the 50k reserve — parks pre-buy",
			wireFloor:   true,
			liveCredits: 52_000, // headroom 52k-50k=2k buys 1 unit at 2k — below the 5-unit min partial -> HOLD
			unitAsk:     2_000,
			wantUnits:   nil,
			wantPark:    true,
		},
		{
			// sp-q8bon regression pin: the NON-CONTRACT engines' reserve default rose to
			// common.NonContractWorkingCapitalFloor (150k) so the 50k–150k band is
			// contract-EXCLUSIVE — which only works if the contract engine itself keeps
			// gating on the untouched 50k ImmutableReserveFloor. A source-buy landing
			// INSIDE the band (100k − 36k = 64k ≥ 50k) must therefore PROCEED; parking it
			// would mean the contract floor was accidentally raised too, recreating the
			// full-economy deadlock the band exists to prevent.
			name:        "contract keeps the 50k floor: a buy landing in the 50k-150k band proceeds (sp-q8bon)",
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
			name:        "inert when not wired: byte-identical, the buy proceeds even below the floor",
			wireFloor:   false,
			liveCredits: 52_000,
			unitAsk:     2_000,
			wantUnits:   []int{18},
			wantPark:    false,
		},
		{
			name:        "zero projected ask below the floor still parks (no partial-lot math on a priceless basis)",
			wireFloor:   true,
			liveCredits: 40_000, // already under the reserve; ask 0 gives no basis to size a partial lot
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

// TestSourceBuyPartialLotUnwedge pins sp-8f8fg: an all-or-nothing floor park
// deadlocked the sole earner over a small gap (the full 70-unit FOOD lot cost
// 106,400 against treasury 142,185 — a 14,215 breach of the 50k reserve — so the
// worker parked forever with nothing to refill treasury). When the FULL lot would
// breach the floor, the executor must instead buy the largest affordable lot,
// halt further sourcing this pass, and deliver that partial through the existing
// sourcing-halt flow so fulfillment keeps advancing. The floor itself is
// untouched: the partial's projected cost still leaves treasury >= the reserve.
func TestSourceBuyPartialLotUnwedge(t *testing.T) {
	cases := []struct {
		name          string
		cargoCapacity int
		wantUnits     []int // units of each executed purchase, in order
		wantHaltPark  bool  // partial delivered via the sourcing-halt park (not an error park)
	}{
		{
			// The observed incident numbers: treasury 142,185, ask 1,520, 70 units wanted
			// in one hull-sized lot. Affordable = floor((142185-50000)/1520) = 60 units
			// (cost 91,200 leaves 50,985 >= the 50k floor; 61 units would cost 92,720 and
			// breach). Exactly one purchase of exactly 60, then sourcing halts and the
			// delivery leg parks the remainder honestly.
			name:          "full lot breaches -> buys exactly the affordable partial lot and halts sourcing",
			cargoCapacity: 80,
			wantUnits:     []int{60},
			wantHaltPark:  true,
		},
		{
			// Bound pin: the affordable-lot formula never overrides the smaller
			// hull-sized lot. With a 40-capacity hull each trip's lot (40, then 30)
			// clears the floor on the static treasury, so both trips buy FULL lots —
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
			med := &sourceFloorFakeMediator{navShip: ship, liveCredits: 142_185}
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
				PurchaseCost:           70 * 1_520, // 106,400 — the incident's full-lot projection
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
				t.Errorf("sourcing-halt park logged = %v, want %v (a floor-sized partial must deliver-then-park via the existing halt flow)", haltParked, tc.wantHaltPark)
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
