package queries

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
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewListShipsHandler creates a new ListShipsHandler
func NewListShipsHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *ListShipsHandler {
	return &ListShipsHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the ListShips query
func (h *ListShipsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*ListShipsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ListShipsQuery")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
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
