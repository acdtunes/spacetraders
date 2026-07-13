package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindCoordinatorShips returns the list of ship symbols currently owned by the coordinator.
// These are ships that are assigned to the coordinator container and haven't been transferred to workers.
//
// Parameters:
//   - coordinatorID: The container ID of the coordinator
//   - playerID: Player ID for ship lookups
//   - shipRepo: Repository to query ships with assignments
//
// Returns:
//   - shipSymbols: List of ship symbols owned by the coordinator
//   - error: Any error encountered
func FindCoordinatorShips(
	ctx context.Context,
	coordinatorID string,
	playerID int,
	shipRepo navigation.ShipRepository,
) ([]string, error) {
	// Find all ships assigned to this coordinator
	ships, err := shipRepo.FindByContainer(ctx, coordinatorID, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find coordinator ships: %w", err)
	}

	// Extract ship symbols
	shipSymbols := make([]string, 0, len(ships))
	for _, ship := range ships {
		shipSymbols = append(shipSymbols, ship.ShipSymbol())
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Coordinator ships retrieved", map[string]interface{}{
		"action":         "find_coordinator_ships",
		"coordinator_id": coordinatorID,
		"ship_count":     len(shipSymbols),
		"ships":          shipSymbols,
	})

	return shipSymbols, nil
}

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
	// fleet capacity (sp-4a4e).
	IncludeCommandShip
)

// CargoCapacityPolicy controls whether a dedicated-fleet lookup excludes hulls
// with zero cargo capacity. Mirrors the qr3v/sp-9hu8 "unsuitable = UNSELECTABLE,
// not spawned-then-crashed" pattern for the dedicated pool.
type CargoCapacityPolicy int

const (
	// AnyCargoCapacity returns every tagged fleet member regardless of cargo
	// capacity - the original FindIdleShipsByFleet behavior. The idle-arb
	// dispatcher keeps this default so its reserve accounting is unchanged.
	AnyCargoCapacity CargoCapacityPolicy = iota
	// RequireCargoCapacity excludes 0-cargo hulls (probes/satellites) from the
	// pool. The contract coordinator opts in: a 0-cargo hull can never deliver a
	// contract, so a probe mispinned into the contract fleet (sp-lybx: TORWIND-24)
	// must be UNSELECTABLE here rather than claimed, spawned, and crashed on
	// 'deliveries not complete' - the storm this closes at discovery.
	RequireCargoCapacity
)

// roleHauler is the registration role of dedicated haul hulls; the command
// ship's role lives with the shared IsCommandHull predicate in the domain
// contract package.
const roleHauler = "HAULER"

