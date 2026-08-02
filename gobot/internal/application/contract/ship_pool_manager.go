package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
//   - systemFilter: When non-empty, restricts the pool to hulls whose CURRENT
//     system equals it. Single-system callers (manufacturing/factory
//     coordinators, which never jump cross-system) pass their operating system
//     so an out-of-system hull they could never operate is UNSELECTABLE here
//     rather than claimed-then-failed. Fleet-wide callers (contract) pass ""
//     for the pre-filter's original, unfiltered behavior.
//   - policies: Optional command-ship policy (default: ExcludeCommandShip). Pass
//     IncludeCommandShip to treat the command ship as a first-class candidate.
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
	// RESORT - see the last-resort merge after the loop.
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

		// Claim-filter: a ship dedicated to a coordinator's exclusive fleet is
		// invisible to this general-purpose pool, unconditionally. Every caller
		// of this function (contract, manufacturing, factory, balance-handler)
		// shares this one exclusion "for free" - a coordinator finds its own
		// dedicated ships separately via FindIdleShipsByFleet. This is layer 1 of
		// the two-layer dedication enforcement: a cheap read-side pre-filter.
		// Layer 2 - the correctness guarantee - is the atomic dedication check
		// inside ShipRepository.ClaimShip. This is also what makes the
		// last-resort rule below apply to exactly the UNDEDICATED command
		// frigate: a command hull the captain pinned with `fleet assign --fleet
		// contract` carries the tag and is routed to the coordinator's own
		// FindIdleShipsByFleet lookup instead of here.
		if ship.DedicatedFleet() != "" {
			continue
		}

		// Must have cargo capacity (excludes probes/satellites tagged as haulers)
		if ship.CargoCapacity() == 0 {
			continue
		}

		// At least one haul-capable hull exists in the fleet.
		candidateShipsExist = true

		// Single-system filter: a caller that operates within one system
		// (manufacturing/factory, which never jumps cross-system) restricts the
		// pool to hulls CURRENTLY in that system. An out-of-system hull is
		// invisible here - the coordinator can never navigate it home to work, so
		// claiming it just fails the worker on every pass. A hull whose location
		// is unknown is treated as out-of-system: the pre-filter fails CLOSED,
		// never surfacing a hull it cannot confirm is in range. Fleet-wide
		// callers pass "" and skip this.
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

	// LAST-RESORT COMMAND FRIGATE (RULINGS #7: "the command frigate hauls only
	// as last resort"). An undedicated command hull - including one deliberately
	// RETIRED via `fleet unassign` (tag cleared to "") - is admitted to the
	// candidate pool ONLY when no regular hauler is idle. This stops the RUNNING
	// contract coordinator from re-sweeping a retired frigate back onto
	// contracts while haulers exist - a re-claim would strand whatever
	// mid-delivery contract is running and put a low-cargo/low-fuel command hull
	// back on contract duty - WITHOUT benching it when it is the only hull
	// available. The exclusion is therefore CONDITIONAL, never an absolute ban:
	// with zero idle haulers the frigate is the last resort and enters the pool
	// (preserving the "don't idle a usable hull for 5h" guarantee). Discovery
	// makes the last-resort decision because only here is the whole idle fleet
	// visible; the spawn-side claim guard (spawnContractWorker) is the
	// single-writer backstop.
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

// FindIdleShipsByFleet looks up a coordinator's own dedicated fleet by name -
// every ship whose persisted DedicatedFleet tag equals fleet - and returns
// only the ones currently idle. Busy and in-transit ships are silently
// skipped rather than erroring, since fleet composition legitimately varies
// over the coordinator's lifetime.
//
// Reading DedicatedFleet() from the DB on every discovery pass (rather than a
// remembered --dedicated-ships list) is what makes a `fleet assign`
// reassignment live instead of "live after next restart."
//
// Unlike FindIdleLightHaulers, this never filters by ROLE: a ship qualifies
// purely by carrying the fleet's tag, whatever hull it is (an excavator, the
// command frigate) - the dedication itself is the authorization. Cargo-capacity
// filtering, by contrast, is OPT-IN via CargoCapacityPolicy: the default keeps
// every tagged member (idle-arb relies on this for its reserve accounting), and
// the contract coordinator passes RequireCargoCapacity so a 0-cargo probe
// mispinned into the contract fleet is UNSELECTABLE rather than
// claimed-spawned-crashed.
//
//   - fleet: The fleet name to look up; "" (no dedicated fleet) returns nothing,
//     since an empty tag means "general pool", never a fleet of its own
//   - policies: Optional cargo-capacity policy (default: AnyCargoCapacity). Pass
//     RequireCargoCapacity to exclude 0-cargo hulls (probes/satellites) that can
//     never carry a delivery.
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

		// Cargo-capacity exclusion: a caller that opts in (RequireCargoCapacity)
		// drops 0-cargo hulls, because a probe/satellite can never carry a
		// contract delivery - claiming it just spawns a worker that dies
		// instantly on 'deliveries not complete'. Logged by name so the captain
		// can see WHY a mispinned hull is being ignored (honest exclusion), and
		// counted into the summary below so an all-probe fleet reads as "0
		// dispatchable, N excluded for 0 cargo" rather than a silent empty pool.
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
