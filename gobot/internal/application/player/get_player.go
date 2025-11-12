package player

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// GetPlayerCommand represents a command to get a player by ID or agent symbol
type GetPlayerCommand struct {
	PlayerID    *int   // Optional: get by player ID
	AgentSymbol string // Optional: get by agent symbol
}

// GetPlayerResponse represents the result of getting a player
type GetPlayerResponse struct {
	Player *common.Player
}

// GetPlayerHandler handles the GetPlayer command
type GetPlayerHandler struct {
	playerRepo common.PlayerRepository
	apiClient  common.APIClient
}

// NewGetPlayerHandler creates a new GetPlayerHandler
func NewGetPlayerHandler(playerRepo common.PlayerRepository, apiClient common.APIClient) *GetPlayerHandler {
	return &GetPlayerHandler{
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the GetPlayer command
func (h *GetPlayerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*GetPlayerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetPlayerCommand")
	}

	// Validate that at least one identifier is provided
	if cmd.PlayerID == nil && cmd.AgentSymbol == "" {
		return nil, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	var player *common.Player
	var err error

	// Priority: PlayerID > AgentSymbol
	if cmd.PlayerID != nil {
		player, err = h.playerRepo.FindByID(ctx, *cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by ID: %w", err)
		}
	} else {
		player, err = h.playerRepo.FindByAgentSymbol(ctx, cmd.AgentSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by agent symbol: %w", err)
		}
	}

	// Fetch fresh credits from API
	// NOTE: Credits are never persisted in DB - always fetched live from SpaceTraders API
	agent, err := h.apiClient.GetAgent(ctx, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent credits from API: %w", err)
	}
	player.Credits = agent.Credits

	return &GetPlayerResponse{
		Player: player,
	}, nil
}
