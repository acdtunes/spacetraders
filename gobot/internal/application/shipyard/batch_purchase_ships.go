package shipyard

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// BatchPurchaseShipsCommand is a command to purchase multiple ships of the same type in a batch
//
// The command will purchase as many ships as possible within constraints:
// - Quantity requested
// - Maximum budget allocated
// - Player's available credits
//
// The purchasing ship will be used to navigate to the shipyard if needed.
// If shipyard_waypoint is not provided, will auto-discover nearest shipyard that sells the ship type.
type BatchPurchaseShipsCommand struct {
	PurchasingShipSymbol string
	ShipType             string
	Quantity             int
	MaxBudget            int
	PlayerID   shared.PlayerID
	ShipyardWaypoint     string // Optional - will auto-discover if empty
}

// BatchPurchaseShipsResponse contains the list of purchased ships and total cost
type BatchPurchaseShipsResponse struct {
	PurchasedShips      []*navigation.Ship
	TotalCost           int
	ShipsPurchasedCount int
}

// BatchPurchaseShipsHandler handles the BatchPurchaseShips command
type BatchPurchaseShipsHandler struct {
	playerRepo player.PlayerRepository
	mediator   common.Mediator
	apiClient  ports.APIClient
}

// NewBatchPurchaseShipsHandler creates a new BatchPurchaseShipsHandler
func NewBatchPurchaseShipsHandler(
	playerRepo player.PlayerRepository,
	mediator common.Mediator,
	apiClient ports.APIClient,
) *BatchPurchaseShipsHandler {
	return &BatchPurchaseShipsHandler{
		playerRepo: playerRepo,
		mediator:   mediator,
		apiClient:  apiClient,
	}
}

// Handle executes the BatchPurchaseShips command
func (h *BatchPurchaseShipsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*BatchPurchaseShipsCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Validate quantity and budget
	if cmd.Quantity <= 0 || cmd.MaxBudget <= 0 {
		return &BatchPurchaseShipsResponse{
			PurchasedShips:      []*navigation.Ship{},
			TotalCost:           0,
			ShipsPurchasedCount: 0,
		}, nil
	}

	// 2. Get player token from context
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 3. Get shipyard listings to determine ship price (if shipyard provided)
	// This helps us calculate max purchasable count upfront
	var shipPrice int
	var purchasableCount int
	shipyardWaypoint := cmd.ShipyardWaypoint

	if shipyardWaypoint != "" {
		// Get shipyard listings to determine price
		// Extract system symbol (find last hyphen)
		systemSymbol := shipyardWaypoint
		for i := len(shipyardWaypoint) - 1; i >= 0; i-- {
			if shipyardWaypoint[i] == '-' {
				systemSymbol = shipyardWaypoint[:i]
				break
			}
		}
		query := &GetShipyardListingsQuery{
			SystemSymbol:   systemSymbol,
			WaypointSymbol: shipyardWaypoint,
			PlayerID:       cmd.PlayerID,
		}
		shipyardResp, err := h.mediator.Send(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("failed to get shipyard listings: %w", err)
		}

		shipyardListings, ok := shipyardResp.(*GetShipyardListingsResponse)
		if !ok {
			return nil, fmt.Errorf("invalid response type from GetShipyardListings")
		}

		// Find ship type in listings
		listing, found := shipyardListings.Shipyard.FindListingByType(cmd.ShipType)
		if !found {
			return nil, fmt.Errorf("ship type %s not available at shipyard %s", cmd.ShipType, shipyardWaypoint)
		}

		shipPrice = listing.PurchasePrice

		// Get current credits from API
		agentData, err := h.apiClient.GetAgent(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("failed to get agent data: %w", err)
		}

		// Calculate maximum ships that can be purchased
		maxByQuantity := cmd.Quantity
		maxByBudget := cmd.MaxBudget / shipPrice
		maxByCredits := agentData.Credits / shipPrice

		purchasableCount = utils.Min3(maxByQuantity, maxByBudget, maxByCredits)
	} else {
		// Shipyard will be discovered on first purchase
		// We'll purchase up to quantity requested
		purchasableCount = cmd.Quantity
	}

	// 4. Purchase ships in loop
	// Each PurchaseShipCommand will fetch fresh credits from API before purchase
	// First purchase will auto-discover shipyard if not provided
	var purchasedShips []*navigation.Ship
	totalSpent := 0

	for i := 0; i < purchasableCount; i++ {
		// Call PurchaseShipCommand via mediator
		// NOTE: PurchaseShipCommand will auto-discover shipyard on first call if not provided
		// After first purchase, use the discovered shipyard location
		purchaseCmd := &PurchaseShipCommand{
			PurchasingShipSymbol: cmd.PurchasingShipSymbol,
			ShipType:             cmd.ShipType,
			PlayerID:             cmd.PlayerID,
			ShipyardWaypoint:     shipyardWaypoint,
		}

		resp, err := h.mediator.Send(ctx, purchaseCmd)
		if err != nil {
			// If we've already purchased some ships, return what we have
			// rather than failing completely
			if len(purchasedShips) > 0 {
				return &BatchPurchaseShipsResponse{
					PurchasedShips:      purchasedShips,
					TotalCost:           totalSpent,
					ShipsPurchasedCount: len(purchasedShips),
				}, nil
			}
			return nil, fmt.Errorf("failed to purchase ship %d of %d: %w", i+1, purchasableCount, err)
		}

		purchaseResp, ok := resp.(*PurchaseShipResponse)
		if !ok {
			return nil, fmt.Errorf("invalid response type from PurchaseShip")
		}

		purchasedShips = append(purchasedShips, purchaseResp.Ship)
		totalSpent += purchaseResp.PurchasePrice

		// After first purchase, capture the shipyard location for subsequent purchases
		if shipyardWaypoint == "" && i == 0 {
			// Get the shipyard waypoint from the first purchased ship's location
			shipyardWaypoint = purchaseResp.Ship.CurrentLocation().Symbol
		}

		// Check if we've exceeded budget (safety check)
		if totalSpent+shipPrice > cmd.MaxBudget {
			break
		}

		// Check if we've exhausted credits (using the returned agent credits from last purchase)
		if purchaseResp.AgentCredits < shipPrice {
			break
		}
	}

	// 5. Return response with purchased ships
	return &BatchPurchaseShipsResponse{
		PurchasedShips:      purchasedShips,
		TotalCost:           totalSpent,
		ShipsPurchasedCount: len(purchasedShips),
	}, nil
}
