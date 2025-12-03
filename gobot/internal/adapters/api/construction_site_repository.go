package api

import (
	"context"
	"fmt"
	"strings"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ConstructionSiteAPIRepository implements ConstructionSiteRepository using the SpaceTraders API.
type ConstructionSiteAPIRepository struct {
	apiClient  domainPorts.APIClient
	playerRepo player.PlayerRepository
}

// NewConstructionSiteRepository creates a new ConstructionSiteAPIRepository.
func NewConstructionSiteRepository(
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
) *ConstructionSiteAPIRepository {
	return &ConstructionSiteAPIRepository{
		apiClient:  apiClient,
		playerRepo: playerRepo,
	}
}

// FindByWaypoint retrieves construction site information from API.
func (r *ConstructionSiteAPIRepository) FindByWaypoint(ctx context.Context, waypointSymbol string, playerID int) (*manufacturing.ConstructionSite, error) {
	// Get player token
	p, err := r.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find player %d: %w", playerID, err)
	}

	// Extract system symbol from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5")
	systemSymbol := extractSystemFromWaypoint(waypointSymbol)

	// Call API
	data, err := r.apiClient.GetConstruction(ctx, systemSymbol, waypointSymbol, p.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get construction data for %s: %w", waypointSymbol, err)
	}

	// Convert to domain entity
	materials := make([]manufacturing.ConstructionMaterial, len(data.Materials))
	for i, mat := range data.Materials {
		materials[i] = manufacturing.NewConstructionMaterial(mat.TradeSymbol, mat.Required, mat.Fulfilled)
	}

	return manufacturing.ReconstructConstructionSite(
		data.Symbol,
		"",
		materials,
		data.IsComplete,
	), nil
}

// SupplyMaterial delivers materials to construction site.
func (r *ConstructionSiteAPIRepository) SupplyMaterial(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, playerID int) (*manufacturing.ConstructionSupplyResult, error) {
	// Get player token
	p, err := r.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find player %d: %w", playerID, err)
	}

	// Call API
	resp, err := r.apiClient.SupplyConstruction(ctx, shipSymbol, waypointSymbol, tradeSymbol, units, p.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to supply construction at %s: %w", waypointSymbol, err)
	}

	// Convert construction data to domain entity
	var construction *manufacturing.ConstructionSite
	if resp.Construction != nil {
		materials := make([]manufacturing.ConstructionMaterial, len(resp.Construction.Materials))
		for i, mat := range resp.Construction.Materials {
			materials[i] = manufacturing.NewConstructionMaterial(mat.TradeSymbol, mat.Required, mat.Fulfilled)
		}
		construction = manufacturing.ReconstructConstructionSite(
			resp.Construction.Symbol,
			"",
			materials,
			resp.Construction.IsComplete,
		)
	}

	// Calculate cargo info
	cargoCapacity := 0
	cargoUnits := 0
	if resp.Cargo != nil {
		cargoCapacity = resp.Cargo.Capacity
		cargoUnits = resp.Cargo.Units
	}

	return &manufacturing.ConstructionSupplyResult{
		Construction:   construction,
		UnitsDelivered: units,
		CargoCapacity:  cargoCapacity,
		CargoUnits:     cargoUnits,
	}, nil
}

// extractSystemFromWaypoint extracts system symbol from waypoint symbol.
// e.g., "X1-FB5-I61" -> "X1-FB5"
func extractSystemFromWaypoint(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}
