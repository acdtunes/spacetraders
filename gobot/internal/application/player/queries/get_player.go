package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// GetPlayerQuery represents a query to get a player by ID or agent symbol
type GetPlayerQuery struct {
	PlayerID    *int   // Optional: get by player ID
	AgentSymbol string // Optional: get by agent symbol
}

// GetPlayerResponse represents the result of getting a player
type GetPlayerResponse struct {
	Player *player.Player
}

// GetPlayerHandler handles the GetPlayer query
type GetPlayerHandler struct {
	playerRepo player.PlayerRepository
	apiClient  domainPorts.APIClient
}

// NewGetPlayerHandler creates a new GetPlayerHandler
func NewGetPlayerHandler(playerRepo player.PlayerRepository, apiClient domainPorts.APIClient) *GetPlayerHandler {
	return &GetPlayerHandler{
		playerRepo: playerRepo,
		apiClient:  apiClient,
	}
}

// Handle executes the GetPlayer query
func (h *GetPlayerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetPlayerQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetPlayerQuery")
	}

	// Validate that at least one identifier is provided
	if query.PlayerID == nil && query.AgentSymbol == "" {
		return nil, fmt.Errorf("either player_id or agent_symbol must be provided")
	}

	var player *player.Player
	var err error

	// Priority: PlayerID > AgentSymbol
	if query.PlayerID != nil {
		playerID, err := shared.NewPlayerID(*query.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("invalid player ID: %w", err)
		}
		player, err = h.playerRepo.FindByID(ctx, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by ID: %w", err)
		}
	} else {
		player, err = h.playerRepo.FindByAgentSymbol(ctx, query.AgentSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to find player by agent symbol: %w", err)
		}
	}

	// Get token from context (injected by middleware)
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("player token not found in context: %w", err)
	}

	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent credits from API: %w", err)
	}
	player.Credits = agent.Credits

	return &GetPlayerResponse{
		Player: player,
	}, nil
}
