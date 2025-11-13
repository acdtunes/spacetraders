package ports

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// APIClient defines operations for interacting with SpaceTraders API
// This is in infrastructure/ports because it's an external service interface
type APIClient interface {
	// Ship operations
	GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error)
	ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error)
	NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.NavigationResult, error)
	OrbitShip(ctx context.Context, symbol, token string) error
	DockShip(ctx context.Context, symbol, token string) error
	RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error)
	SetFlightMode(ctx context.Context, symbol, flightMode, token string) error

	// Player operations
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)

	// Waypoint operations
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error)
}