// FindIdleLightHaulers finds all idle haul-capable ships for a player.
//
// A ship is a candidate if:
//  1. Its role is "HAULER" - or "COMMAND" when the caller passes IncludeCommandShip
//  2. It is not dedicated to a coordinator's exclusive fleet (Ship.DedicatedFleet() is empty)
//  3. It has cargo capacity (excludes probes/satellites)
//  4. It is currently in systemFilter's system when a non-empty systemFilter is given
//  5. It is not in transit and has no active assignment (Ship.IsIdle() is true)
//
// This provides a dynamic pool of available haulers without requiring pre-assignment.
// Ship assignment status is now embedded in the Ship aggregate and enriched by the repository.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships (enriches assignment data automatically)
//   - systemFilter: When non-empty, restricts the pool to hulls whose CURRENT
//     system equals it. Single-system callers (manufacturing/factory
//     coordinators, which never jump cross-system) pass their operating system
//     so an out-of-system hull they could never operate is UNSELECTABLE here
//     rather than claimed-then-failed (the sp-9hu8 class, factory-side: sp-qr3v).
//     Fleet-wide callers (contract) pass "" for the pre-filter's original,
//     unfiltered behavior.
//   - policies: Optional command-ship policy (default: ExcludeCommandShip). Pass
//     IncludeCommandShip to treat the command ship as a first-class candidate.
//
// Returns:
//   - ships: List of idle candidate ship entities
//   - shipSymbols: List of idle candidate ship symbols (for convenience)
//   - error: Any error encountered
func FindIdleLightHaulers(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	systemFilter string,
	policies ...CommandShipPolicy,
) ([]*navigation.Ship, []string, error) {
	// Default: keep the command ship out of the pool.
	policy := ExcludeCommandShip
	if len(policies) > 0 {
		policy = policies[0]
	}
	logger := common.LoggerFromContext(ctx)

	// Fetch all ships for player (includes assignment data via hybrid repo)
	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	var idleHaulers []*navigation.Ship

	// The command frigate is collected separately and admitted only as a LAST
	// RESORT (sp-sqq5) - see the last-resort merge after the loop.
	var idleCommandHulls []*navigation.Ship

	// Track whether ANY haul-capable hull exists (regardless of availability),
	// purely for the discovery log below.
	candidateShipsExist := false

	for _, ship := range allShips {
		// Candidacy by role. Haulers always qualify. The command ship (role
		// COMMAND, symbol "*-1") qualifies only when the caller opts in
		// (contracts do; manufacturing keeps it reserved by not opting in), and
		// even then enters the pool only as a last resort - see the merge below.
		isCommand := isCommandHull(ship)
		switch {
		case isCommand:
			if policy != IncludeCommandShip {
				continue
			}
		case ship.Role() != roleHauler:
			// Probes, satellites, excavators, etc. never haul contracts.
			continue
		}

		// Claim-filter (sp-snmb): a ship dedicated to a coordinator's exclusive
		// fleet is invisible to this general-purpose pool, unconditionally.
		// Every caller of this function (contract, manufacturing, factory,
		// balance-handler) shares this one exclusion "for free" - a coordinator
		// finds its own dedicated ships separately via FindIdleShipsByFleet.
		// This is layer 1 of the two-layer dedication enforcement (sp-l7h2): a
		// cheap read-side pre-filter. Layer 2 - the correctness guarantee - is
		// the atomic dedication check inside ShipRepository.ClaimShip. This is
		// also what makes the sp-sqq5 last-resort rule below apply to exactly the
		// UNDEDICATED command frigate: a command hull the captain pinned with
		// `fleet assign --fleet contract` carries the tag and is routed to the
		// coordinator's own FindIdleShipsByFleet lookup instead of here.
		if ship.DedicatedFleet() != "" {
			continue
		}

		// Must have cargo capacity (excludes probes/satellites tagged as haulers)
		if ship.CargoCapacity() == 0 {
			continue
		}

		// At least one haul-capable hull exists in the fleet.
		candidateShipsExist = true

		// Single-system filter (sp-qr3v): a caller that operates within one
		// system (manufacturing/factory, which never jumps cross-system) restricts
		// the pool to hulls CURRENTLY in that system. An out-of-system hull is
		// invisible here - the coordinator can never navigate it home to work, so
		// claiming it just fails the worker on every pass (the sp-9hu8 class,
		// factory-side). A hull whose location is unknown is treated as
		// out-of-system: the pre-filter fails CLOSED, never surfacing a hull it
		// cannot confirm is in range. Fleet-wide callers pass "" and skip this.
		if systemFilter != "" && shipCurrentSystem(ship) != systemFilter {
			continue
		}

		// Exclude ships in transit (even without assignment): a hull being
		// balanced or navigating is not available for a new contract leg.
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}

		// Only idle ships (no active assignment). Ship.IsIdle() checks the
		// embedded assignment state. The command frigate is held back into its
		// own bucket so it can be admitted last-resort-only below.
		if !ship.IsIdle() {
			continue
		}
		if isCommand {
			idleCommandHulls = append(idleCommandHulls, ship)
		} else {
			idleHaulers = append(idleHaulers, ship)
		}
	}

	// LAST-RESORT COMMAND FRIGATE (sp-sqq5, RULINGS #7: "the command frigate
	// hauls only as last resort"). An undedicated command hull - including one
	// deliberately RETIRED via `fleet unassign` (tag cleared to "") - is admitted
	// to the candidate pool ONLY when no regular hauler is idle. This stops the
	// RUNNING contract coordinator from re-sweeping a retired frigate back onto
	// contracts while haulers exist (the sp-sqq5 defect: a re-claim that stranded
	// a mid-delivery contract and put the low-cargo/low-fuel command hull on
	// contracts), WITHOUT benching it when it is the only hull available. The
	// exclusion is therefore CONDITIONAL, never an absolute ban: with zero idle
	// haulers the frigate is the last resort and enters the pool (preserving the
	// sp-4a4e "don't idle a usable hull for 5h" guarantee). Discovery makes the
	// last-resort decision because only here is the whole idle fleet visible; the
	// spawn-side claim guard (spawnContractWorker) is the single-writer backstop.
	commandAdmittedLastResort := false
	if len(idleHaulers) == 0 && len(idleCommandHulls) > 0 {
		idleHaulers = append(idleHaulers, idleCommandHulls...)
		commandAdmittedLastResort = true
	}

	idleHaulerSymbols := make([]string, 0, len(idleHaulers))
	for _, ship := range idleHaulers {
		idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())
	}

	logger.Log("INFO", "Idle light haulers discovered", map[string]interface{}{
		"action":                       "find_idle_haulers",
		"total_ships":                  len(allShips),
		"candidate_ships_exist":        candidateShipsExist,
		"include_command_ship":         policy == IncludeCommandShip,
		"system_filter":                systemFilter,
		"idle_haulers":                 len(idleHaulers),
		"hauler_symbols":               idleHaulerSymbols,
		"command_hulls_held":           len(idleCommandHulls),
		"command_admitted_last_resort": commandAdmittedLastResort,
	})

	return idleHaulers, idleHaulerSymbols, nil
}

