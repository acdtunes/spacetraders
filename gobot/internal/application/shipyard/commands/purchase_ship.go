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
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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

// shipyardCandidate represents a potential shipyard with its distance from current location
type shipyardCandidate struct {
	waypoint string
	distance float64
}

// PurchaseShipHandler handles the PurchaseShip command
type PurchaseShipHandler struct {
	shipRepo         navigation.ShipRepository
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	apiClient        domainPorts.APIClient
	mediator         common.Mediator
}

// NewPurchaseShipHandler creates a new PurchaseShipHandler
func NewPurchaseShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
	apiClient domainPorts.APIClient,
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

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	purchasingShip, err := h.loadPurchasingShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	shipyardWaypoint, err := h.resolveShipyardWaypoint(ctx, cmd, purchasingShip, token)
	if err != nil {
		return nil, err
	}

	purchasingShip, err = h.prepareShipForPurchase(ctx, cmd, shipyardWaypoint, purchasingShip)
	if err != nil {
		return nil, err
	}

	purchasePrice, systemSymbol, err := h.validateAndGetShipPrice(ctx, cmd, shipyardWaypoint)
	if err != nil {
		return nil, err
	}

	agentCredits, err := h.ensureSufficientCredits(ctx, token, purchasePrice)
	if err != nil {
		return nil, err
	}

	purchaseResult, err := h.apiClient.PurchaseShip(ctx, cmd.ShipType, shipyardWaypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	newShip, err := h.convertShipDataToEntity(ctx, purchaseResult.Ship, cmd.PlayerID, shipyardWaypoint, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

	return &PurchaseShipResponse{
		Ship:            newShip,
		PurchasePrice:   purchaseResult.Transaction.Price,
		AgentCredits:    agentCredits,
		TransactionTime: purchaseResult.Transaction.Timestamp,
	}, nil
}

// loadPurchasingShip fetches the ship that will make the purchase
// Returns: purchasing ship entity, error
func (h *PurchaseShipHandler) loadPurchasingShip(
	ctx context.Context,
	cmd *PurchaseShipCommand,
) (*navigation.Ship, error) {
	purchasingShip, err := h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("purchasing ship not found: %w", err)
	}
	return purchasingShip, nil
}

// resolveShipyardWaypoint determines the target shipyard (provided or auto-discovered)
// Returns: shipyard waypoint symbol, error
func (h *PurchaseShipHandler) resolveShipyardWaypoint(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	purchasingShip *navigation.Ship,
	token string,
) (string, error) {
	if cmd.ShipyardWaypoint != "" {
		return cmd.ShipyardWaypoint, nil
	}

	discoveredWaypoint, err := h.discoverNearestShipyard(ctx, purchasingShip, cmd.ShipType, token)
	if err != nil {
		return "", fmt.Errorf("failed to discover shipyard: %w", err)
	}
	return discoveredWaypoint, nil
}

// prepareShipForPurchase ensures ship is at shipyard and docked
// Combines navigation and docking steps
// Returns: prepared ship (reloaded after movements), error
func (h *PurchaseShipHandler) prepareShipForPurchase(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	var err error
	purchasingShip, err = h.navigateToShipyard(ctx, cmd, shipyardWaypoint, purchasingShip)
	if err != nil {
		return nil, err
	}

	purchasingShip, err = h.dockShipIfNeeded(ctx, cmd, purchasingShip)
	if err != nil {
		return nil, err
	}

	return purchasingShip, nil
}

// navigateToShipyard moves ship to shipyard waypoint if not already there
// Returns: reloaded ship after navigation, error
func (h *PurchaseShipHandler) navigateToShipyard(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	if purchasingShip.CurrentLocation().Symbol == shipyardWaypoint {
		return purchasingShip, nil
	}

	navCmd := &commands.NavigateRouteCommand{
		ShipSymbol:  cmd.PurchasingShipSymbol,
		Destination: shipyardWaypoint,
		PlayerID:    cmd.PlayerID,
	}
	_, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to shipyard: %w", err)
	}

	purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
	}

	return purchasingShip, nil
}

