package ship

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// SystemCharter charts a whole star system on warp arrival (sp-0xd0). When a
// SHIP_EXPLORER warps into a fresh cluster off the jump-gate network, this is
// what makes the destination discoverable to the rest of the fleet: it persists
// the new system's jump-gate edges (so the cheap gate-hopping probe frontier -
// sp-dc50 growFrontierGraph / gategraph - can resume expanding from the new
// cluster), its waypoints, and the market + shipyard telemetry at each.
//
// It is a driven collaborator the RouteExecutor delegates to on arrival, exactly
// as it delegates market scanning to MarketScanner - charting is best-effort and
// never fails the warp that hosts it.
type SystemCharter interface {
	ChartSystem(ctx context.Context, systemSymbol string, playerID shared.PlayerID) error
}

// gateEdgeCharter is the narrow slice of *gategraph.Service the charter needs:
// the fetch-through Connections call that fetches a system's live jump-gate
// connections and PERSISTS them into gate_edges (the same store sp-dc50
// growFrontierGraph reads). Narrowed so the charter test needs no gate store.
type gateEdgeCharter interface {
	Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error)
}

// systemWaypointSource fetches AND persists a system's waypoints, returning them
// so the charter can route each marketplace/shipyard to its scanner. The
// production GraphWaypointSource fulfils it via the system-graph provider, whose
// GetGraph fetches-through and caches the system's full waypoint set on a miss -
// i.e. charting+persisting the waypoints as a side effect of listing them.
type systemWaypointSource interface {
	ChartWaypoints(ctx context.Context, systemSymbol string, playerID shared.PlayerID) ([]*shared.Waypoint, error)
}

// GraphWaypointSource is the production systemWaypointSource: it fetches (and, on
// a cache miss, persists) a system's full waypoint set through the system-graph
// provider, then returns the waypoints for telemetry scanning. GetGraph is the
// codebase's existing fetch-through-and-cache path for a system's waypoints, so
// reusing it IS the "chart + persist waypoints" step - no new waypoint-ingest code.
type GraphWaypointSource struct {
	graphProvider system.ISystemGraphProvider
}

// NewGraphWaypointSource wires the production waypoint source over the system-graph provider.
func NewGraphWaypointSource(graphProvider system.ISystemGraphProvider) *GraphWaypointSource {
	return &GraphWaypointSource{graphProvider: graphProvider}
}

// ChartWaypoints returns the destination system's waypoints, fetching+caching them
// via the graph provider. A nil graph is treated as an empty system rather than a
// crash, since charting is best-effort.
func (g *GraphWaypointSource) ChartWaypoints(ctx context.Context, systemSymbol string, playerID shared.PlayerID) ([]*shared.Waypoint, error) {
	result, err := g.graphProvider.GetGraph(ctx, systemSymbol, false, playerID.Value())
	if err != nil {
		return nil, err
	}
	if result == nil || result.Graph == nil {
		return nil, nil
	}
	waypoints := make([]*shared.Waypoint, 0, len(result.Graph.Waypoints))
	for _, waypoint := range result.Graph.Waypoints {
		waypoints = append(waypoints, waypoint)
	}
	return waypoints, nil
}

// marketChartScanner / shipyardChartScanner are the narrow scan hooks the charter
// reuses (satisfied by *MarketScanner / *ShipyardScanner) - the SAME hooks
// RouteExecutor fires on a marketplace gate arrival, now driven across every
// waypoint of a freshly warped-to system.
type marketChartScanner interface {
	ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error
}

type shipyardChartScanner interface {
	ScanAndSaveShipyard(ctx context.Context, playerID uint, waypointSymbol string) error
}

// WarpSystemCharter is the production SystemCharter. It fans a single
// chart-on-arrival request out to the four persistence deliverables, each
// best-effort: a failure in one (a frontier gate that will not read, an
// unreachable market) is logged and the rest still run, so a partial chart still
// advances the frontier rather than aborting the whole arrival.
type WarpSystemCharter struct {
	gateCharter     gateEdgeCharter
	waypointSource  systemWaypointSource
	marketScanner   marketChartScanner
	shipyardScanner shipyardChartScanner
}

