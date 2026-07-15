package expansion

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This adapter implements expansionCmd.ExplorerDispatchPort (sp-a3yn, slice C) over slice-A's warp
// executor. The frontier coordinator decides WHO to warp WHERE (unit-tested at the port); this adapter
// performs the warp.
//
// INTEGRATION NOTE (the one live-API unknown, flagged for review): slice B's universe roster carries
// only a system's symbol + coords, NOT its waypoints, so the off-gate TARGET is a SYSTEM. A warp is
// issued to a WAYPOINT, so we resolve one here via the graph waypoint source (which lists a system's
// waypoints). If resolution yields nothing (an uncharted system whose waypoints the graph does not yet
// know), the dispatch fail-CLOSES: it logs and warps nothing — no strand, no spend. Slice-A
// ExecuteWarpRoute independently fail-closes on any fuel-strand or missing-warp-drive (ErrWarpWouldStrand
// / ErrShipHasNoWarpDrive), so even an imperfect arrival waypoint can only cause a logged refusal.

// warpRouteRunner is the slice-A warp entrypoint (satisfied by *ship.RouteExecutor.ExecuteWarpRoute).
type warpRouteRunner interface {
	ExecuteWarpRoute(ctx context.Context, ship *navigation.Ship, destinations []*shared.Waypoint, playerID shared.PlayerID) error
}

// shipBySymbolReader loads a ship by symbol (satisfied by navigation.ShipRepository).
type shipBySymbolReader interface {
	FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error)
}

// arrivalWaypointResolver lists a target system's waypoints so the dispatcher can pick a landing point
// (satisfied by *ship.GraphWaypointSource.ChartWaypoints).
type arrivalWaypointResolver interface {
	ChartWaypoints(ctx context.Context, systemSymbol string, playerID shared.PlayerID) ([]*shared.Waypoint, error)
}

// ExplorerWarpDispatcher warps a bought+dedicated explorer to an off-gate target via slice-A's
// ExecuteWarpRoute. The warp runs in a BACKGROUND goroutine so the frontier reconcile loop (a
// singleton serving every player) is never blocked for the minutes a multi-system warp takes; an
// in-flight set dedups a ship already being warped in the brief window before its nav status flips to
// IN_TRANSIT (the frontier's idle-only selection dedups the steady state).
type ExplorerWarpDispatcher struct {
	routes   warpRouteRunner
	ships    shipBySymbolReader
	arrivals arrivalWaypointResolver

	mu       sync.Mutex
	inFlight map[string]bool
}

// NewExplorerWarpDispatcher wires the dispatcher over the slice-A warp executor, the ship-by-symbol
// loader, and the arrival-waypoint resolver.
func NewExplorerWarpDispatcher(routes warpRouteRunner, ships shipBySymbolReader, arrivals arrivalWaypointResolver) *ExplorerWarpDispatcher {
	return &ExplorerWarpDispatcher{routes: routes, ships: ships, arrivals: arrivals, inFlight: make(map[string]bool)}
}

// DispatchExplorer implements expansionCmd.ExplorerDispatchPort. It loads the hull, resolves a landing
// waypoint in the off-gate system, and warps there in the background. Returns quickly; a warp failure
// is logged in the goroutine (non-fatal).
func (d *ExplorerWarpDispatcher) DispatchExplorer(ctx context.Context, playerID int, shipSymbol string, target expansionCmd.OffGateTarget) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	// Dedup: never launch a second warp for a ship already in flight (guards the window before its
	// IN_TRANSIT status is persisted, which the frontier's idle-only selection then relies on).
	d.mu.Lock()
	if d.inFlight[shipSymbol] {
		d.mu.Unlock()
		return nil
	}
	d.inFlight[shipSymbol] = true
	d.mu.Unlock()

	ship, err := d.ships.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil || ship == nil {
		d.clearInFlight(shipSymbol)
		return fmt.Errorf("explorer %s not loadable for warp: %w", shipSymbol, err)
	}
	destination, err := d.arrivalWaypoint(ctx, target, pid)
	if err != nil {
		d.clearInFlight(shipSymbol)
		return err
	}

	// The frontier ctx is the coordinator's lifetime context (Handle passes it straight to
	// ReconcileOnce), so the warp survives the tick and is only cancelled on coordinator shutdown.
	go func() {
		defer d.clearInFlight(shipSymbol)
		logger := common.LoggerFromContext(ctx)
		if warpErr := d.routes.ExecuteWarpRoute(ctx, ship, []*shared.Waypoint{destination}, pid); warpErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Explorer warp %s → %s failed (slice-A fail-closed; ship stays put): %v", shipSymbol, target.SystemSymbol, warpErr), map[string]interface{}{
				"action": "explorer_warp_failed", "ship": shipSymbol, "target_system": target.SystemSymbol,
			})
			return
		}
		logger.Log("INFO", fmt.Sprintf("Explorer %s warped to %s and charted it — growFrontierGraph resumes next frontier cycle", shipSymbol, target.SystemSymbol), map[string]interface{}{
			"action": "explorer_warp_done", "ship": shipSymbol, "target_system": target.SystemSymbol,
		})
	}()
	return nil
}

// arrivalWaypoint resolves a concrete landing waypoint in the off-gate system, preferring a fuel
// station (so the explorer can top off to warp onward). Fails when the system's waypoints are unknown.
func (d *ExplorerWarpDispatcher) arrivalWaypoint(ctx context.Context, target expansionCmd.OffGateTarget, pid shared.PlayerID) (*shared.Waypoint, error) {
	waypoints, err := d.arrivals.ChartWaypoints(ctx, target.SystemSymbol, pid)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve arrival waypoint in off-gate system %s: %w", target.SystemSymbol, err)
	}
	var firstUsable *shared.Waypoint
	for _, waypoint := range waypoints {
		if waypoint == nil {
			continue
		}
		if firstUsable == nil {
			firstUsable = waypoint
		}
		if waypoint.HasFuel {
			return waypoint, nil // prefer a fuel-bearing arrival so the explorer can warp onward
		}
	}
	if firstUsable == nil {
		return nil, fmt.Errorf("no arrival waypoint known in off-gate system %s (uncharted) — fail closed, no warp", target.SystemSymbol)
	}
	return firstUsable, nil
}

func (d *ExplorerWarpDispatcher) clearInFlight(shipSymbol string) {
	d.mu.Lock()
	delete(d.inFlight, shipSymbol)
	d.mu.Unlock()
}
