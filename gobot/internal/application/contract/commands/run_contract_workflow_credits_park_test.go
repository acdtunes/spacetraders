package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// creditsParkFakeShipRepo embeds the full ShipRepository interface (nil) and
// only overrides the two methods the workflow's purchase path actually calls
// (ReloadShipState -> SyncShipFromAPI; post-purchase reload -> FindBySymbol).
// Any other method call panics, same pattern as reconcileFakeShipRepo in the
// services package test.
type creditsParkFakeShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *creditsParkFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *creditsParkFakeShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func buildCreditsParkTestShip(t *testing.T) *navigation.Ship {
	t.Helper()

	waypoint, err := shared.NewWaypoint("X1-TEST-M1", 1, 1)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}

	ship, err := navigation.NewShip(
		"TORWIND-9",
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

// creditsParkFakeMediator drives RunWorkflowHandler.Handle end-to-end through
// the purchase path: profitability evaluation succeeds (unlike the sibling
// AfterFulfill test, so ProcessAllDeliveries actually reaches purchasing),
// navigate/dock succeed, and the purchase itself fails with the 4600 wire
// error. GetPlayerQuery answers the WARNING-enrichment lookup. Negotiate/
// Accept/Fulfill are intentionally NOT handled: the seeded contract is
// already accepted and has units remaining, so AcceptContractIfNeeded
// short-circuits and FulfillContract is never reached (the park happens
// first) - any call to those would be a bug and should fail the test via the
// default case.
type creditsParkFakeMediator struct {
	common.Mediator

	ship *navigation.Ship

	purchaseCalls int
}

func (m *creditsParkFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *contractQueries.EvaluateContractProfitabilityQuery:
		return &contractQueries.ProfitabilityResult{
			IsProfitable:           true,
			PurchaseCost:           100188,
			CheapestMarketWaypoint: "X1-TEST-M1",
		}, nil

	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.ship}, nil

	case *shipTypes.DockShipCommand:
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		return nil, fmt.Errorf(`API error (status 400): {"error":{"message":"Purchase failed. Agent has insufficient funds.","code":4600}}`)

	case *playerQueries.GetPlayerQuery:
		return &playerQueries.GetPlayerResponse{Player: &player.Player{Credits: 85517}}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T (park should happen at first purchase attempt, before any negotiate/accept/fulfill call)", request)
	}
}

// Reproduces the sp-vwhi incident end-to-end through the public Handle()
// entrypoint: a contract purchase hitting 4600 must PARK (clean nil-error
// exit so the container runner does not count it as a crash/restart) rather
// than propagate the error and crashloop. Before this fix, the coordinator
// respawned the worker roughly every 10s (~18 container.crashed events in 3
// minutes) until the captain intervened manually.
func TestRunWorkflowHandler_ParksOnInsufficientCredits(t *testing.T) {
	seedContract := mustNewWorkflowTestContract(t, "contract-park", 0) // 80 units of ALUMINUM remaining
	if err := seedContract.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	contractRepo := newWorkflowStubContractRepo(seedContract)

	ship := buildCreditsParkTestShip(t)
	shipRepo := &creditsParkFakeShipRepo{ship: ship}
	mediator := &creditsParkFakeMediator{ship: ship}

	handler := NewRunWorkflowHandler(mediator, shipRepo, contractRepo, nil)

	ctx := auth.WithPlayerToken(context.Background(), "test-token")
	cmd := &RunWorkflowCommand{
		ShipSymbol: "TORWIND-9",
		PlayerID:   shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(ctx, cmd)

	// The critical "no crash" proof: a park must surface as a nil Go error
	// so the container runner treats this as a clean exit, not a failure.
	if err != nil {
		t.Fatalf("expected Handle to return a nil error on park (container must not crashloop), got: %v", err)
	}

	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}

	if mediator.purchaseCalls != 1 {
		t.Fatalf("expected exactly one purchase attempt before parking, got %d", mediator.purchaseCalls)
	}

	// Numbers requirement: the renderer drops the metadata map, so
	// credits_needed/credits_available/action/reason must be in the message
	// text itself. result.Error is what a caller/log consumer actually sees.
	for _, substr := range []string{"credits_needed=100188", "credits_available=85517", "action=parked", "reason=insufficient_credits"} {
		if !strings.Contains(result.Error, substr) {
			t.Errorf("expected result.Error to contain %q, got: %s", substr, result.Error)
		}
	}

	if result.Fulfilled {
		t.Errorf("expected contract to remain unfulfilled after a park, got Fulfilled=true")
	}
}
