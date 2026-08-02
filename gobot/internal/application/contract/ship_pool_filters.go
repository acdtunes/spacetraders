package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// CommandShipPolicy controls whether the command ship counts as a haul candidate.
type CommandShipPolicy int

const (
	// ExcludeCommandShip keeps the command ship out of the candidate pool.
	// Default for manufacturing/factory work, which reserves the command ship
	// for contracts and manual operations.
	ExcludeCommandShip CommandShipPolicy = iota
	// IncludeCommandShip makes the command ship a first-class haul candidate,
	// sized to its own cargo. The contract coordinator opts in because the
	// command frigate hauls contract legs fine and is frequently the fastest,
	// largest-cargo hull owned - benching it until zero haulers remain wastes
	// fleet capacity.
	IncludeCommandShip
)

// CargoCapacityPolicy controls whether a dedicated-fleet lookup excludes hulls
// with zero cargo capacity, mirroring the "unsuitable = UNSELECTABLE, not
// spawned-then-crashed" pattern used elsewhere in this package for the
// dedicated pool.
type CargoCapacityPolicy int

const (
	// AnyCargoCapacity returns every tagged fleet member regardless of cargo
	// capacity - the original FindIdleShipsByFleet behavior. The idle-arb
	// dispatcher keeps this default so its reserve accounting is unchanged.
	AnyCargoCapacity CargoCapacityPolicy = iota
	// RequireCargoCapacity excludes 0-cargo hulls (probes/satellites) from the
	// pool. The contract coordinator opts in: a 0-cargo hull can never deliver a
	// contract, so a probe mispinned into the contract fleet must be
	// UNSELECTABLE here rather than claimed, spawned, and crashed on
	// 'deliveries not complete'.
	RequireCargoCapacity
)

// CommandCargoBaselineDefault is the minimum cargo capacity a command ship
// must carry to stay a contract-selection candidate once IncludeCommandShip
// has already opted it into FindIdleLightHaulers' pool. It matches the
// light-hauler standard (RULINGS #5): a stock 40-cargo frigate double-trips
// a load an 80-cargo light hauler single-trips, spending its whole speed
// advantage on the extra leg for a net loss versus just dispatching the
// hauler - so a stock hull is not a genuine candidate. era-2's upgraded
// frigate (115 cargo) clears this bar.
const CommandCargoBaselineDefault = 80

// FilterCommandCargoBaseline drops the command ship from a candidate list
// when its cargo capacity is below baseline; every non-command hull passes
// through untouched. This is a SELECTION-time gate only, applied by the
// caller immediately after FindIdleLightHaulers returns (when it opted in
// with IncludeCommandShip) - it does not change FindIdleLightHaulers itself,
// the dedication-write floor (AssignShipFleet's cargo_capacity>=1 floor), or
// the last-resort ranking in SelectHullForCargo (domain contract package),
// which simply never sees a candidate this gate already removed.
//
//   - baseline: Minimum cargo capacity a command ship must carry to remain
//     eligible. <= 0 falls back to CommandCargoBaselineDefault (RULINGS #5:
//     parametrize, don't hardcode - the zero value means "not configured",
//     matching the IdleArb* knobs' idiom).
//
// Returned symbols keep their input order.
func FilterCommandCargoBaseline(ctx context.Context, ships []*navigation.Ship, baseline int) []string {
	if baseline <= 0 {
		baseline = CommandCargoBaselineDefault
	}
	logger := common.LoggerFromContext(ctx)

	symbols := make([]string, 0, len(ships))
	for _, ship := range ships {
		if isCommandHull(ship) && ship.CargoCapacity() < baseline {
			logger.Log("INFO", fmt.Sprintf(
				"Command ship %s skipped for contract selection: cargo capacity %d below baseline %d - upgrade its cargo hold or dispatch a light hauler instead",
				ship.ShipSymbol(), ship.CargoCapacity(), baseline), map[string]interface{}{
				"action":         "skipped:command_cargo_below_baseline",
				"ship_symbol":    ship.ShipSymbol(),
				"cargo_capacity": ship.CargoCapacity(),
				"baseline":       baseline,
			})
			continue
		}
		symbols = append(symbols, ship.ShipSymbol())
	}
	return symbols
}

// SelectAvailableShips combines the general and dedicated-fleet candidate
// pools into the coordinator's working set for one discovery pass.
//
// EXCLUSIVE MODE: when dedicatedFleetActive is true, the general pool is
// dropped entirely - the coordinator draws ONLY from dedicatedIdleShips, even
// when that is empty because every dedicated member is busy, rather than
// falling back to idle non-dedicated hulls by distance. A dedicated fleet
// must be genuinely exclusive: drafting a general-pool hull instead risks
// displacing cargo the operator is using that hull for elsewhere.
//
// When dedicatedFleetActive is false, the two pools are combined
// (dedicatedIdleShips is normally empty in this branch, since the caller's
// dedication check already says no fleet is tagged).
func SelectAvailableShips(generalShips, dedicatedIdleShips []string, dedicatedFleetActive bool) []string {
	if dedicatedFleetActive {
		return dedicatedIdleShips
	}
	return append(generalShips, dedicatedIdleShips...)
}

