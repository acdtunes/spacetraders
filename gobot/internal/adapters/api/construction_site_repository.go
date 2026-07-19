package api

import (
	"context"
	"fmt"

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
	p, err := r.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find player %d: %w", playerID, err)
	}

	systemSymbol := extractSystemSymbol(waypointSymbol)

	data, err := r.apiClient.GetConstruction(ctx, systemSymbol, waypointSymbol, p.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get construction data for %s: %w", waypointSymbol, err)
	}

	materials := toConstructionMaterials(data.Materials)

	return manufacturing.ReconstructConstructionSite(
		data.Symbol,
		"",
		materials,
		data.IsComplete,
	), nil
}

// SupplyMaterial delivers materials to construction site.
func (r *ConstructionSiteAPIRepository) SupplyMaterial(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, playerID int) (*manufacturing.ConstructionSupplyResult, error) {
	p, err := r.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find player %d: %w", playerID, err)
	}

	resp, err := r.apiClient.SupplyConstruction(ctx, shipSymbol, waypointSymbol, tradeSymbol, units, p.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to supply construction at %s: %w", waypointSymbol, err)
	}

	var construction *manufacturing.ConstructionSite
	if resp.Construction != nil {
		materials := toConstructionMaterials(resp.Construction.Materials)
		construction = manufacturing.ReconstructConstructionSite(
			resp.Construction.Symbol,
			"",
			materials,
			resp.Construction.IsComplete,
		)
	}

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

// toConstructionMaterials converts the API port's construction-material rows into
// domain ConstructionMaterial values.
func toConstructionMaterials(materials []domainPorts.ConstructionMaterialData) []manufacturing.ConstructionMaterial {
	result := make([]manufacturing.ConstructionMaterial, len(materials))
	for i, mat := range materials {
		result[i] = manufacturing.NewConstructionMaterial(mat.TradeSymbol, mat.Required, mat.Fulfilled)
	}
	return result
}
