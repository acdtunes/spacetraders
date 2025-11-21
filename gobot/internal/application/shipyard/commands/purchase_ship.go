package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// PurchaseShipCommand is a command to purchase a ship from a shipyard
//
// The purchasing ship will:
// 1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
// 2. Navigate to the shipyard waypoint if not already there
// 3. Dock if in orbit
// 4. Purchase the specified ship type
// 5. Return the new ship entity
type PurchaseShipCommand struct {
	PurchasingShipSymbol string
	ShipType             string
	PlayerID             shared.PlayerID
	ShipyardWaypoint     string // Optional - will auto-discover if empty
}

// PurchaseShipResponse contains the newly purchased ship
type PurchaseShipResponse struct {
	Ship            *navigation.Ship
	PurchasePrice   int
	AgentCredits    int
	TransactionTime string
}

// PurchaseShipHandler handles the PurchaseShip command
type PurchaseShipHandler struct {
	shipRepo         navigation.ShipRepository
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	apiClient        ports.APIClient
	mediator         common.Mediator
}

// NewPurchaseShipHandler creates a new PurchaseShipHandler
func NewPurchaseShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
	apiClient ports.APIClient,
	mediator common.Mediator,
) *PurchaseShipHandler {
	return &PurchaseShipHandler{
		shipRepo:         shipRepo,
		playerRepo:       playerRepo,
		waypointRepo:     waypointRepo,
		waypointProvider: waypointProvider,
		apiClient:        apiClient,
		mediator:         mediator,
	}
}

// Handle executes the PurchaseShip command
func (h *PurchaseShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*PurchaseShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get player token from context
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 2. Load purchasing ship from API
	purchasingShip, err := h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("purchasing ship not found: %w", err)
	}

	// 3. Auto-discover shipyard if not provided
	shipyardWaypoint := cmd.ShipyardWaypoint
	if shipyardWaypoint == "" {
		discoveredWaypoint, err := h.discoverNearestShipyard(ctx, purchasingShip, cmd.ShipType, token)
		if err != nil {
			return nil, fmt.Errorf("failed to discover shipyard: %w", err)
		}
		shipyardWaypoint = discoveredWaypoint
	}

	// 4. Navigate to shipyard if not already there
	if purchasingShip.CurrentLocation().Symbol != shipyardWaypoint {
		navCmd := &commands.NavigateRouteCommand{
			ShipSymbol:  cmd.PurchasingShipSymbol,
			Destination: shipyardWaypoint,
			PlayerID:    cmd.PlayerID,
		}
		_, err := h.mediator.Send(ctx, navCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to navigate to shipyard: %w", err)
		}

		// Reload ship after navigation
		purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
	}

	// 5. Dock ship if in orbit
	if purchasingShip.NavStatus() == navigation.NavStatusInOrbit {
		dockCmd := &commands.DockShipCommand{
			ShipSymbol: cmd.PurchasingShipSymbol,
			PlayerID:   cmd.PlayerID,
		}
		_, err := h.mediator.Send(ctx, dockCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to dock ship: %w", err)
		}

		// Reload ship after docking
		purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after docking: %w", err)
		}
	}

	// 6. Get shipyard listings and validate ship type is available
	// Extract system symbol using domain function
	systemSymbol := shared.ExtractSystemSymbol(shipyardWaypoint)
	query := &queries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: shipyardWaypoint,
		PlayerID:       cmd.PlayerID,
	}
	shipyardResp, err := h.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	shipyardListings, ok := shipyardResp.(*queries.GetShipyardListingsResponse)
	if !ok {
		return nil, fmt.Errorf("invalid response type from GetShipyardListings")
	}

	// 7. Find ship type in listings and get price
	listing, found := shipyardListings.Shipyard.FindListingByType(cmd.ShipType)
	if !found {
		return nil, fmt.Errorf("ship type %s not available at shipyard %s", cmd.ShipType, shipyardWaypoint)
	}

	purchasePrice := listing.PurchasePrice

	// 8. Validate player has sufficient credits
	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent data: %w", err)
	}

	if agentData.Credits < purchasePrice {
		return nil, fmt.Errorf("insufficient credits: have %d, need %d", agentData.Credits, purchasePrice)
	}

	// 9. Call API to purchase ship
	purchaseResult, err := h.apiClient.PurchaseShip(ctx, cmd.ShipType, shipyardWaypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	// 10. Convert API ship data to domain entity
	newShip, err := h.convertShipDataToEntity(ctx, purchaseResult.Ship, cmd.PlayerID, shipyardWaypoint, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

	// 11. Return the new ship
	return &PurchaseShipResponse{
		Ship:            newShip,
		PurchasePrice:   purchaseResult.Transaction.Price,
		AgentCredits:    purchaseResult.Agent.Credits,
		TransactionTime: purchaseResult.Transaction.Timestamp,
	}, nil
}

// discoverNearestShipyard discovers the nearest shipyard that sells the desired ship type
func (h *PurchaseShipHandler) discoverNearestShipyard(
	ctx context.Context,
	purchasingShip *navigation.Ship,
	shipType string,
	token string,
) (string, error) {
	systemSymbol := purchasingShip.CurrentLocation().SystemSymbol

	// Find waypoints with SHIPYARD trait
	shipyardWaypoints, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "SHIPYARD")
	if err != nil {
		return "", fmt.Errorf("failed to find shipyards: %w", err)
	}

	if len(shipyardWaypoints) == 0 {
		return "", fmt.Errorf("no shipyards found in system %s", systemSymbol)
	}

	// For each shipyard, check if it sells the desired ship type
	type shipyardCandidate struct {
		waypoint string
		distance float64
	}
	var validShipyards []shipyardCandidate

	for _, waypoint := range shipyardWaypoints {
		// Get shipyard listings
		shipyardData, err := h.apiClient.GetShipyard(ctx, systemSymbol, waypoint.Symbol, token)
		if err != nil {
			// Skip waypoints where shipyard data cannot be retrieved
			continue
		}

		// Check if this shipyard sells the desired ship type
		for _, shipTypeInfo := range shipyardData.ShipTypes {
			if shipTypeInfo.Type == shipType {
				distance := purchasingShip.CurrentLocation().DistanceTo(waypoint)
				validShipyards = append(validShipyards, shipyardCandidate{
					waypoint: waypoint.Symbol,
					distance: distance,
				})
				break
			}
		}
	}

	if len(validShipyards) == 0 {
		return "", fmt.Errorf("no shipyards in system %s sell %s", systemSymbol, shipType)
	}

	// Find the nearest shipyard
	nearest := validShipyards[0]
	for _, candidate := range validShipyards[1:] {
		if candidate.distance < nearest.distance {
			nearest = candidate
		}
	}

	return nearest.waypoint, nil
}

