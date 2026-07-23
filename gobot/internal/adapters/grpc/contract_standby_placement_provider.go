package grpc

import (
	"context"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// contractStandbyPlacementProvider implements appContract.StandbyPlacementProvider for the contract
// fleet coordinator's between-legs homing + the idle-arb re-home sweep (epic sp-9le3x / sp-mtgje). It
// REUSES the auto-scaler's home-market read (homeMarkets), classifies this era's central parks by
// market role + geometry, coord-dedups them (so slots are distinct LOCATIONS), and returns the ≤6
// FIXED delivery placement slots (TopDeliverySlots) — the SAME selection the scaler buys against, via
// the SHARED dedupedCentralParkSymbols helper, so the two positioning consumers agree on ONE slot set
// with no drift. The runtime homing zips hulls onto these slots by symbol — NO demand. It computes NO
// plan and holds NO state; the coordinator resolves it live each pass. RULINGS #14 (home-system only);
// RULINGS #3 (a READ, never a config write).
type contractStandbyPlacementProvider struct {
	resolver *contractScalerRoleResolver
}

var _ appContract.StandbyPlacementProvider = (*contractStandbyPlacementProvider)(nil)

// NewContractStandbyPlacementProvider wires the coordinator's placement reader over the SAME three
// read-ports the scaler's RoleResolver uses (home-system anchor, waypoint geometry, market roles). The
// waypoint lister is assigned only when non-nil so a typed-nil pointer cannot defeat the reader's nil
// guard (fail-safe empty era) with a panic — mirroring NewContractScalerCoordinatorHandler.
func NewContractStandbyPlacementProvider(shipRepo navigation.ShipRepository, waypointRepo *persistence.GormWaypointRepository, marketRepo market.MarketRepository) *contractStandbyPlacementProvider {
	resolver := &contractScalerRoleResolver{
		home:    &contractScalerShipHomeReader{shipRepo: shipRepo},
		markets: marketRepo,
	}
	if waypointRepo != nil {
		resolver.waypoints = waypointRepo
	}
	return &contractStandbyPlacementProvider{resolver: resolver}
}

// StandbyPlacement resolves this era's ≤6 FIXED delivery placement slots (coord-deduped central parks,
// ranked highest-demand first, capped at the knee) for the player's home system — the SAME selection
// the scaler buys against. An empty era (unresolvable/unscanned home) yields an empty slice (no error)
// so homing keeps its passed set — a positioning read that fails SAFE, never a spend.
func (p *contractStandbyPlacementProvider) StandbyPlacement(ctx context.Context, playerID int) ([]string, error) {
	markets, demand, err := p.resolver.homeMarkets(ctx, playerID)
	if err != nil {
		return nil, err
	}
	roles := contractscaler.ResolveRoles(markets)
	deduped := dedupedCentralParkSymbols(roles, markets, demand)
	return contractscaler.TopDeliverySlots(deduped, demand), nil
}

// dedupedCentralParkSymbols returns the era's central-park symbols coord-deduped to one representative
// per LOCATION (highest-demand, symbol-tiebroken — DedupeCoLocatedParks), sorted for determinism. It is
// the SHARED selection input behind BOTH the scaler's role lookup (contractScalerRoleResolver.ResolveRoles)
// AND the coordinator's placement provider, so the fixed placement TopDeliverySlots derives from ONE
// deduped park set with no drift between the buy-order homing and the between-legs re-homing.
func dedupedCentralParkSymbols(roles contractscaler.EraRoles, markets []contractscaler.WaypointMarket, demand map[string]float64) []string {
	coords := make(map[string][2]float64, len(markets))
	for _, m := range markets {
		coords[m.Symbol] = [2]float64{m.X, m.Y}
	}
	parks := make([]contractscaler.CentralPark, 0, len(roles.CentralParks))
	for _, symbol := range roles.CentralParks {
		coord := coords[symbol]
		parks = append(parks, contractscaler.CentralPark{Symbol: symbol, X: coord[0], Y: coord[1], Demand: demand[symbol]})
	}
	deduped := contractscaler.DedupeCoLocatedParks(parks)
	out := make([]string, 0, len(deduped))
	for symbol := range deduped {
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}
