package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// ListShipsQuery represents a query to list all ships for a player
type ListShipsQuery struct {
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// ListShipsResponse represents the result of listing ships
type ListShipsResponse struct {
	Ships []*navigation.Ship
}

// ListShipsHandler handles the ListShips query
type ListShipsHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
}

// NewListShipsHandler creates a new ListShipsHandler
func NewListShipsHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *ListShipsHandler {
	return &ListShipsHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
	}
}

// Handle executes the ListShips query
func (h *ListShipsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*ListShipsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ListShipsQuery")
	}

	// Validate that at least one identifier is provided
	if query.PlayerID == nil && query.AgentSymbol == "" {
		return nil, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	// Resolve player ID if agent symbol is provided
	playerID := 0
	if query.PlayerID != nil {
		playerID = *query.PlayerID
	} else {
		player, err := h.playerRepo.FindByAgentSymbol(ctx, query.AgentSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by agent symbol: %w", err)
		}
		playerID = player.ID
	}

	// Get all ships for the player
	ships, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	return &ListShipsResponse{
		Ships: ships,
	}, nil
}
