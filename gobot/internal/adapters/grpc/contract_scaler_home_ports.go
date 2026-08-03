package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// contractScalerHomeReader resolves the player's home system — the home-system-only sourcing scope
// (RULINGS #14) the role lookup runs within. readable=false (no resolvable home, e.g. a cold
// pre-registration boot) makes the scaler no-op (an empty era) rather than guess.
type contractScalerHomeReader interface {
	HomeSystem(ctx context.Context, playerID int) (string, bool, error)
}

// contractScalerWaypointLister lists a system's waypoints with their coordinates — the geometry half of
// the lookup. Satisfied by *persistence.GormWaypointRepository.
type contractScalerWaypointLister interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// contractScalerMarketReader reads a waypoint's market roles — the trade-role half of the lookup.
// Satisfied by market.MarketRepository. A nil market (no scanned data) is geometry-only: the waypoint
// is neither importer nor exporter, so it fills no role this era.
type contractScalerMarketReader interface {
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// contractScalerRoleResolver implements the coordinator's RoleResolver port. It reads the home system
// once, lists its waypoints (geometry) + their markets (trade roles), and hands the pure domain lookup
// (contractscaler.ResolveRoles) a []WaypointMarket, returning the resolved roles plus a per-park demand
// weight map. It computes NO plan and holds NO state — the coordinator memoizes the result at arm.
type contractScalerRoleResolver struct {
	home      contractScalerHomeReader
	waypoints contractScalerWaypointLister
	markets   contractScalerMarketReader
}

// ResolveRoles is the once-at-arm lookup. It returns empty roles + empty demand (no error) when the
// home system is unresolvable so the coordinator's armed plan is empty and it never spends against an
// unknown topology (fail-safe, not fail-error — an empty era is a valid state, not a failure). The
// central parks are coord-DEDUPED to one representative per location (dedupedCentralParkSymbols — the
// SAME helper the placement provider uses), so the scaler's TopDeliverySlots selection and the
// coordinator's homing placement derive from ONE deduped park set (no drift).
func (r *contractScalerRoleResolver) ResolveRoles(ctx context.Context, playerID int) (contractscaler.EraRoles, map[string]float64, error) {
	markets, demand, err := r.homeMarkets(ctx, playerID)
	if err != nil {
		return contractscaler.EraRoles{}, nil, err
	}
	roles := contractscaler.ResolveRoles(markets)
	roles.CentralParks = dedupedCentralParkSymbols(roles, markets, demand)
	return roles, demand, nil
}

// homeMarkets reads the home system ONCE and returns its waypoints as []WaypointMarket
// (geometry + trade roles) plus the per-waypoint import-volume demand map — the SHARED
// read behind BOTH the scaler's buy-order homing (ResolveRoles) and the coordinator's
// between-legs homing (contractStandbyPlacementProvider), so the two positioning consumers
// rank parks by ONE demand definition. Empty era (unresolvable/unscanned home, or no
// waypoint surface wired) → nil markets + empty demand, no error (fail-safe, not fail-error).
func (r *contractScalerRoleResolver) homeMarkets(ctx context.Context, playerID int) ([]contractscaler.WaypointMarket, map[string]float64, error) {
	system, readable, err := r.home.HomeSystem(ctx, playerID)
	if err != nil {
		return nil, nil, err
	}
	if !readable || system == "" {
		return nil, map[string]float64{}, nil
	}
	if r.waypoints == nil {
		return nil, map[string]float64{}, nil // no waypoint surface wired → empty era, no spend
	}

	waypoints, err := r.waypoints.ListBySystem(ctx, system)
	if err != nil {
		return nil, nil, err
	}

	markets := make([]contractscaler.WaypointMarket, 0, len(waypoints))
	demand := make(map[string]float64, len(waypoints))
	for _, waypoint := range waypoints {
		if waypoint == nil {
			continue
		}
		exports, imports, weight := r.tradeRoles(ctx, waypoint.Symbol, playerID)
		markets = append(markets, contractscaler.WaypointMarket{
			Symbol:        waypoint.Symbol,
			X:             waypoint.X,
			Y:             waypoint.Y,
			Exports:       exports,
			Imports:       imports,
			IsMarketplace: waypoint.IsMarketplace(), // durable charted trait → a sink even before its imports are dock-scanned
			// The rest of the DURABLE charted record — the generator type, the charted traits and
			// on-site fuel — carries the era-invariant standby anchors (contractscaler/anchors.go).
			// Without these three the anchors have nothing to key on and every slot fails open to
			// the central set, so this is the wiring the whole placement rests on.
			Type:    waypoint.Type,
			Traits:  waypoint.Traits,
			HasFuel: waypoint.HasFuel,
		})
		if len(imports) > 0 {
			demand[waypoint.Symbol] = weight
		}
	}
	return markets, demand, nil
}

// tradeRoles reads one waypoint's market and splits its goods into EXPORT / IMPORT symbol lists
// (EXCHANGE goods — neither produced nor consumed — are omitted, matching WaypointMarket's contract).
// It also returns the per-waypoint demand weight: the SUM of import trade VOLUMES (open Q a: prefer
// volume over count as the freq×draw proxy), falling back to the import COUNT when no volume signal is
// present (a zero-volume market still ranks, never sinking to 0 and dropping out of the spread). A nil
// market (no scanned data) contributes geometry only: no roles, no demand.
func (r *contractScalerRoleResolver) tradeRoles(ctx context.Context, waypointSymbol string, playerID int) (exports, imports []string, weight float64) {
	data, err := r.markets.GetMarketData(ctx, waypointSymbol, playerID)
	if err != nil || data == nil {
		return nil, nil, 0
	}
	importVolume := 0
	for _, good := range data.TradeGoods() {
		switch good.TradeType() {
		case market.TradeTypeExport:
			exports = append(exports, good.Symbol())
		case market.TradeTypeImport:
			imports = append(imports, good.Symbol())
			importVolume += good.TradeVolume()
		}
	}
	if importVolume > 0 {
		return exports, imports, float64(importVolume)
	}
	return exports, imports, float64(len(imports)) // count fallback when volume is unavailable
}

// contractScalerShipHomeReader resolves the home system from the player's hull locations by ANCHOR
// PRIORITY: (1) the contract fleet's own FOOTPRINT — the base where the "contract"-dedicated
// hulls sit — wins whenever any contract hull exists (degree 1+); (2) the command frigate's system
// anchors ONLY when no contract hull exists yet (degree-0 cold-start, where the frigate IS the sole
// contract hauler); (3) the lexicographically smallest ship system is the final determinism fallback.
// Priority 1 exists because post-degree-0 the command frigate is RETIRED from contracts and becomes the
// reserved PURCHASE ship that WANDERS to shipyards — anchoring on it flips home to the wrong system. The
// footprint is the MODAL contract system (the base where MOST contract hulls sit), so a single hull
// transiting away on a delivery never flips home. readable=false (no resolvable hull location) makes the
// scaler no-op (an empty era) rather than guess.
type contractScalerShipHomeReader struct{ shipRepo navigation.ShipRepository }

func (r *contractScalerShipHomeReader) HomeSystem(ctx context.Context, playerID int) (string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", false, nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return "", false, err
	}
	contractSystems := map[string]int{} // contract-fleet footprint: system → count of "contract" hulls there
	commandHome, anyHome := "", ""
	for _, ship := range ships {
		location := ship.CurrentLocation()
		if location == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(location.Symbol)
		if system == "" {
			continue
		}
		if anyHome == "" || system < anyHome {
			anyHome = system
		}
		if ship.Role() == commandRole {
			commandHome = system
		}
		if ship.DedicatedFleet() == contractFleetTag {
			contractSystems[system]++
		}
	}
	// Priority 1: the contract fleet's own footprint (degree 1+) — the base most contract hulls sit in.
	if footprint := mostCommonSystem(contractSystems); footprint != "" {
		return footprint, true, nil
	}
	// Priority 2: the command frigate (degree-0 cold-start — it is the sole contract hauler).
	if commandHome != "" {
		return commandHome, true, nil
	}
	// Priority 3: any-hull lexicographically-smallest (final determinism fallback).
	if anyHome != "" {
		return anyHome, true, nil
	}
	return "", false, nil
}

// mostCommonSystem returns the system with the highest count, ties broken lexicographically smallest, or
// "" when counts is empty. Choosing the MODAL system (not a first-seen or extremal one) is what makes the
// contract-footprint anchor robust to a single hull transiting away on a delivery: the base where the
// fleet actually sits wins over a transient far outlier. Deterministic regardless of map-iteration order —
// a strictly greater count always replaces, and among equal counts the smaller system always replaces.
func mostCommonSystem(counts map[string]int) string {
	best, bestCount := "", 0
	for system, count := range counts {
		if count > bestCount || (count == bestCount && system < best) {
			best, bestCount = system, count
		}
	}
	return best
}
