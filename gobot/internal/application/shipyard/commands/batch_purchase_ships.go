package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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
	PlayerID             shared.PlayerID
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
	apiClient  domainPorts.APIClient
}

// NewBatchPurchaseShipsHandler creates a new BatchPurchaseShipsHandler
func NewBatchPurchaseShipsHandler(
	playerRepo player.PlayerRepository,
	mediator common.Mediator,
	apiClient domainPorts.APIClient,
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

	if response := h.validatePurchaseRequest(cmd.Quantity, cmd.MaxBudget); response != nil {
		return response, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	shipPrice, purchasableCount, shipyardWaypoint, err := h.calculatePurchasableCount(ctx, cmd, token)
	if err != nil {
		return nil, err
	}

	purchasedShips, totalSpent, err := h.executePurchaseLoop(ctx, cmd, purchasableCount, shipyardWaypoint, shipPrice)
	if err != nil {
		return nil, err
	}

	return &BatchPurchaseShipsResponse{
		PurchasedShips:      purchasedShips,
		TotalCost:           totalSpent,
		ShipsPurchasedCount: len(purchasedShips),
	}, nil
}

// validatePurchaseRequest validates quantity and budget constraints
// Returns early-return response if validation fails, nil if valid
func (h *BatchPurchaseShipsHandler) validatePurchaseRequest(quantity int, maxBudget int) *BatchPurchaseShipsResponse {
	if quantity <= 0 || maxBudget <= 0 {
		return &BatchPurchaseShipsResponse{
			PurchasedShips:      []*navigation.Ship{},
			TotalCost:           0,
			ShipsPurchasedCount: 0,
		}
	}
	return nil
}

// calculatePurchasableCount determines how many ships can be purchased
// Considers: requested quantity, budget constraints, agent credits
// Returns: ship price, purchasable count, shipyard waypoint, error
func (h *BatchPurchaseShipsHandler) calculatePurchasableCount(
	ctx context.Context,
	cmd *BatchPurchaseShipsCommand,
	token string,
) (shipPrice int, purchasableCount int, shipyardWaypoint string, err error) {
	shipyardWaypoint = cmd.ShipyardWaypoint

	if shipyardWaypoint != "" {
		shipPrice, err = h.getShipPriceFromShipyard(ctx, shipyardWaypoint, cmd.ShipType, cmd.PlayerID)
		if err != nil {
			return 0, 0, "", err
		}

		agentData, err := h.apiClient.GetAgent(ctx, token)
		if err != nil {
			return 0, 0, "", fmt.Errorf("failed to get agent data: %w", err)
		}

		purchasableCount = h.calculateMaxPurchasableShips(cmd.Quantity, cmd.MaxBudget, agentData.Credits, shipPrice)
	} else {
		purchasableCount = cmd.Quantity
	}

	return shipPrice, purchasableCount, shipyardWaypoint, nil
}

// getShipPriceFromShipyard fetches shipyard data and gets price for ship type
// Returns: ship purchase price, error
func (h *BatchPurchaseShipsHandler) getShipPriceFromShipyard(
	ctx context.Context,
	shipyardWaypoint string,
	shipType string,
	playerID shared.PlayerID,
) (int, error) {
	systemSymbol := shared.ExtractSystemSymbol(shipyardWaypoint)

	query := &queries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: shipyardWaypoint,
		PlayerID:       playerID,
	}
	shipyardResp, err := h.mediator.Send(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	shipyardListings, ok := shipyardResp.(*queries.GetShipyardListingsResponse)
	if !ok {
		return 0, fmt.Errorf("invalid response type from GetShipyardListings")
	}

	listing, found := shipyardListings.Shipyard.FindListingByType(shipType)
	if !found {
		return 0, fmt.Errorf("ship type %s not available at shipyard %s", shipType, shipyardWaypoint)
	}

	return listing.PurchasePrice, nil
}

// calculateMaxPurchasableShips applies all constraints to determine max purchasable count
// Returns: minimum of quantity requested, budget allows, credits allow
func (h *BatchPurchaseShipsHandler) calculateMaxPurchasableShips(
	requestedQuantity int,
	maxBudget int,
	agentCredits int,
	shipPrice int,
) int {
	maxByQuantity := requestedQuantity
	maxByBudget := maxBudget / shipPrice
	maxByCredits := agentCredits / shipPrice
	return utils.Min3(maxByQuantity, maxByBudget, maxByCredits)
}

// executePurchaseLoop purchases ships one at a time up to purchasable count
// Handles partial success, captures shipyard location from first purchase
// Returns: purchased ships, total spent, error
func (h *BatchPurchaseShipsHandler) executePurchaseLoop(
	ctx context.Context,
	cmd *BatchPurchaseShipsCommand,
	purchasableCount int,
	shipyardWaypoint string,
	shipPrice int,
) ([]*navigation.Ship, int, error) {
	var purchasedShips []*navigation.Ship
	totalSpent := 0

	for i := 0; i < purchasableCount; i++ {
		purchaseResp, err := h.purchaseShip(ctx, cmd, shipyardWaypoint)
		if err != nil {
			if len(purchasedShips) > 0 {
				return purchasedShips, totalSpent, nil
			}
			return nil, 0, fmt.Errorf("failed to purchase ship %d of %d: %w", i+1, purchasableCount, err)
		}

		purchasedShips = append(purchasedShips, purchaseResp.Ship)
		totalSpent += purchaseResp.PurchasePrice

		if shipyardWaypoint == "" && i == 0 {
			shipyardWaypoint = purchaseResp.Ship.CurrentLocation().Symbol
		}

		if !h.hasRemainingBudgetAndCredits(totalSpent, purchaseResp.AgentCredits, shipPrice, cmd.MaxBudget) {
			break
		}
	}

	return purchasedShips, totalSpent, nil
}

// purchaseShip purchases a single ship via the PurchaseShipCommand
// Returns: purchase response, error
func (h *BatchPurchaseShipsHandler) purchaseShip(
	ctx context.Context,
	cmd *BatchPurchaseShipsCommand,
	shipyardWaypoint string,
) (*PurchaseShipResponse, error) {
	purchaseCmd := &PurchaseShipCommand{
		PurchasingShipSymbol: cmd.PurchasingShipSymbol,
		ShipType:             cmd.ShipType,
		PlayerID:             cmd.PlayerID,
		ShipyardWaypoint:     shipyardWaypoint,
	}

	resp, err := h.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		return nil, err
	}

	purchaseResp, ok := resp.(*PurchaseShipResponse)
	if !ok {
		return nil, fmt.Errorf("invalid response type from PurchaseShip")
	}

	return purchaseResp, nil
}

// hasRemainingBudgetAndCredits checks if we can afford another ship purchase
// Returns: true if both budget and credits allow another purchase
func (h *BatchPurchaseShipsHandler) hasRemainingBudgetAndCredits(
	totalSpent int,
	remainingCredits int,
	shipPrice int,
	maxBudget int,
) bool {
	return totalSpent+shipPrice <= maxBudget && remainingCredits >= shipPrice
}
