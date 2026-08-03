package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	// The local `player` variable in Handle shadows the package name, so identity helpers are
	// reached through an alias rather than renaming a variable used throughout the file.
	domainPlayer "github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

// agentIdentityReader is the ONE method this handler needs from the API client: the /my/agent
// read that carries the agent's durable identity. Narrowed from the full domainPorts.APIClient
// (which the constructor still accepts, and which satisfies this) so the sync can be exercised
// against a small fake instead of a ~50-method stub — the reason this handler had no tests while
// it was the sole writer of players.metadata.headquarters.
type agentIdentityReader interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}

// SyncPlayerHandler handles the SyncPlayer command
// This syncs player credits and metadata from the SpaceTraders API
type SyncPlayerHandler struct {
	playerRepo player.PlayerRepository
	apiClient  agentIdentityReader
}

// NewSyncPlayerHandler creates a new SyncPlayerHandler
func NewSyncPlayerHandler(
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
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

	// Durable identity (headquarters / starting_faction / account_id) goes through the ONE shared
	// merge in the player domain: it preserves every other key in the row and reports whether
	// anything actually changed.
	//
	// The write is CONDITIONAL on that answer. An unconditional write — `updated = true`
	// assigned outright, with a last_synced stamp refreshed on every call guaranteeing the
	// row differed — would stop this handler being run on a schedule without rewriting an
	// unchanged row every time. Because the durable fix re-asserts identity on
	// EVERY daemon boot, that thrash is no longer hypothetical.
	metadata, identityChanged := domainPlayer.MergeAgentIdentity(player.Metadata, agentData)
	player.Metadata = metadata
	if identityChanged {
		// last_synced is an operator-facing breadcrumb (displayed by `player show`), deliberately
		// excluded from the change decision above and stamped only when a real change is being
		// persisted — a timestamp that moved on every call would defeat the idempotency it sits next to.
		player.Metadata["last_synced"] = time.Now().UTC().Format(time.RFC3339)
		updated = true
	}

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
