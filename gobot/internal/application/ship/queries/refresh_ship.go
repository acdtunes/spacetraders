package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// RefreshShipQuery forces a resync of a ship's state from the SpaceTraders API,
// overwriting the daemon's local cache. Unlike GetShip (which serves the cache),
// RefreshShip reconciles a desynced cache without a daemon restart.
type RefreshShipQuery struct {
	ShipSymbol  string // Required: ship symbol to refresh
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// RefreshShipResponse holds the server-true ship state after reconciliation.
type RefreshShipResponse struct {
	Ship *navigation.Ship
}

// RefreshShipHandler handles the RefreshShip query
type RefreshShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewRefreshShipHandler creates a new RefreshShipHandler
func NewRefreshShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *RefreshShipHandler {
	return &RefreshShipHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the RefreshShip query
func (h *RefreshShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*RefreshShipQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RefreshShipQuery")
	}

	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// Force a fresh GET /my/ships/<symbol> and write it through to the cache,
	// overwriting stale cargo + nav state. This is the reconciliation a daemon
	// restart performs today, exposed as a Captain-accessible verb.
	ship, err := h.shipRepo.SyncShipFromAPI(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh ship: %w", err)
	}

	return &RefreshShipResponse{
		Ship: ship,
	}, nil
}
