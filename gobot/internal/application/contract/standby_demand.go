package contract

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// StandbyDemandProvider resolves the per-central-park DEMAND weight map the coordinator's
// between-legs homing ranks its standby set by — the coordinator-side analogue of
// StandbyStationProvider, backed by the SAME home-system role lookup + import-volume demand
// the contract auto-scaler buys against (epic sp-9le3x), so both positioning consumers rank
// parks by ONE demand definition. The map is coord-deduped (one representative per distinct
// LOCATION) so idle hulls spread across distinct planets, not co-located waypoints. Nil
// provider or an empty/error result leaves homing on plain occupancy+nearest balancing
// (byte-identical to the pre-fix behavior — the resolver is a READ, never a config write,
// RULINGS #3).
type StandbyDemandProvider interface {
	// StandbyDemand returns the per-park demand weight map for the player's home system.
	// An empty (non-error) result is a valid "no demand signal" state, honored as uniform.
	StandbyDemand(ctx context.Context, playerID int) (map[string]float64, error)
}

// ResolveStandbyDemand returns the per-park demand map for this homing pass, nil-safe: a nil
// provider or a read error yields nil (uniform → plain occupancy+nearest homing, never worse
// than the pre-fix behavior — a positioning read that fails OPEN, RULINGS #4).
func ResolveStandbyDemand(ctx context.Context, logger common.ContainerLogger, provider StandbyDemandProvider, playerID int) map[string]float64 {
	if provider == nil {
		return nil
	}
	demand, err := provider.StandbyDemand(ctx, playerID)
	if err != nil {
		if logger != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"failed to read standby demand for player %d (homing degrades to occupancy+nearest balancing): %v",
				playerID, err), nil)
		}
		return nil
	}
	return demand
}

// ResolveStandbyForHoming augments an already-resolved station set with its demand map and
// the empty-set auto-fallback. A non-empty station set (live `fleet hub` pins) is kept and
// merely RANKED by demand (operator pins win — RULINGS #2, no thrash); an EMPTY set is
// auto-driven by the role-resolved central parks (the demand map's keys, sorted), the
// sp-bu6ma auto hub-placement that fixes the idle-hull pile-up with no manual pins. All
// nil-safe: no demand signal → the set is returned untouched with a nil demand map.
func ResolveStandbyForHoming(ctx context.Context, logger common.ContainerLogger, provider StandbyDemandProvider, playerID int, stations []string) ([]string, map[string]float64) {
	demand := ResolveStandbyDemand(ctx, logger, provider, playerID)
	if len(stations) == 0 && len(demand) > 0 {
		stations = sortedKeys(demand)
	}
	return stations, demand
}

// sortedKeys returns the demand map's keys in ascending symbol order — the deterministic
// auto-resolved standby set (the engine re-ranks by demand; sorting only fixes the order
// for config/logging determinism).
func sortedKeys(demand map[string]float64) []string {
	keys := make([]string, 0, len(demand))
	for k := range demand {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
