package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// GetShipQuery represents a query to get ship details
type GetShipQuery struct {
	ShipSymbol  string // Required: ship symbol to retrieve
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// GetShipResponse represents the result of getting a ship
type GetShipResponse struct {
	Ship *navigation.Ship
}

// GetShipHandler handles the GetShip query
type GetShipHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
}

// NewGetShipHandler creates a new GetShipHandler
func NewGetShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *GetShipHandler {
	return &GetShipHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
	}
}

// Handle executes the GetShip query
func (h *GetShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetShipQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetShipQuery")
	}

	// Validate ship symbol is provided
	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	// Validate that at least one identifier is provided
	if query.PlayerID == nil && query.AgentSymbol == "" {
		return nil, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	// Resolve player ID if agent symbol is provided
	var playerID shared.PlayerID
	if query.PlayerID != nil {
		pid, err := shared.NewPlayerID(*query.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("invalid player ID: %w", err)
		}
		playerID = pid
	} else {
		player, err := h.playerRepo.FindByAgentSymbol(ctx, query.AgentSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by agent symbol: %w", err)
		}
		playerID = player.ID
	}

	// Get the ship
	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &GetShipResponse{
		Ship: ship,
	}, nil
}
