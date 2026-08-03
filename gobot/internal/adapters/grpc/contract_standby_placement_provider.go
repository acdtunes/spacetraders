package grpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// contractStandbyPlacementProvider implements appContract.StandbyPlacementProvider for the contract
// fleet coordinator's between-legs homing + the idle-arb re-home sweep. It
// REUSES the auto-scaler's home-market read (homeMarkets), classifies this era's central parks by
// market role + geometry, coord-dedups them (so slots are distinct LOCATIONS), and returns the ≤6
// FIXED delivery placement slots (TopDeliverySlots) — the SAME selection the scaler buys against, via
// the SHARED dedupedCentralParkSymbols helper, so the two positioning consumers agree on ONE slot set
// with no drift. The runtime homing zips hulls onto these slots by symbol — NO demand. It computes NO
// plan and holds NO placement state; the coordinator resolves it live each pass. RULINGS #14
// (home-system only); RULINGS #3 (a READ, never a config write).
//
// loggedMisses is a LOG LATCH ONLY — the last anchor-miss signature reported per player, so a
// template break is logged when it appears and when it changes instead of once per homing pass.
// It never feeds a placement decision; clearing it would change nothing but the log volume.
type contractStandbyPlacementProvider struct {
	resolver *contractScalerRoleResolver

	mu           sync.Mutex
	loggedMisses map[int]string
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
	roles.CentralParks = dedupedCentralParkSymbols(roles, markets, demand)
	p.reportAnchorMisses(ctx, playerID, roles.Anchors)
	return contractscaler.TopDeliverySlots(roles, demand), nil
}

// reportAnchorMisses WARNs when this era's charted template failed to produce one of the
// era-invariant standby anchors. Those slots silently degrade to the demand-ranked central set
// (which is the whole point of failing open), so without this log a template change looks
// exactly like a healthy era and the analyst never re-ranks the slot from the contract corpus.
// Latched on the miss SIGNATURE so a persistent break costs one line, not one per homing pass.
func (p *contractStandbyPlacementProvider) reportAnchorMisses(ctx context.Context, playerID int, anchors contractscaler.EraAnchors) {
	misses := anchors.Misses()
	signature := strings.Join(misses, ",")

	p.mu.Lock()
	if p.loggedMisses == nil {
		p.loggedMisses = map[int]string{}
	}
	repeat := p.loggedMisses[playerID] == signature
	p.loggedMisses[playerID] = signature
	p.mu.Unlock()

	if repeat || len(misses) == 0 {
		return
	}
	common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
		"Contract standby placement: this era charted no %v anchor(s) — those slots fall back to the demand-ranked central set; re-rank them from the contract corpus",
		misses), map[string]interface{}{
		"action": "contract_standby_anchor_miss", "player_id": playerID, "missing_anchors": misses,
	})
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
