package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// contractStandbyDemandProvider implements appContract.StandbyDemandProvider for the contract
// fleet coordinator's between-legs homing (epic sp-9le3x C2c). It REUSES the auto-scaler's
// home-market read (homeMarkets — ONE demand definition, no drift between the scaler's buy-order
// homing and the coordinator's re-homing), classifies this era's central parks by market role +
// geometry, and coord-dedups them so idle hulls spread across distinct LOCATIONS (not co-located
// waypoints like a planet and its moon). It computes NO plan and holds NO state — the coordinator
// resolves it live each homing pass. RULINGS #14 (home-system only, via the scaler's home anchor);
// RULINGS #3 (a READ, never a config write).
type contractStandbyDemandProvider struct {
	resolver *contractScalerRoleResolver
}

var _ appContract.StandbyDemandProvider = (*contractStandbyDemandProvider)(nil)

// NewContractStandbyDemandProvider wires the coordinator's demand reader over the SAME three
// read-ports the scaler's RoleResolver uses (home-system anchor, waypoint geometry, market roles).
// The waypoint lister is assigned only when non-nil so a typed-nil pointer cannot defeat the
// reader's nil guard (fail-safe empty era) with a panic — mirroring NewContractScalerCoordinatorHandler.
func NewContractStandbyDemandProvider(shipRepo navigation.ShipRepository, waypointRepo *persistence.GormWaypointRepository, marketRepo market.MarketRepository) *contractStandbyDemandProvider {
	resolver := &contractScalerRoleResolver{
		home:    &contractScalerShipHomeReader{shipRepo: shipRepo},
		markets: marketRepo,
	}
	if waypointRepo != nil {
		resolver.waypoints = waypointRepo
	}
	return &contractStandbyDemandProvider{resolver: resolver}
}

// StandbyDemand resolves this era's central-park demand map (coord-deduped, import-volume ranked)
// for the player's home system. An empty era (unresolvable/unscanned home) yields an empty map
// (no error) so homing degrades to plain occupancy+nearest balancing — a positioning read that
// fails SAFE, never a spend.
func (p *contractStandbyDemandProvider) StandbyDemand(ctx context.Context, playerID int) (map[string]float64, error) {
	markets, demand, err := p.resolver.homeMarkets(ctx, playerID)
	if err != nil {
		return nil, err
	}
	roles := contractscaler.ResolveRoles(markets)

	coords := make(map[string][2]float64, len(markets))
	for _, m := range markets {
		coords[m.Symbol] = [2]float64{m.X, m.Y}
	}
	parks := make([]contractscaler.CentralPark, 0, len(roles.CentralParks))
	for _, symbol := range roles.CentralParks {
		coord := coords[symbol]
		parks = append(parks, contractscaler.CentralPark{Symbol: symbol, X: coord[0], Y: coord[1], Demand: demand[symbol]})
	}
	return contractscaler.DedupeCoLocatedParks(parks), nil
}