// NewWarpSystemCharter wires the production charter. Any collaborator may be nil,
// in which case that deliverable is skipped (keeps the charter usable in partial
// wirings and slice-A bring-up where only the gate graph is required).
func NewWarpSystemCharter(
	gateCharter gateEdgeCharter,
	waypointSource systemWaypointSource,
	marketScanner marketChartScanner,
	shipyardScanner shipyardChartScanner,
) *WarpSystemCharter {
	return &WarpSystemCharter{
		gateCharter:     gateCharter,
		waypointSource:  waypointSource,
		marketScanner:   marketScanner,
		shipyardScanner: shipyardScanner,
	}
}

// ChartSystem persists the destination system's gate edges, waypoints, markets,
// and shipyards. Every step is best-effort and logged on failure; the method
// returns nil so an arrival is never failed by a charting hiccup (the warp has
// already physically happened - the ship IS in the new system).
func (c *WarpSystemCharter) ChartSystem(ctx context.Context, systemSymbol string, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)

	c.chartGateEdges(ctx, systemSymbol, playerID, logger)
	waypoints := c.chartWaypoints(ctx, systemSymbol, playerID, logger)
	c.scanWaypointTelemetry(ctx, waypoints, playerID, logger)

	return nil
}

// chartGateEdges persists the system's jump-gate connections - the deliverable
// that lets the gate-hopping probe frontier resume from the new cluster.
func (c *WarpSystemCharter) chartGateEdges(ctx context.Context, systemSymbol string, playerID shared.PlayerID, logger common.ContainerLogger) {
	if c.gateCharter == nil {
		return
	}
	if _, err := c.gateCharter.Connections(ctx, systemSymbol, playerID.Value()); err != nil {
		logger.Log("WARNING", "Warp chart-on-arrival: gate-edge charting failed (non-fatal)", map[string]interface{}{
			"action": "warp_chart_gate_edges",
			"system": systemSymbol,
			"error":  err.Error(),
		})
	}
}

// chartWaypoints fetches+persists the system's waypoints and returns them for
// telemetry scanning. Returns nil on failure (telemetry scanning is then skipped).
func (c *WarpSystemCharter) chartWaypoints(ctx context.Context, systemSymbol string, playerID shared.PlayerID, logger common.ContainerLogger) []*shared.Waypoint {
	if c.waypointSource == nil {
		return nil
	}
	waypoints, err := c.waypointSource.ChartWaypoints(ctx, systemSymbol, playerID)
	if err != nil {
		logger.Log("WARNING", "Warp chart-on-arrival: waypoint charting failed (non-fatal)", map[string]interface{}{
			"action": "warp_chart_waypoints",
			"system": systemSymbol,
			"error":  err.Error(),
		})
		return nil
	}
	return waypoints
}

// scanWaypointTelemetry routes each charted waypoint to the market scanner (when
// it is a marketplace) and the shipyard scanner (which itself no-ops non-shipyard
// waypoints for zero API budget), mirroring RouteExecutor's arrival scan hooks.
func (c *WarpSystemCharter) scanWaypointTelemetry(ctx context.Context, waypoints []*shared.Waypoint, playerID shared.PlayerID, logger common.ContainerLogger) {
	for _, waypoint := range waypoints {
		c.scanMarket(ctx, waypoint, playerID, logger)
		c.scanShipyard(ctx, waypoint, playerID, logger)
	}
}

func (c *WarpSystemCharter) scanMarket(ctx context.Context, waypoint *shared.Waypoint, playerID shared.PlayerID, logger common.ContainerLogger) {
	if c.marketScanner == nil || !waypoint.IsMarketplace() {
		return
	}
	if err := c.marketScanner.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypoint.Symbol); err != nil {
		logger.Log("WARNING", "Warp chart-on-arrival: market scan failed (non-fatal)", map[string]interface{}{
			"action":   "warp_chart_market",
			"waypoint": waypoint.Symbol,
			"error":    err.Error(),
		})
	}
}

func (c *WarpSystemCharter) scanShipyard(ctx context.Context, waypoint *shared.Waypoint, playerID shared.PlayerID, logger common.ContainerLogger) {
	if c.shipyardScanner == nil {
		return
	}
	if err := c.shipyardScanner.ScanAndSaveShipyard(ctx, uint(playerID.Value()), waypoint.Symbol); err != nil {
		logger.Log("WARNING", "Warp chart-on-arrival: shipyard scan failed (non-fatal)", map[string]interface{}{
			"action":   "warp_chart_shipyard",
			"waypoint": waypoint.Symbol,
			"error":    err.Error(),
		})
	}
}