// dockShipIfNeeded docks the ship if currently in orbit
// Returns: reloaded ship after docking, error
func (h *PurchaseShipHandler) dockShipIfNeeded(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	if purchasingShip.NavStatus() != navigation.NavStatusInOrbit {
		return purchasingShip, nil
	}

	dockCmd := &commands.DockShipCommand{
		ShipSymbol: cmd.PurchasingShipSymbol,
		PlayerID:   cmd.PlayerID,
	}
	_, err := h.mediator.Send(ctx, dockCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to dock ship: %w", err)
	}

	purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after docking: %w", err)
	}

	return purchasingShip, nil
}

// validateAndGetShipPrice gets shipyard listings and validates ship availability
// Returns: purchase price for ship type, system symbol, error
func (h *PurchaseShipHandler) validateAndGetShipPrice(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
) (int, string, error) {
	systemSymbol := shared.ExtractSystemSymbol(shipyardWaypoint)

	query := &queries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: shipyardWaypoint,
		PlayerID:       cmd.PlayerID,
	}
	shipyardResp, err := h.mediator.Send(ctx, query)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	shipyardListings, ok := shipyardResp.(*queries.GetShipyardListingsResponse)
	if !ok {
		return 0, "", fmt.Errorf("invalid response type from GetShipyardListings")
	}

	listing, found := shipyardListings.Shipyard.FindListingByType(cmd.ShipType)
	if !found {
		return 0, "", fmt.Errorf("ship type %s not available at shipyard %s", cmd.ShipType, shipyardWaypoint)
	}

	return listing.PurchasePrice, systemSymbol, nil
}

// ensureSufficientCredits validates player has enough credits for purchase
// Returns: agent credits after validation, error
func (h *PurchaseShipHandler) ensureSufficientCredits(
	ctx context.Context,
	token string,
	purchasePrice int,
) (int, error) {
	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to get agent data: %w", err)
	}

	if agentData.Credits < purchasePrice {
		return 0, fmt.Errorf("insufficient credits: have %d, need %d", agentData.Credits, purchasePrice)
	}

	return agentData.Credits, nil
}

// discoverNearestShipyard discovers the nearest shipyard that sells the desired ship type
func (h *PurchaseShipHandler) discoverNearestShipyard(
	ctx context.Context,
	purchasingShip *navigation.Ship,
	shipType string,
	token string,
) (string, error) {
	systemSymbol := purchasingShip.CurrentLocation().SystemSymbol

	shipyardWaypoints, err := h.getShipyardWaypoints(ctx, systemSymbol)
	if err != nil {
		return "", err
	}

	candidates, err := h.filterShipyardsBySupportedType(
		ctx, shipyardWaypoints, systemSymbol, shipType, token, purchasingShip.CurrentLocation(),
	)
	if err != nil {
		return "", err
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no shipyards in system %s sell %s", systemSymbol, shipType)
	}

	return h.findNearestShipyard(candidates), nil
}

// getShipyardWaypoints fetches all waypoints in system with SHIPYARD trait
// Returns: waypoint array, error
func (h *PurchaseShipHandler) getShipyardWaypoints(
	ctx context.Context,
	systemSymbol string,
) ([]*shared.Waypoint, error) {
	shipyardWaypoints, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "SHIPYARD")
	if err != nil {
		return nil, fmt.Errorf("failed to find shipyards: %w", err)
	}

	if len(shipyardWaypoints) == 0 {
		return nil, fmt.Errorf("no shipyards found in system %s", systemSymbol)
	}

	return shipyardWaypoints, nil
}

