package player

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// RegisterPlayerCommand represents a command to register a new player
type RegisterPlayerCommand struct {
	AgentSymbol string
	Token       string                 // JWT token from SpaceTraders API registration
	Metadata    map[string]interface{} // Optional metadata (faction, headquarters, etc.)
}

// RegisterPlayerResponse represents the result of registering a player
type RegisterPlayerResponse struct {
	Player *common.Player
}

// RegisterPlayerHandler handles the RegisterPlayer command
type RegisterPlayerHandler struct {
	playerRepo common.PlayerRepository
}

// NewRegisterPlayerHandler creates a new RegisterPlayerHandler
func NewRegisterPlayerHandler(playerRepo common.PlayerRepository) *RegisterPlayerHandler {
	return &RegisterPlayerHandler{
		playerRepo: playerRepo,
	}
}

// Handle executes the RegisterPlayer command
func (h *RegisterPlayerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RegisterPlayerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RegisterPlayerCommand")
	}

	// Validate inputs
	if cmd.AgentSymbol == "" {
		return nil, fmt.Errorf("agent_symbol is required")
	}
	if cmd.Token == "" {
		return nil, fmt.Errorf("token is required")
	}

	// Create player entity
	player := &common.Player{
		AgentSymbol: cmd.AgentSymbol,
		Token:       cmd.Token,
		Metadata:    cmd.Metadata,
		Credits:     0, // Will be synced from API later
	}

	// Save to database
	if err := h.playerRepo.Save(ctx, player); err != nil {
		return nil, fmt.Errorf("failed to save player: %w", err)
	}

	return &RegisterPlayerResponse{
		Player: player,
	}, nil
}

// SyncPlayerCommand represents a command to sync player data from API
type SyncPlayerCommand struct {
	PlayerID int
}

// SyncPlayerResponse represents the result of syncing player data
type SyncPlayerResponse struct {
	Player  *common.Player
	Updated bool
}

// SyncPlayerHandler handles the SyncPlayer command
// This syncs player credits and metadata from the SpaceTraders API
type SyncPlayerHandler struct {
	playerRepo common.PlayerRepository
	apiClient  common.APIClient
}

// NewSyncPlayerHandler creates a new SyncPlayerHandler
func NewSyncPlayerHandler(
	playerRepo common.PlayerRepository,
	apiClient common.APIClient,
) *SyncPlayerHandler {
	return &SyncPlayerHandler{
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the SyncPlayer command
func (h *SyncPlayerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SyncPlayerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *SyncPlayerCommand")
	}

	// Get player from database
	player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Fetch agent data from API
	agentData, err := h.apiClient.GetAgent(ctx, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent data from API: %w", err)
	}

	// Update player with API data
	updated := false
	if player.Credits != agentData.Credits {
		player.Credits = agentData.Credits
		updated = true
	}

	// Update metadata
	if player.Metadata == nil {
		player.Metadata = make(map[string]interface{})
	}
	player.Metadata["account_id"] = agentData.AccountID
	player.Metadata["headquarters"] = agentData.Headquarters
	player.Metadata["starting_faction"] = agentData.StartingFaction
	player.Metadata["last_synced"] = time.Now().UTC().Format(time.RFC3339)
	updated = true

	// Save updates
	if updated {
		if err := h.playerRepo.Save(ctx, player); err != nil {
			return nil, fmt.Errorf("failed to save player updates: %w", err)
		}
	}

	return &SyncPlayerResponse{
		Player:  player,
		Updated: updated,
	}, nil
}
