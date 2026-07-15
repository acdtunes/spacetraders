package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WarpNavigator is the driven port RouteExecutor uses to execute a single
// off-gate warp leg (sp-0xd0). It is the ONE boundary the warp path crosses to
// the live API, kept narrow (ISP, mirroring gategraph's gateAPI) so the executor's
// warp behaviour - the fuel-safety guard and chart-on-arrival - is unit-testable
// against a tiny fake warp API with no HTTP.
//
// Warp puts the ship IN_TRANSIT toward a waypoint in ANOTHER system, consuming
// fuel by inter-system distance. The returned Result mirrors a navigate leg: the
// destination waypoint, the arrival time, and the post-warp fuel state (which the
// executor folds back into the ship via UpdateFuelFromAPI).
type WarpNavigator interface {
	Warp(ctx context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*domainNavigation.Result, error)
}

// warpShipAPI is the narrow slice of the SpaceTraders API the warp navigator
// touches: the single POST /my/ships/{symbol}/warp call. Narrowing it (vs. the
// full ports.APIClient) states exactly what the adapter needs and keeps the fake
// in tests one method wide. The concrete *api.SpaceTradersClient satisfies it.
type warpShipAPI interface {
	WarpShip(ctx context.Context, symbol, destination, token string) (*domainNavigation.Result, error)
}

// APIWarpNavigator is the production WarpNavigator: it resolves the player token
// from the request context (exactly as MarketScanner does) and issues the live
// warp call. It holds no per-request state, so a single instance is shared by the
// route executor for the daemon's lifetime.
type APIWarpNavigator struct {
	apiClient warpShipAPI
}

// NewAPIWarpNavigator wires the production warp navigator over the live API client.
func NewAPIWarpNavigator(apiClient warpShipAPI) *APIWarpNavigator {
	return &APIWarpNavigator{apiClient: apiClient}
}

// Warp resolves the token from context and executes the live warp leg. The
// caller (RouteExecutor.executeWarpLeg) has already enforced the fuel-safety
// guard, so a rejection here is a genuine API failure surfaced to the caller.
func (a *APIWarpNavigator) Warp(ctx context.Context, ship *domainNavigation.Ship, destination *shared.Waypoint, _ shared.PlayerID) (*domainNavigation.Result, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get player token for warp: %w", err)
	}
	return a.apiClient.WarpShip(ctx, ship.ShipSymbol(), destination.Symbol, token)
}
