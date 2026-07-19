package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// A contract coordinator's standby stations (its "hubs") are the waypoints an
// idle dedicated hull homes to between legs. The persisted set is LIVE-MUTABLE:
// the daemon rewrites it in place on a running coordinator's container config
// (single writer, RULINGS #3, via the `fleet hub add|remove` RPC), and the
// coordinator RESOLVES it live each discovery pass rather than trusting the
// frozen launch snapshot. A live change survives a daemon restart because the
// restart rebuild reads the same (mutated) config key (RULINGS #2). Empty set →
// homing stays disabled.

// ApplyStandbyStationChange returns the standby-station set after adding or
// removing waypoint, plus whether the set actually changed. It is the pure hub-set
// mutation the daemon applies to a coordinator's persisted config: add is an
// order-preserving append that dedupes (adding a member already present is a
// no-op), remove drops the member (removing an absent one is a no-op). The
// waypoint is whitespace-trimmed so a stray-spaced CLI value can never create a
// phantom duplicate hub. changed=false lets the caller report an idempotent verb
// invocation honestly and skip a redundant DB write.
func ApplyStandbyStationChange(current []string, waypoint string, add bool) (result []string, changed bool) {
	waypoint = strings.TrimSpace(waypoint)

	present := false
	out := make([]string, 0, len(current)+1)
	for _, wp := range current {
		if wp == waypoint {
			present = true
			if add {
				out = append(out, wp) // keep it (dedupe: don't append a second copy)
			}
			// on remove: drop it by not appending
			continue
		}
		out = append(out, wp)
	}

	if add {
		if present {
			return out, false // already a hub — no-op
		}
		return append(out, waypoint), true
	}
	// remove
	if !present {
		return out, false // not a hub — no-op
	}
	return out, true
}

// StandbyStationProvider resolves the LIVE standby-station set for a coordinator
// container each discovery pass, the operation-level analogue of the live
// dedicated-fleet membership read. It is backed by the coordinator's
// OWN container config — the store the `fleet hub` daemon RPC mutates — so a hub
// added/removed live is visible to homing on the very next pass with no restart. A
// nil provider or a read error leaves the coordinator on the launch snapshot
// (never worse than the pre-fix behavior); an explicitly EMPTY live set disables
// homing (an operator clearing every hub must take effect live).
type StandbyStationProvider interface {
	// StandbyStations returns the coordinator container's current standby-station
	// set. An empty (non-error) result is a valid "homing disabled" state and is
	// honored, not overridden by any launch snapshot.
	StandbyStations(ctx context.Context, containerID string, playerID int) ([]string, error)
}

// ResolveStandbyStations returns the LIVE standby-station set the coordinator's
// homing must use this pass, mirroring resolveDedicatedMembersForHoming: the live
// provider is authoritative; a nil provider or a read error falls back to
// launchList so a transient failure is never worse than the frozen-launch-list
// behavior. An empty live set is returned verbatim (homing disabled) — it is NOT
// replaced by launchList, or a `fleet hub remove` of the last hub could never take
// effect while the coordinator ran.
func ResolveStandbyStations(
	ctx context.Context,
	logger common.ContainerLogger,
	provider StandbyStationProvider,
	containerID string,
	playerID int,
	launchList []string,
) []string {
	if provider == nil {
		return launchList
	}
	live, err := provider.StandbyStations(ctx, containerID, playerID)
	if err != nil {
		if logger != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"failed to read live standby-station set for coordinator %s (falling back to launch --standby-stations list): %v",
				containerID, err), nil)
		}
		return launchList
	}
	return live
}