// filterShipyardsBySupportedType finds shipyards that sell the desired ship type
// Returns: array of shipyard candidates with distances
func (h *PurchaseShipHandler) filterShipyardsBySupportedType(
	ctx context.Context,
	waypoints []*shared.Waypoint,
	systemSymbol string,
	shipType string,
	token string,
	currentLocation *shared.Waypoint,
) ([]shipyardCandidate, error) {
	var validShipyards []shipyardCandidate

	for _, waypoint := range waypoints {
		sells, err := h.doesShipyardSellType(ctx, systemSymbol, waypoint, shipType, token)
		if err != nil {
			continue
		}

		if sells {
			distance := currentLocation.DistanceTo(waypoint)
			validShipyards = append(validShipyards, shipyardCandidate{
				waypoint: waypoint.Symbol,
				distance: distance,
			})
		}
	}

	return validShipyards, nil
}

// doesShipyardSellType checks if a specific shipyard sells the ship type
// Returns: true if shipyard supports type, false otherwise, error
func (h *PurchaseShipHandler) doesShipyardSellType(
	ctx context.Context,
	systemSymbol string,
	waypoint *shared.Waypoint,
	shipType string,
	token string,
) (bool, error) {
	shipyardData, err := h.apiClient.GetShipyard(ctx, systemSymbol, waypoint.Symbol, token)
	if err != nil {
		return false, err
	}

	for _, shipTypeInfo := range shipyardData.ShipTypes {
		if shipTypeInfo.Type == shipType {
			return true, nil
		}
	}

	return false, nil
}

// findNearestShipyard selects the closest shipyard from candidates
// Returns: waypoint symbol of nearest shipyard
func (h *PurchaseShipHandler) findNearestShipyard(
	candidates []shipyardCandidate,
) string {
	nearest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.distance < nearest.distance {
			nearest = candidate
		}
	}
	return nearest.waypoint
}

// convertShipDataToEntity converts API ship data to domain entity
func (h *PurchaseShipHandler) convertShipDataToEntity(
	ctx context.Context,
	shipData *navigation.ShipData,
	playerID shared.PlayerID,
	waypointSymbol string,
	systemSymbol string,
) (*navigation.Ship, error) {
	waypoint, err := h.getWaypointDetails(ctx, waypointSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	cargoItems, err := h.convertInventoryItems(shipData.Cargo.Inventory)
	if err != nil {
		return nil, err
	}

	cargo, fuel, navStatus, err := h.createShipValueObjects(shipData, cargoItems)
	if err != nil {
		return nil, err
	}

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

// getWaypointDetails fetches waypoint data for ship's current location
// Returns: waypoint entity, error
func (h *PurchaseShipHandler) getWaypointDetails(
	ctx context.Context,
	waypointSymbol string,
	systemSymbol string,
	playerID shared.PlayerID,
) (*shared.Waypoint, error) {
	waypoint, err := h.waypointProvider.GetWaypoint(ctx, waypointSymbol, systemSymbol, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", waypointSymbol, err)
	}
	return waypoint, nil
}

// convertInventoryItems converts API cargo data to domain cargo items
// Returns: cargo item array, error
func (h *PurchaseShipHandler) convertInventoryItems(
	inventoryData []shared.CargoItem,
) ([]*shared.CargoItem, error) {
	var inventory []*shared.CargoItem
	for _, item := range inventoryData {
		cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
		if err != nil {
			return nil, fmt.Errorf("failed to create cargo item: %w", err)
		}
		inventory = append(inventory, cargoItem)
	}
	return inventory, nil
}

// createShipValueObjects creates domain value objects from API data
// Returns: cargo, fuel, navStatus value objects, error
func (h *PurchaseShipHandler) createShipValueObjects(
	shipData *navigation.ShipData,
	cargoItems []*shared.CargoItem,
) (*shared.Cargo, *shared.Fuel, navigation.NavStatus, error) {
	cargo, err := shared.NewCargo(shipData.Cargo.Capacity, shipData.Cargo.Units, cargoItems)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create cargo: %w", err)
	}

	fuel, err := shared.NewFuel(shipData.FuelCurrent, shipData.FuelCapacity)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create fuel: %w", err)
	}

	navStatus := navigation.NavStatus(shipData.NavStatus)

	return cargo, fuel, navStatus, nil
}
