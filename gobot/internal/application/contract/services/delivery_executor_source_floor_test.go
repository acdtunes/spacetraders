package services

import (
	"context"
	"errors"
	"fmt"
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
// read fail (drives the fail-closed branch). A successful PurchaseCargoCommand is
// counted so a HELD buy (zero calls) is distinguishable from an executed one.
type sourceFloorFakeMediator struct {
	common.Mediator

	navShip     *navigation.Ship
	liveCredits int
	treasuryErr error

	purchaseCalls int
}

func (m *sourceFloorFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
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
// (sp-zq635 §4b) on a contract source-buy: a buy that would drop treasury below the
// immutable 50k reserve is HELD before it (and before the flight to market), ample
// treasury proceeds byte-identical, an unreadable treasury fails CLOSED (parks), and
// the whole guard is INERT unless WithSourceBuyFloor is wired (so every existing
// caller/test is unchanged). One behavior, its variations parametrized.
func TestSourceBuyReserveFloor(t *testing.T) {
	// The floor in force is the flat common.ImmutableReserveFloor (50k) — sp-05glh scrapped
	// the old EffectiveReserveFloor(50k, 40%, treasury) proportional formula this comment used
	// to describe. sourceBuyFloorBreached reads the constant directly (delivery_executor.go),
	// so every case below is a flat-50k check, not a proportional-clamp regime.
	cases := []struct {
		name          string
		wireFloor     bool
		liveCredits   int
		treasuryErr   error
		unitAsk       int // projected per-unit ask; 18 units bought per trip
		wantPurchases int
		wantPark      bool
	}{
		{
			name:          "held below floor: a source-buy that would breach the 50k reserve parks pre-buy",
			wireFloor:     true,
			liveCredits:   70_000, // 70k - 18*2000=36k = 34k < 50k floor -> HOLD
			unitAsk:       2_000,
			wantPurchases: 0,
			wantPark:      true,
		},
		{
			name:          "ample treasury proceeds byte-identical",
			wireFloor:     true,
			liveCredits:   10_000_000,
			unitAsk:       2_000,
			wantPurchases: 1,
			wantPark:      false,
		},
		{
			name:          "fail-closed: an unreadable treasury parks pre-buy",
			wireFloor:     true,
			treasuryErr:   errors.New("agent read failed"),
			unitAsk:       2_000,
			wantPurchases: 0,
			wantPark:      true,
		},
		{
			name:          "inert when not wired: byte-identical, the buy proceeds even below the floor",
			wireFloor:     false,
			liveCredits:   70_000,
			unitAsk:       2_000,
			wantPurchases: 1,
			wantPark:      false,
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

			if med.purchaseCalls != tc.wantPurchases {
				t.Errorf("purchase calls = %d, want %d (a held buy makes zero calls)", med.purchaseCalls, tc.wantPurchases)
			}
			var insufficientErr *ErrInsufficientCredits
			parked := errors.As(err, &insufficientErr)
			if parked != tc.wantPark {
				t.Fatalf("park = %v (err %v), want park = %v", parked, err, tc.wantPark)
			}
		})
	}
}