// convertShipDataToEntity converts API ship data to domain entity
func (h *PurchaseShipHandler) convertShipDataToEntity(
	ctx context.Context,
	shipData *navigation.ShipData,
	playerID shared.PlayerID,
	waypointSymbol string,
	systemSymbol string,
) (*navigation.Ship, error) {
	// Get waypoint details (auto-fetches from API if not cached)
	waypoint, err := h.waypointProvider.GetWaypoint(ctx, waypointSymbol, systemSymbol, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", waypointSymbol, err)
	}

	// Convert cargo inventory
	var inventory []*shared.CargoItem
	for _, item := range shipData.Cargo.Inventory {
		cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
		if err != nil {
			return nil, fmt.Errorf("failed to create cargo item: %w", err)
		}
		inventory = append(inventory, cargoItem)
	}

	// Create cargo
	cargo, err := shared.NewCargo(shipData.Cargo.Capacity, shipData.Cargo.Units, inventory)
	if err != nil {
		return nil, fmt.Errorf("failed to create cargo: %w", err)
	}

	// Create fuel
	fuel, err := shared.NewFuel(shipData.FuelCurrent, shipData.FuelCapacity)
	if err != nil {
		return nil, fmt.Errorf("failed to create fuel: %w", err)
	}

	// Parse nav status - NavStatus is a string type
	navStatus := navigation.NavStatus(shipData.NavStatus)

	// Create ship using constructor
	ship, err := navigation.NewShip(
		shipData.Symbol,
		playerID,
		waypoint,
		fuel,
		shipData.FuelCapacity,
		shipData.CargoCapacity,
		cargo,
		shipData.EngineSpeed,
		shipData.FrameSymbol,
		shipData.Role,
		navStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ship: %w", err)
	}

	return ship, nil
}