// CommandCargoBaselineDefault is the minimum cargo capacity a command ship
// must carry to stay a contract-selection candidate once IncludeCommandShip
// has already opted it into FindIdleLightHaulers' pool. It matches the
// light-hauler standard (RULINGS #5): a stock 40-cargo frigate double-trips
// a load an 80-cargo light hauler single-trips, spending its whole speed
// advantage on the extra leg for a net loss versus just dispatching the
// hauler - so a stock hull is not a genuine candidate. era-2's upgraded
// frigate (115 cargo) clears this bar (sp-uj6a).
const CommandCargoBaselineDefault = 80

// FilterCommandCargoBaseline drops the command ship from a candidate list
// when its cargo capacity is below baseline; every non-command hull passes
// through untouched. This is a SELECTION-time gate only, applied by the
// caller immediately after FindIdleLightHaulers returns (when it opted in
// with IncludeCommandShip) - it does not change FindIdleLightHaulers itself,
// the r6f1 dedication-write floor (AssignShipFleet's cargo_capacity>=1
// floor), or the sp-4a4e last-resort ranking in SelectHullForCargo (domain
// contract package), which simply never sees a candidate this gate already
// removed (sp-uj6a).
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - ships: Candidate ships to filter, as returned by FindIdleLightHaulers
//   - baseline: Minimum cargo capacity a command ship must carry to remain
//     eligible. <= 0 falls back to CommandCargoBaselineDefault (RULINGS #5:
//     parametrize, don't hardcode - the zero value means "not configured",
//     matching the IdleArb* knobs' idiom).
//
// Returns:
//   - symbols: Candidate ship symbols with any under-baseline command ship
//     removed, in input order.
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

