package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	appPlayer "github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
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
	playerRepo     player.PlayerRepository
	apiClient      domainPorts.APIClient
	playerResolver *appPlayer.PlayerResolver
}

// NewGetPlayerHandler creates a new GetPlayerHandler
func NewGetPlayerHandler(playerRepo player.PlayerRepository, apiClient domainPorts.APIClient) *GetPlayerHandler {
	return &GetPlayerHandler{
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		playerResolver: appPlayer.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the GetPlayer query
func (h *GetPlayerHandler) Handle(ctx context.Context, request mediator.Request) (mediator.Response, error) {
	query, ok := request.(*GetPlayerQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetPlayerQuery")
	}

	// Resolve player ID using common utility
	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// Fetch player entity
	player, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Get token from context (injected by middleware)
	token, err := auth.PlayerTokenFromContext(ctx)
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
