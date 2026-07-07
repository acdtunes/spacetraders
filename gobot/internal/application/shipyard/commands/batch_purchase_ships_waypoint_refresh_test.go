package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// These tests pin the fix for sp-vz9u: `shipyard purchase --waypoint` must not
// false-fail against an empty shipyard listing. The SpaceTraders /shipyard
// endpoint only returns priced `ships` listings when one of your ships is
// present at the waypoint; until then it returns just `shipTypes` and an empty
// `ships` array (the "empty cache" the bead describes). The batch handler reads
// the price up front for budget math BEFORE the purchase loop navigates the ship
// there, so a pinned waypoint used to report "ship type not available" even
// though the shipyard sells it. An explicit waypoint must be at least as
// reliable as auto-discovery, so on an empty listing the handler defers to the
// purchase loop, which visits the pinned waypoint and reads fresh listings.

const (
	wpRefreshPinnedWaypoint = "X1-GZ7-A1"
	wpRefreshShipType       = "SHIP_MINING_DRONE"
)

// wpRefreshFakeMediator embeds common.Mediator so any dispatch other than the
// shipyard listings query nil-panics, keeping the fake honest about what
// calculatePurchasableCount actually sends.
type wpRefreshFakeMediator struct {
	common.Mediator

	shipyardResp *queries.GetShipyardListingsResponse
	sends        int
}

func (m *wpRefreshFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sends++
	if _, ok := request.(*queries.GetShipyardListingsQuery); !ok {
		return nil, nil
	}
	return m.shipyardResp, nil
}

// wpRefreshFakeAPIClient embeds the port so only GetAgent is implemented; it is
// exercised solely on the priced-listing path where budget math runs.
type wpRefreshFakeAPIClient struct {
	domainPorts.APIClient

	credits int
}

func (c *wpRefreshFakeAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	return &player.AgentData{Credits: c.credits}, nil
}

func wpRefreshCommand() *BatchPurchaseShipsCommand {
	return &BatchPurchaseShipsCommand{
		PurchasingShipSymbol: "BUYER-1",
		ShipType:             wpRefreshShipType,
		Quantity:             3,
		MaxBudget:            0,
		PlayerID:             shared.MustNewPlayerID(1),
		ShipyardWaypoint:     wpRefreshPinnedWaypoint,
	}
}

// The bug: a pinned waypoint whose shipyard SELLS the type but returns an empty
// listing (no ship present yet) must not false-fail. The handler must defer to
// the purchase loop by returning the full requested quantity and preserving the
// pinned waypoint so the loop navigates there and reads fresh listings.
func TestBatchPurchase_PinnedWaypointEmptyCache_DefersInsteadOfFalseFailing(t *testing.T) {
	med := &wpRefreshFakeMediator{
		shipyardResp: &queries.GetShipyardListingsResponse{
			Shipyard: shipyard.Shipyard{
				Symbol:    wpRefreshPinnedWaypoint,
				ShipTypes: []string{wpRefreshShipType}, // shipyard sells the type
				Listings:  nil,                         // but no priced listing: no ship present
			},
		},
	}
	handler := &BatchPurchaseShipsHandler{mediator: med}
	cmd := wpRefreshCommand()

	_, purchasableCount, shipyardWaypoint, err := handler.calculatePurchasableCount(context.Background(), cmd, "token")

	if err != nil {
		t.Fatalf("pinned waypoint with an empty shipyard listing must not false-fail, got error: %v", err)
	}
	if purchasableCount != cmd.Quantity {
		t.Fatalf("expected deferral to full quantity %d so the loop visits and reads fresh, got %d", cmd.Quantity, purchasableCount)
	}
	if shipyardWaypoint != wpRefreshPinnedWaypoint {
		t.Fatalf("expected pinned waypoint %q preserved for the purchase loop, got %q", wpRefreshPinnedWaypoint, shipyardWaypoint)
	}
}

// Guard against over-deferral: a shipyard that genuinely does not sell the type
// (the type is absent from shipTypes, which the API always returns) must still
// fail fast rather than send the ship on a wasted trip.
func TestBatchPurchase_PinnedWaypointGenuinelyNotSold_FailsFast(t *testing.T) {
	med := &wpRefreshFakeMediator{
		shipyardResp: &queries.GetShipyardListingsResponse{
			Shipyard: shipyard.Shipyard{
				Symbol:    wpRefreshPinnedWaypoint,
				ShipTypes: []string{"SHIP_ORE_HOUND"}, // does not sell wpRefreshShipType
				Listings:  nil,
			},
		},
	}
	handler := &BatchPurchaseShipsHandler{mediator: med}
	cmd := wpRefreshCommand()

	_, _, _, err := handler.calculatePurchasableCount(context.Background(), cmd, "token")

	if err == nil {
		t.Fatalf("a shipyard that genuinely does not sell the type must fail fast, got nil error")
	}
}

// A priced listing (ship already present) must still drive precise budget math,
// so the fix does not regress the common case: credits 250000 / price 100000 = 2
// caps a requested quantity of 3 at 2.
func TestBatchPurchase_PinnedWaypointWithPricedListing_UsesPreciseBudgetMath(t *testing.T) {
	med := &wpRefreshFakeMediator{
		shipyardResp: &queries.GetShipyardListingsResponse{
			Shipyard: shipyard.Shipyard{
				Symbol:    wpRefreshPinnedWaypoint,
				ShipTypes: []string{wpRefreshShipType},
				Listings: []shipyard.ShipListing{
					{ShipType: wpRefreshShipType, PurchasePrice: 100000},
				},
			},
		},
	}
	handler := &BatchPurchaseShipsHandler{mediator: med, apiClient: &wpRefreshFakeAPIClient{credits: 250000}}
	cmd := wpRefreshCommand()

	shipPrice, purchasableCount, shipyardWaypoint, err := handler.calculatePurchasableCount(context.Background(), cmd, "token")

	if err != nil {
		t.Fatalf("priced listing must not error, got: %v", err)
	}
	if shipPrice != 100000 {
		t.Fatalf("expected ship price 100000 from the listing, got %d", shipPrice)
	}
	if purchasableCount != 2 {
		t.Fatalf("expected budget-capped count 2 (250000/100000), got %d", purchasableCount)
	}
	if shipyardWaypoint != wpRefreshPinnedWaypoint {
		t.Fatalf("expected pinned waypoint %q preserved, got %q", wpRefreshPinnedWaypoint, shipyardWaypoint)
	}
}
