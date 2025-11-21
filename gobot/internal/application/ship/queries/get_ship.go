package queries

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

	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.resolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &GetShipResponse{
		Ship: ship,
	}, nil
}

func (h *GetShipHandler) resolvePlayerID(ctx context.Context, playerID *int, agentSymbol string) (shared.PlayerID, error) {
	if playerID == nil && agentSymbol == "" {
		return shared.PlayerID{}, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	if playerID != nil {
		pid, err := shared.NewPlayerID(*playerID)
		if err != nil {
			return shared.PlayerID{}, fmt.Errorf("invalid player ID: %w", err)
		}
		return pid, nil
	}

	player, err := h.playerRepo.FindByAgentSymbol(ctx, agentSymbol)
	if err != nil {
		return shared.PlayerID{}, fmt.Errorf("failed to find player by agent symbol: %w", err)
	}
	return player.ID, nil
}