// FilterUnrelatedCargo splits candidate ship symbols into those safe to
// claim for a delivery of requiredCargo and those that must be parked
// instead.
//
// NO-CARGO-DUMP CLAIM GUARD: a hull already holding cargo that is NOT part
// of this delivery is never claimed, so the worker's jettison step
// (CargoManager.JettisonWrongCargoIfNeeded) can never silently dump cargo the
// operator is using the hull for elsewhere. The guard runs at selection
// time, before a hull is ever assigned, so unrelated cargo is never at risk
// of being jettisoned by this coordinator's own workers.
//
// A ship whose hold is empty, or whose hold contains only requiredCargo
// (e.g. a partial delivery resumed after a restart), is claimable. A
// candidate symbol not found in the current fleet snapshot is skipped
// silently - matching FindIdleShipsByFleet's tolerance for fleet
// composition that varies between passes - and appears in neither returned
// list.
//
//   - symbols: Candidate ship symbols to classify (already idle/dedication
//     filtered by the caller)
//   - requiredCargo: The trade symbol this delivery needs; a hull carrying
//     ONLY this symbol is not considered "unrelated" cargo
//
// Returns the symbols safe to hand to SelectClosestShip, then those excluded
// because they hold unrelated cargo.
func FilterUnrelatedCargo(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	symbols []string,
	requiredCargo string,
) ([]string, []string, error) {
	logger := common.LoggerFromContext(ctx)

	bySymbol, err := fleetBySymbol(ctx, playerID, shipRepo)
	if err != nil {
		return nil, nil, err
	}

	var claimable []string
	var parked []string
	for _, symbol := range symbols {
		ship, ok := bySymbol[symbol]
		if !ok {
			// Not in the current fleet snapshot (sold, renamed since
			// discovery) - excluded from both lists rather than guessed at.
			continue
		}
		if ship.Cargo().HasItemsOtherThan(requiredCargo) {
			parked = append(parked, symbol)
			continue
		}
		claimable = append(claimable, symbol)
	}

	if len(parked) > 0 {
		logger.Log("INFO", "Parked candidates holding unrelated cargo", map[string]interface{}{
			"action":          "filter_unrelated_cargo",
			"required_cargo":  requiredCargo,
			"parked_ships":    parked,
			"claimable_ships": claimable,
		})
	}

	return claimable, parked, nil
}

// FilterToHomeSystem narrows a candidate ship-symbol list to the hulls currently located in
// homeSystem — the contract's HOME system, derived from the delivery destination
// (shared.ExtractSystemSymbol) exactly as PlanSourcing/market_finder scope contract sourcing
// (RULINGS #14). A contract worker sources AND delivers in that one system with ZERO jump
// capability, so a hull idle in a FOREIGN system (a gate hop away) can reach neither the
// source market nor the delivery and must be UNSELECTABLE here, never claimed-then-stalled.
// This is the worker-pool LOCALITY half of the reserve floor that keeps N HOME haulers
// undedicated; this ensures the grab only ever takes HOME ones.
//
// homeSystem == "" degrades to a fleet-wide passthrough (fail-open): an un-derivable
// destination must never block the contract, matching FindIdleLightHaulers' "" convention.
// A candidate whose CURRENT system cannot be resolved (unknown location) is treated as
// out-of-home and dropped (fail-closed, matching shipCurrentSystem's pre-filter): the pool
// never surfaces a hull it cannot confirm is in range. A symbol absent from the current fleet
// snapshot is skipped silently, mirroring FilterUnrelatedCargo's tolerance for fleet
// composition that varies between passes.
//
//   - symbols: Candidate ship symbols to scope (already idle/dedication/cargo filtered by the caller)
//   - homeSystem: The contract's home system; "" returns symbols unchanged (fleet-wide)
//
// Returns the subset whose ship is currently in homeSystem, in input order.
func FilterToHomeSystem(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	symbols []string,
	homeSystem string,
) ([]string, error) {
	if homeSystem == "" {
		return symbols, nil
	}
	logger := common.LoggerFromContext(ctx)

	bySymbol, err := fleetBySymbol(ctx, playerID, shipRepo)
	if err != nil {
		return nil, err
	}

	homeSymbols := make([]string, 0, len(symbols))
	var foreign []string
	for _, symbol := range symbols {
		ship, ok := bySymbol[symbol]
		if !ok {
			// Not in the current fleet snapshot (sold/renamed since discovery) — excluded
			// from the result rather than guessed at, mirroring FilterUnrelatedCargo.
			continue
		}
		if shipCurrentSystem(ship) == homeSystem {
			homeSymbols = append(homeSymbols, symbol)
		} else {
			foreign = append(foreign, symbol)
		}
	}

	if len(foreign) > 0 {
		logger.Log("INFO", "Excluded out-of-home-system hulls from contract worker selection", map[string]interface{}{
			"action":        "filter_to_home_system",
			"home_system":   homeSystem,
			"home_symbols":  homeSymbols,
			"foreign_ships": foreign,
		})
	}

	return homeSymbols, nil
}

// isCommandHull reports whether a ship is the command ship, by registration role
// or by the conventional "*-1" symbol. Candidate discovery, the selection log
// and the domain cargo-fit ladder (SelectHullForCargo) share the one domain
// predicate so they all mark exactly the same hull as the command ship.
func isCommandHull(ship *navigation.Ship) bool {
	return domainContract.IsCommandHull(ship)
}

// shipCurrentSystem returns the system symbol a ship is currently located in,
// derived from its current waypoint symbol (e.g. "X1-KA42-E42" -> "X1-KA42").
// Returns "" when the location is unknown, which the single-system pool filter
// treats as out-of-system (fail-closed).
func shipCurrentSystem(ship *navigation.Ship) string {
	loc := ship.CurrentLocation()
	if loc == nil {
		return ""
	}
	return shared.ExtractSystemSymbol(loc.Symbol)
}
