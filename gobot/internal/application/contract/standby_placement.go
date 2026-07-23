package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// StandbyPlacementProvider resolves the ≤6 FIXED delivery placement slots for a player's home
// system each homing pass — the coordinator/idle-arb analogue of the scaler's armed-plan slots,
// backed by the SAME home-system role lookup + TopDeliverySlots selection the contract auto-scaler
// buys against (epic sp-9le3x / sp-mtgje), so every positioning consumer agrees on ONE slot set with
// no drift. The runtime homing then zips hulls onto these slots by symbol (domainContract.AssignedSlot)
// — NO demand, NO occupancy. A nil provider or an empty/error result leaves homing on the passed set
// (byte-identical fallback; a READ, never a config write, RULINGS #3).
type StandbyPlacementProvider interface {
	// StandbyPlacement returns the player's home-system ≤6 fixed delivery placement slots. An empty
	// (non-error) result is a valid "no placement resolved this pass" state, honored as-is.
	StandbyPlacement(ctx context.Context, playerID int) ([]string, error)
}

// ResolveStandbyForHoming returns the standby slot set this homing pass must use: a non-empty
// operator-pinned set (live `fleet hub` pins / launch snapshot) WINS untouched (RULINGS #2, no
// thrash — the operator override is respected); an EMPTY pin set is auto-driven by the provider's
// FIXED ≤6 placement slots (the sp-bu6ma auto hub-placement, now the fixed one-per-waypoint set that
// fixes the idle-hull pile-up with no manual pins). All nil-safe: a nil provider or a read error
// leaves the passed set unchanged (a positioning read that fails OPEN, never worse than before).
func ResolveStandbyForHoming(ctx context.Context, logger common.ContainerLogger, provider StandbyPlacementProvider, playerID int, stations []string) []string {
	if len(stations) > 0 {
		return stations // operator pins / launch snapshot win, untouched
	}
	if provider == nil {
		return stations
	}
	slots, err := provider.StandbyPlacement(ctx, playerID)
	if err != nil {
		if logger != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"failed to read standby placement for player %d (homing keeps the passed set): %v",
				playerID, err), nil)
		}
		return stations
	}
	return slots
}
