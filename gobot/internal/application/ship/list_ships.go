package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

	playerID, err := h.resolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	ships, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	return &ListShipsResponse{
		Ships: ships,
	}, nil
}

func (h *ListShipsHandler) resolvePlayerID(ctx context.Context, playerID *int, agentSymbol string) (shared.PlayerID, error) {
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
