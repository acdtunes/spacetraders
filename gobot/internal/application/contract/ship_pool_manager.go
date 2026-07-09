package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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

// Ship roles that can be drafted to haul contract cargo.
const (
	roleHauler  = "HAULER"
	roleCommand = "COMMAND"
)

// FindIdleLightHaulers finds all idle haul-capable ships for a player.
//
// A ship is a candidate if:
//  1. Its role is "HAULER" - or "COMMAND" when the caller passes IncludeCommandShip
//  2. It is not dedicated to a coordinator's exclusive fleet (Ship.DedicatedFleet() is empty)
//  3. It has cargo capacity (excludes probes/satellites)
//  4. It is not in transit and has no active assignment (Ship.IsIdle() is true)
//
// This provides a dynamic pool of available haulers without requiring pre-assignment.
// Ship assignment status is now embedded in the Ship aggregate and enriched by the repository.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships (enriches assignment data automatically)
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
	var idleHaulerSymbols []string

	// Track whether ANY haul-capable hull exists (regardless of availability),
	// purely for the discovery log below.
	candidateShipsExist := false

	for _, ship := range allShips {
		// Candidacy by role. Haulers always qualify. The command ship (role
		// COMMAND, symbol "*-1") qualifies only when the caller opts in: it
		// hauls contract legs fine and is often the fastest, largest-cargo hull
		// owned, so contracts treat it as a first-class candidate (sp-4a4e)
		// rather than benching it until zero haulers remain. Manufacturing
		// keeps it reserved by not opting in.
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

		// Claim-filter (sp-snmb): a ship dedicated to a coordinator (via the
		// operator-configured --dedicated-ships list) is invisible to this
		// general-purpose pool, unconditionally. Every caller of this function
		// (contract, manufacturing, factory, balance-handler) shares this one
		// exclusion "for free" - the contract coordinator finds its own
		// dedicated ships separately via FindIdleDedicatedShips.
		if ship.DedicatedFleet() != "" {
			continue
		}

		// Must have cargo capacity (excludes probes/satellites tagged as haulers)
		if ship.CargoCapacity() == 0 {
			continue
		}

		// At least one haul-capable hull exists in the fleet.
		candidateShipsExist = true

		// Exclude ships in transit (even without assignment): a hull being
		// balanced or navigating is not available for a new contract leg.
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}

		// Only idle ships (no active assignment). Ship.IsIdle() checks the
		// embedded assignment state.
		if ship.IsIdle() {
			idleHaulers = append(idleHaulers, ship)
			idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())
		}
	}

	logger.Log("INFO", "Idle light haulers discovered", map[string]interface{}{
		"action":                "find_idle_haulers",
		"total_ships":           len(allShips),
		"candidate_ships_exist": candidateShipsExist,
		"include_command_ship":  policy == IncludeCommandShip,
		"idle_haulers":          len(idleHaulers),
		"hauler_symbols":        idleHaulerSymbols,
	})

	return idleHaulers, idleHaulerSymbols, nil
}

// FindIdleDedicatedShips looks up the contract coordinator's own reserved
// fleet by symbol - the operator-supplied --dedicated-ships list - and
// returns only the ones currently idle. Busy ships and unknown/not-yet-owned
// symbols are silently skipped rather than erroring, since fleet composition
// legitimately varies over the coordinator's lifetime (sp-snmb).
//
// Unlike FindIdleLightHaulers, this does not filter by role or cargo
// capacity: a ship qualifies purely by being named on the operator's list,
// whatever hull it is. The list itself is the authorization - there is no
// need to also check Ship.DedicatedFleet(), since the caller already knows
// which ships it dedicated.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships (enriches assignment data automatically)
//   - dedicatedShips: Ship symbols reserved for this coordinator (operator-supplied)
//
// Returns:
//   - ships: List of idle dedicated ship entities
//   - shipSymbols: List of idle dedicated ship symbols (for convenience)
//   - error: Any error encountered
func FindIdleDedicatedShips(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	dedicatedShips []string,
) ([]*navigation.Ship, []string, error) {
	if len(dedicatedShips) == 0 {
		return nil, nil, nil
	}

	logger := common.LoggerFromContext(ctx)

	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	wanted := make(map[string]bool, len(dedicatedShips))
	for _, symbol := range dedicatedShips {
		wanted[symbol] = true
	}

	var idleShips []*navigation.Ship
	var idleSymbols []string
	for _, ship := range allShips {
		if !wanted[ship.ShipSymbol()] {
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

	logger.Log("INFO", "Idle dedicated ships discovered", map[string]interface{}{
		"action":          "find_idle_dedicated_ships",
		"dedicated_total": len(dedicatedShips),
		"idle_dedicated":  len(idleSymbols),
		"ship_symbols":    idleSymbols,
	})

	return idleShips, idleSymbols, nil
}

// isCommandShip checks if a ship symbol represents the command ship (ship #1).
//
// Ship symbols ending in "-1" are considered command ships (e.g., "TORWIND-1", "AGENT-1").
func isCommandShip(shipSymbol string) bool {
	return strings.HasSuffix(shipSymbol, "-1")
}

// isCommandHull reports whether a ship is the command ship, by registration role
// or by the conventional "*-1" symbol. Candidate discovery and the selection log
// share this predicate so the log marks exactly the hull the pool treats as the
// command ship.
func isCommandHull(ship *navigation.Ship) bool {
	return ship.Role() == roleCommand || isCommandShip(ship.ShipSymbol())
}
