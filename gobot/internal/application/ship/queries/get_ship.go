package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// GetShipQuery represents a query to get ship details
type GetShipQuery struct {
	ShipSymbol  string // Required: ship symbol to retrieve
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// GetShipResponse represents the result of getting a ship
type GetShipResponse struct {
	Ship *navigation.Ship
}

// GetShipHandler handles the GetShip query
type GetShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewGetShipHandler creates a new GetShipHandler
func NewGetShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *GetShipHandler {
	return &GetShipHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the GetShip query
func (h *GetShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetShipQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetShipQuery")
	}

	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return &GetShipResponse{
		Ship: ship,
	}, nil
}
