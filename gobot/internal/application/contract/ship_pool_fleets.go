package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FleetHasMembers reports whether ANY ship - idle, busy, or in transit -
// currently carries the given DedicatedFleet tag. Unlike FindIdleShipsByFleet,
// which only surfaces dispatchable members, this answers a different
// question: does this coordinator have an exclusive fleet AT ALL right now?
//
// That distinction is what makes EXCLUSIVE MODE correct: a dedicated fleet
// that is fully busy must still block the coordinator from raiding the
// general pool. Only the absence of ANY tagged member falls back to shared
// hulls. Reading the persisted tag on every call (rather than trusting a
// remembered --dedicated-ships list) keeps this live with the same "no
// restart needed" guarantee FindIdleShipsByFleet already gives `fleet
// assign`/`unassign`.
//
//   - fleet: The fleet name to look up; "" always returns false, mirroring
//     FindIdleShipsByFleet's "no dedicated fleet" convention.
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

// OwnsHaulerHull reports whether the player owns ANY haul-capable HAULER-role hull at all —
// idle, busy, in transit, or pinned to another coordinator's exclusive fleet. It answers a
// different question from FindIdleLightHaulers: not "is a hauler free right now" (which the
// pool's last-resort admission already decides) but "does a hauler exist to prefer over the
// command frigate", the comparison FilterCommandCargoBaseline's economics rest on.
//
// The candidacy test is the pool's own, minus every availability condition: not the command
// frigate (it can never be the hauler it is compared against — IsCommandHull also matches the
// "*-1" symbol, so a mis-registered flagship never counts itself), role HAULER, and non-zero
// cargo (a probe/satellite mistagged HAULER single-trips nothing, so it is no hauler either).
func OwnsHaulerHull(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
) (bool, error) {
	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch ships: %w", err)
	}

	for _, ship := range allShips {
		if isCommandHull(ship) || ship.Role() != roleHauler || ship.CargoCapacity() == 0 {
			continue
		}
		return true, nil
	}
	return false, nil
}

// FindFleetMemberSymbols returns the symbols of EVERY ship currently carrying the
// given DedicatedFleet tag — idle, busy, or in transit — the LIVE membership of a
// coordinator's dedicated fleet. Unlike FindIdleShipsByFleet it applies no
// idle/role/cargo filter: pure membership by tag, because the callers that need it
// (the between-legs homing gate and the standby-station occupancy balancer) care who
// BELONGS to the fleet, not who is dispatchable right now.
//
// Reading the persisted tag on every call is what makes membership live: a hull
// added via `fleet add` (tag set, absent from the immutable --dedicated-ships launch
// list) is a member immediately, and a hull `fleet remove`d (tag cleared) drops out —
// no restart, and no dependence on the stale launch snapshot. The "" fleet returns
// nothing, mirroring FindIdleShipsByFleet / FleetHasMembers.
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

// fleetBySymbol fetches the player's current fleet snapshot indexed by ship
// symbol. Shared by the candidate filters (FilterUnrelatedCargo /
// FilterToHomeSystem) that resolve already-discovered candidate symbols against
// live ship state: a symbol absent from the returned map is not in the current
// fleet snapshot (sold/renamed since discovery) and those filters skip it.
func fleetBySymbol(ctx context.Context, playerID shared.PlayerID, shipRepo navigation.ShipRepository) (map[string]*navigation.Ship, error) {
	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ships: %w", err)
	}
	bySymbol := make(map[string]*navigation.Ship, len(allShips))
	for _, ship := range allShips {
		bySymbol[ship.ShipSymbol()] = ship
	}
	return bySymbol, nil
}
