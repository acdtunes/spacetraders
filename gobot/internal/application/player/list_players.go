package player

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// ListPlayersCommand represents a command to list all players
type ListPlayersCommand struct {
	// No parameters - lists all players
}

// ListPlayersResponse represents the result of listing players
type ListPlayersResponse struct {
	Players []*player.Player
}

// ListPlayersHandler handles the ListPlayers command
type ListPlayersHandler struct {
	playerRepo player.PlayerRepository
}

// NewListPlayersHandler creates a new ListPlayersHandler
func NewListPlayersHandler(playerRepo player.PlayerRepository) *ListPlayersHandler {
	return &ListPlayersHandler{
		playerRepo: playerRepo,
	}
}

// Handle executes the ListPlayers command
func (h *ListPlayersHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	_, ok := request.(*ListPlayersCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ListPlayersCommand")
	}

	// TODO: Add ListAll method to PlayerRepository
	// For now, return empty list
	return &ListPlayersResponse{
		Players: []*player.Player{},
	}, nil
}