// FindIdleShipsByFleet looks up a coordinator's own dedicated fleet by name -
// every ship whose persisted DedicatedFleet tag equals fleet - and returns
// only the ones currently idle. Busy and in-transit ships are silently
// skipped rather than erroring, since fleet composition legitimately varies
// over the coordinator's lifetime.
//
// This replaces the symbol-list FindIdleDedicatedShips (sp-snmb → sp-l7h2):
// a remembered --dedicated-ships list goes stale the moment the captain
// reassigns a ship via `fleet assign` without restarting the coordinator.
// Reading DedicatedFleet() from the DB on every discovery pass is what makes
// reassignment live instead of "live after next restart."
//
// Unlike FindIdleLightHaulers, this never filters by ROLE: a ship qualifies
// purely by carrying the fleet's tag, whatever hull it is (an excavator, the
// command frigate) - the dedication itself is the authorization. Cargo-capacity
// filtering, by contrast, is OPT-IN via CargoCapacityPolicy: the default keeps
// every tagged member (idle-arb relies on this for its reserve accounting), and
// the contract coordinator passes RequireCargoCapacity so a 0-cargo probe
// mispinned into the contract fleet (sp-lybx) is UNSELECTABLE rather than
// claimed-spawned-crashed.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships (enriches assignment data automatically)
//   - fleet: The fleet name to look up; "" (no dedicated fleet) returns nothing,
//     since an empty tag means "general pool", never a fleet of its own
//   - policies: Optional cargo-capacity policy (default: AnyCargoCapacity). Pass
//     RequireCargoCapacity to exclude 0-cargo hulls (probes/satellites) that can
//     never carry a delivery - the sp-lybx exclusion for contract worker selection.
//
// Returns:
//   - ships: List of idle dedicated ship entities
//   - shipSymbols: List of idle dedicated ship symbols (for convenience)
//   - error: Any error encountered
func FindIdleShipsByFleet(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	fleet string,
	policies ...CargoCapacityPolicy,
) ([]*navigation.Ship, []string, error) {
	if fleet == "" {
		return nil, nil, nil
	}

	// Default: keep every tagged member regardless of cargo capacity.
	cargoPolicy := AnyCargoCapacity
	if len(policies) > 0 {
		cargoPolicy = policies[0]
	}

	logger := common.LoggerFromContext(ctx)

	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	fleetTotal := 0
	zeroCargoExcluded := 0
	var idleShips []*navigation.Ship
	var idleSymbols []string
	for _, ship := range allShips {
		if ship.DedicatedFleet() != fleet {
			continue
		}
		fleetTotal++

		// Cargo-capacity exclusion (sp-lybx): a caller that opts in
		// (RequireCargoCapacity) drops 0-cargo hulls, because a probe/satellite
		// can never carry a contract delivery - claiming it just spawns a worker
		// that dies instantly on 'deliveries not complete'. Logged by name so the
		// captain can see WHY a mispinned hull is being ignored (honest exclusion),
		// and counted into the summary below so an all-probe fleet reads as
		// "0 dispatchable, N excluded for 0 cargo" rather than a silent empty pool.
		if cargoPolicy == RequireCargoCapacity && ship.CargoCapacity() == 0 {
			zeroCargoExcluded++
			logger.Log("WARNING", fmt.Sprintf(
				"Dedicated %s-fleet hull %s excluded from contract worker selection: 0 cargo capacity (cannot deliver) - check hull class/pin",
				fleet, ship.ShipSymbol()), map[string]interface{}{
				"action":      "exclude_zero_cargo_dedicated_hull",
				"fleet":       fleet,
				"ship_symbol": ship.ShipSymbol(),
			})
			continue
		}

		// Exclude ships in transit (even without assignment), mirroring
		// FindIdleLightHaulers: a hull mid-flight is not available to dispatch.
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}
		if ship.IsIdle() {
			idleShips = append(idleShips, ship)
			idleSymbols = append(idleSymbols, ship.ShipSymbol())
		}
	}

	logger.Log("INFO", "Idle dedicated fleet ships discovered", map[string]interface{}{
		"action":              "find_idle_ships_by_fleet",
		"fleet":               fleet,
		"fleet_total":         fleetTotal,
		"idle_in_fleet":       len(idleSymbols),
		"zero_cargo_excluded": zeroCargoExcluded,
		"ship_symbols":        idleSymbols,
	})

	return idleShips, idleSymbols, nil
}

// FleetHasMembers reports whether ANY ship - idle, busy, or in transit -
// currently carries the given DedicatedFleet tag. Unlike FindIdleShipsByFleet,
// which only surfaces dispatchable members, this answers a different
// question: does this coordinator have an exclusive fleet AT ALL right now?
//
// That distinction is what makes EXCLUSIVE MODE (sp-wq7r) correct: a
// dedicated fleet that is fully busy must still block the coordinator from
// raiding the general pool. Only the absence of ANY tagged member falls
// back to shared hulls. Reading the persisted tag on every call (rather than
// trusting a remembered --dedicated-ships list) keeps this live with the
// same "no restart needed" guarantee FindIdleShipsByFleet already gives
// `fleet assign`/`unassign` (sp-l7h2).
//
// Parameters:
//   - fleet: The fleet name to look up; "" always returns false, mirroring
//     FindIdleShipsByFleet's "no dedicated fleet" convention.
//
// Returns:
//   - hasMembers: true if at least one ship carries the fleet tag
//   - error: Any error encountered
func FleetHasMembers(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	fleet string,
) (bool, error) {
	if fleet == "" {
		return false, nil
	}

	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch ships: %w", err)
	}

	for _, ship := range allShips {
		if ship.DedicatedFleet() == fleet {
			return true, nil
		}
	}
	return false, nil
}

