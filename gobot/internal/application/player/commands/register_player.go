package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// RegisterPlayerCommand represents a command to register a new player
type RegisterPlayerCommand struct {
	AgentSymbol string
	Token       string                 // JWT token from SpaceTraders API registration
	Metadata    map[string]interface{} // Optional metadata (faction, headquarters, etc.)
}

// RegisterPlayerResponse represents the result of registering a player
type RegisterPlayerResponse struct {
	Player *player.Player
}

// RegisterPlayerHandler handles the RegisterPlayer command
type RegisterPlayerHandler struct {
	playerRepo player.PlayerRepository
}

// NewRegisterPlayerHandler creates a new RegisterPlayerHandler
func NewRegisterPlayerHandler(playerRepo player.PlayerRepository) *RegisterPlayerHandler {
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

	if cmd.AgentSymbol == "" {
		return nil, fmt.Errorf("agent_symbol is required")
	}
	if cmd.Token == "" {
		return nil, fmt.Errorf("token is required")
	}

	player := &player.Player{
		AgentSymbol: cmd.AgentSymbol,
		Token:       cmd.Token,
		Metadata:    cmd.Metadata,
		Credits:     0, // Will be synced from API later
	}

	if err := h.playerRepo.Add(ctx, player); err != nil {
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
	Player  *player.Player
	Updated bool
}

// SyncPlayerHandler handles the SyncPlayer command
// This syncs player credits and metadata from the SpaceTraders API
type SyncPlayerHandler struct {
	playerRepo player.PlayerRepository
	apiClient  infraPorts.APIClient
}

// NewSyncPlayerHandler creates a new SyncPlayerHandler
func NewSyncPlayerHandler(
	playerRepo player.PlayerRepository,
	apiClient infraPorts.APIClient,
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

	// Get token from context (injected by middleware)
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("player token not found in context: %w", err)
	}

	playerID, err := shared.NewPlayerID(cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}
	player, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent data from API: %w", err)
	}

	updated := false
	if player.Credits != agentData.Credits {
		player.Credits = agentData.Credits
		updated = true
	}

	if player.Metadata == nil {
		player.Metadata = make(map[string]interface{})
	}
	player.Metadata["account_id"] = agentData.AccountID
	player.Metadata["headquarters"] = agentData.Headquarters
	player.Metadata["starting_faction"] = agentData.StartingFaction
	player.Metadata["last_synced"] = time.Now().UTC().Format(time.RFC3339)
	updated = true

	if updated {
		if err := h.playerRepo.Add(ctx, player); err != nil {
			return nil, fmt.Errorf("failed to save player updates: %w", err)
		}
	}

	return &SyncPlayerResponse{
		Player:  player,
		Updated: updated,
	}, nil
}
