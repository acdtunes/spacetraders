package api

import (
	"context"
	"fmt"
	"strings"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// GetJumpGate retrieves information about a jump gate waypoint
func (c *SpaceTradersClient) GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.JumpGateData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/jump-gate", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol      string   `json:"symbol"`
			Connections []string `json:"connections"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get jump gate: %w", err)
	}

	return &domainPorts.JumpGateData{
		Symbol:      response.Data.Symbol,
		Connections: response.Data.Connections,
	}, nil
}

// GetWaypoint reads a single waypoint's detail. Only the fields the gate graph
// needs are decoded: the symbol and isUnderConstruction (whether a jump gate is
// still being built). The jump-gate connections list carries symbols only, so the
// build state of a connected gate is resolved with this per-waypoint read —
// an unbuilt gate is a dead edge the BFS must never route through.
func (c *SpaceTradersClient) GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol              string  `json:"symbol"`
			Type                string  `json:"type"`
			X                   float64 `json:"x"`
			Y                   float64 `json:"y"`
			IsUnderConstruction bool    `json:"isUnderConstruction"`
			Traits              []struct {
				Symbol string `json:"symbol"`
			} `json:"traits"`
			Orbitals []struct {
				Symbol string `json:"symbol"`
			} `json:"orbitals"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", waypointSymbol, err)
	}

	traits := make([]string, 0, len(response.Data.Traits))
	for _, trait := range response.Data.Traits {
		traits = append(traits, trait.Symbol)
	}
	orbitals := make([]string, 0, len(response.Data.Orbitals))
	for _, orbital := range response.Data.Orbitals {
		orbitals = append(orbitals, orbital.Symbol)
	}

	return &domainPorts.WaypointDetail{
		Symbol:              response.Data.Symbol,
		Type:                response.Data.Type,
		X:                   response.Data.X,
		Y:                   response.Data.Y,
		IsUnderConstruction: response.Data.IsUnderConstruction,
		Traits:              traits,
		Orbitals:            orbitals,
	}, nil
}

// ListWaypoints retrieves waypoints for a system with pagination
func (c *SpaceTradersClient) ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error) {
	path := fmt.Sprintf("/systems/%s/waypoints?page=%d&limit=%d", systemSymbol, page, limit)

	var response struct {
		Data []struct {
			Symbol   string                   `json:"symbol"`
			Type     string                   `json:"type"`
			X        float64                  `json:"x"`
			Y        float64                  `json:"y"`
			Traits   []map[string]interface{} `json:"traits"`
			Orbitals []map[string]string      `json:"orbitals"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
			Page  int `json:"page"`
			Limit int `json:"limit"`
		} `json:"meta"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", err)
	}

	waypoints := make([]system.WaypointAPIData, len(response.Data))
	for i, wp := range response.Data {
		waypoints[i] = system.WaypointAPIData{
			Symbol:   wp.Symbol,
			Type:     wp.Type,
			X:        wp.X,
			Y:        wp.Y,
			Traits:   wp.Traits,
			Orbitals: wp.Orbitals,
		}
	}

	return &system.WaypointsListResponse{
		Data: waypoints,
		Meta: system.PaginationMeta{
			Total: response.Meta.Total,
			Page:  response.Meta.Page,
			Limit: response.Meta.Limit,
		},
	}, nil
}

// GetConstruction retrieves construction site information for a waypoint
func (c *SpaceTradersClient) GetConstruction(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ConstructionData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/construction", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol    string `json:"symbol"`
			Materials []struct {
				TradeSymbol string `json:"tradeSymbol"`
				Required    int    `json:"required"`
				Fulfilled   int    `json:"fulfilled"`
			} `json:"materials"`
			IsComplete bool `json:"isComplete"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get construction: %w", err)
	}

	materials := make([]domainPorts.ConstructionMaterialData, len(response.Data.Materials))
	for i, mat := range response.Data.Materials {
		materials[i] = domainPorts.ConstructionMaterialData{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
		}
	}

	return &domainPorts.ConstructionData{
		Symbol:     response.Data.Symbol,
		Materials:  materials,
		IsComplete: response.Data.IsComplete,
	}, nil
}

// SupplyConstruction delivers materials to a construction site
func (c *SpaceTradersClient) SupplyConstruction(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, token string) (*domainPorts.ConstructionSupplyResponse, error) {
	// Construction supply is a waypoint-scoped endpoint; the ship is identified
	// via the request body, not the path.
	systemSymbol := extractSystemSymbol(waypointSymbol)
	path := fmt.Sprintf("/systems/%s/waypoints/%s/construction/supply", systemSymbol, waypointSymbol)

	body := map[string]interface{}{
		"shipSymbol":  shipSymbol,
		"tradeSymbol": tradeSymbol,
		"units":       units,
	}

	var response struct {
		Data struct {
			Construction struct {
				Symbol    string `json:"symbol"`
				Materials []struct {
					TradeSymbol string `json:"tradeSymbol"`
					Required    int    `json:"required"`
					Fulfilled   int    `json:"fulfilled"`
				} `json:"materials"`
				IsComplete bool `json:"isComplete"`
			} `json:"construction"`
			Cargo cargoDTO `json:"cargo"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to supply construction (system=%s): %w", systemSymbol, err)
	}

	materials := make([]domainPorts.ConstructionMaterialData, len(response.Data.Construction.Materials))
	for i, mat := range response.Data.Construction.Materials {
		materials[i] = domainPorts.ConstructionMaterialData{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
		}
	}

	return &domainPorts.ConstructionSupplyResponse{
		Construction: &domainPorts.ConstructionData{
			Symbol:     response.Data.Construction.Symbol,
			Materials:  materials,
			IsComplete: response.Data.Construction.IsComplete,
		},
		Cargo: response.Data.Cargo.toCargoData(),
	}, nil
}

// extractSystemSymbol extracts the system symbol from a waypoint symbol
// e.g., "X1-FB5-I61" -> "X1-FB5"
func extractSystemSymbol(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}