// FindFleetMemberSymbols returns the symbols of EVERY ship currently carrying the
// given DedicatedFleet tag — idle, busy, or in transit — the LIVE membership of a
// coordinator's dedicated fleet (sp-cmwc). Unlike FindIdleShipsByFleet it applies no
// idle/role/cargo filter: pure membership by tag, because the callers that need it
// (the between-legs homing gate and the standby-station occupancy balancer) care who
// BELONGS to the fleet, not who is dispatchable right now.
//
// Reading the persisted tag on every call is what makes membership live: a hull
// added via `fleet add` (tag set, absent from the immutable --dedicated-ships launch
// list) is a member immediately, and a hull `fleet remove`d (tag cleared) drops out —
// no restart, and no dependence on the stale launch snapshot (the sp-cmwc defect).
// The "" fleet returns nothing, mirroring FindIdleShipsByFleet / FleetHasMembers.
func FindFleetMemberSymbols(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	fleet string,
) ([]string, error) {
	if fleet == "" {
		return nil, nil
	}

	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	var members []string
	for _, ship := range allShips {
		if ship.DedicatedFleet() == fleet {
			members = append(members, ship.ShipSymbol())
		}
	}
	return members, nil
}

// SelectAvailableShips combines the general and dedicated-fleet candidate
// pools into the coordinator's working set for one discovery pass.
//
// EXCLUSIVE MODE (sp-wq7r): when dedicatedFleetActive is true, the general
// pool is dropped entirely - the coordinator draws ONLY from
// dedicatedIdleShips, even when that is empty because every dedicated
// member is busy, rather than falling back to idle non-dedicated hulls by
// distance. Before this fix the two pools were unconditionally combined
// (append(generalShips, dedicatedIdleShips...)) regardless of dedication
// state, so a coordinator configured with a dedicated fleet still drafted
// idle pool hulls - once displacing cargo the captain was mid-liquidating
// on a borrowed hull. "Dedicated" was never actually exclusive.
//
// When dedicatedFleetActive is false, the two pools are combined exactly as
// before this fix (dedicatedIdleShips is normally empty in this branch,
// since the caller's dedication check already says no fleet is tagged).
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
// NO-CARGO-DUMP CLAIM GUARD (sp-wq7r): a hull already holding cargo that is
// NOT part of this delivery is never claimed. Before this fix the
// coordinator picked candidates by distance alone and left the worker's
// jettison step (CargoManager.JettisonWrongCargoIfNeeded) to silently dump
// whatever the hull was carrying to make room - once destroying 43 units of
// EQUIPMENT the captain was mid-liquidating on a borrowed pool hull. The
// guard runs at selection time, before a hull is ever assigned, so
// unrelated cargo is never at risk of being jettisoned by this
// coordinator's own workers.
//
// A ship whose hold is empty, or whose hold contains only requiredCargo
// (e.g. a partial delivery resumed after a restart), is claimable. A
// candidate symbol not found in the current fleet snapshot is skipped
// silently - matching FindIdleShipsByFleet's tolerance for fleet
// composition that varies between passes - and appears in neither returned
// list.
//
// Parameters:
//   - symbols: Candidate ship symbols to classify (already idle/dedication
//     filtered by the caller)
//   - requiredCargo: The trade symbol this delivery needs; a hull carrying
//     ONLY this symbol is not considered "unrelated" cargo
//
// Returns:
//   - claimable: Symbols safe to hand to SelectClosestShip
//   - parked: Symbols excluded because they hold unrelated cargo
//   - error: Any error encountered
func FilterUnrelatedCargo(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	symbols []string,
	requiredCargo string,
) ([]string, []string, error) {
	logger := common.LoggerFromContext(ctx)

	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch ships: %w", err)
	}
	bySymbol := make(map[string]*navigation.Ship, len(allShips))
	for _, ship := range allShips {
		bySymbol[ship.ShipSymbol()] = ship
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
