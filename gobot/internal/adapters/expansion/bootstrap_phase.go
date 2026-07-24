package expansion

// BootstrapExpansionPhaseReader backs the probe-buyer-fleet coordinator's EXPANSION phase gate
// (sp-f3mcc): it re-derives the bootstrap lifecycle's EXPANSION signal from the LIVE world, exactly
// the way bootstrap's own derivePhase does (sp-feiy7) — EXPANSION ⇔ the home-system jump-gate
// construction is COMPLETE. Deriving (not reading a stored phase) is the only honest seam: the
// bootstrap phase is never persisted (a stored enum can desync — the reconciler doctrine), and the
// bootstrap container EXITS after its hand-off, so there is nothing running to ask. The reads reuse
// the established repositories, mirroring bootstrap_ports.go's observer:
//   - home system = the COMMAND hull's system (fallback: any located hull's), from the ship repo;
//   - the JUMP_GATE waypoint from the waypoint repo (same discovery readBootstrapGateSnapshot does);
//   - the site's completeness from the construction-site repository (the same live-API read the
//     jump command's sourceGateComplete legality check uses — era-safe by construction).
//
// Error semantics (the coordinator FAILS CLOSED on them, RULINGS #4): any unreadable input — fleet,
// waypoints, construction site, or an unresolvable home system — returns an error. A READABLE world
// that is simply pre-EXPANSION (gate under construction, or no gate waypoint known yet) returns
// (false, nil). Consistent with derivePhase, an under-construction home gate always reads
// pre-EXPANSION even if another (hypothetical) built gate shares the system.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// jumpGateWaypointType is the waypoint type of a jump gate (matches the bootstrap observer's
	// gate-site discovery).
	jumpGateWaypointType = "JUMP_GATE"
	// commandRoleName is the flagship's registration role — its system is the bootstrap home system.
	commandRoleName = "COMMAND"
)

// phaseFleetLister is the ship-roster slice the home-system resolution needs (satisfied by
// navigation.ShipRepository).
type phaseFleetLister interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// phaseWaypointLister lists a system's known waypoints (satisfied by the persistence waypoint repo).
type phaseWaypointLister interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// phaseConstructionReader reads a construction site's live state (satisfied by the API-backed
// manufacturing.ConstructionSiteRepository).
type phaseConstructionReader interface {
	FindByWaypoint(ctx context.Context, waypointSymbol string, playerID int) (*manufacturing.ConstructionSite, error)
}

// BootstrapExpansionPhaseReader derives the EXPANSION phase signal from the live world.
type BootstrapExpansionPhaseReader struct {
	ships        phaseFleetLister
	waypoints    phaseWaypointLister
	construction phaseConstructionReader
}

// NewBootstrapExpansionPhaseReader wires the reader over the established repositories.
func NewBootstrapExpansionPhaseReader(
	ships phaseFleetLister,
	waypoints phaseWaypointLister,
	construction phaseConstructionReader,
) *BootstrapExpansionPhaseReader {
	return &BootstrapExpansionPhaseReader{ships: ships, waypoints: waypoints, construction: construction}
}

// InExpansion reports whether the bootstrap-derived phase is EXPANSION: the home-system jump-gate
// construction is COMPLETE. Errors mean the phase is UNREADABLE (the caller fails closed).
func (r *BootstrapExpansionPhaseReader) InExpansion(ctx context.Context, playerID shared.PlayerID) (bool, error) {
	home, err := r.homeSystem(ctx, playerID)
	if err != nil {
		return false, fmt.Errorf("resolve home system: %w", err)
	}
	if home == "" {
		return false, fmt.Errorf("no home system resolvable (no located hull)")
	}
	waypoints, err := r.waypoints.ListBySystem(ctx, home)
	if err != nil {
		return false, fmt.Errorf("list %s waypoints: %w", home, err)
	}
	built := false
	for _, wp := range waypoints {
		if wp == nil || wp.Type != jumpGateWaypointType {
			continue
		}
		site, serr := r.construction.FindByWaypoint(ctx, wp.Symbol, playerID.Value())
		if serr != nil {
			return false, fmt.Errorf("read construction site %s: %w", wp.Symbol, serr)
		}
		if site == nil {
			continue
		}
		if !site.IsComplete() {
			// An under-construction home gate: the world is still building (DATA/INCOME/GATE at the
			// bootstrap derivation) — pre-EXPANSION regardless of any other gate in the system.
			return false, nil
		}
		built = true
	}
	return built, nil
}

// homeSystem mirrors the bootstrap observer's resolution: the COMMAND hull's system, else the first
// located hull's system, else "" (the caller treats that as unreadable).
func (r *BootstrapExpansionPhaseReader) homeSystem(ctx context.Context, playerID shared.PlayerID) (string, error) {
	ships, err := r.ships.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return "", err
	}
	anyHome := ""
	for _, ship := range ships {
		location := ship.CurrentLocation()
		if location == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(location.Symbol)
		if ship.Role() == commandRoleName {
			return system, nil
		}
		if anyHome == "" {
			anyHome = system
		}
	}
	return anyHome, nil
}
