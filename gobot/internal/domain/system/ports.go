package system

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WaypointRepository defines waypoint persistence operations
type WaypointRepository interface {
	FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error)
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
	Add(ctx context.Context, waypoint *shared.Waypoint) error
}

// SystemGraphRepository defines operations for system graph persistence
type SystemGraphRepository interface {
	// Get retrieves a graph for a system from cache
	Get(ctx context.Context, systemSymbol string) (*NavigationGraph, error)

	// Add persists a graph for a system
	Add(ctx context.Context, systemSymbol string, graph *NavigationGraph) error
}

// IGraphBuilder defines operations for building system graphs from API
type IGraphBuilder interface {
	// BuildSystemGraph fetches all waypoints from API and builds navigation graph
	// Returns strongly-typed NavigationGraph with waypoints and edges
	BuildSystemGraph(ctx context.Context, systemSymbol string, playerID int) (*NavigationGraph, error)
}

// ISystemGraphProvider defines operations for system graph management with caching
type ISystemGraphProvider interface {
	// GetGraph retrieves system navigation graph (checks cache first, builds from API if needed)
	// playerID is required for API authentication when building graphs
	GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool, playerID int) (*GraphLoadResult, error)
}

// IWaypointProvider defines operations for waypoint data retrieval with auto-fetch
type IWaypointProvider interface {
	// GetWaypoint retrieves waypoint data (auto-fetches from API if not cached)
	// Flow: Try database cache → build system graph if needed → retry database
	// playerID is required for API authentication when building the system graph
	GetWaypoint(ctx context.Context, waypointSymbol, systemSymbol string, playerID int) (*shared.Waypoint, error)
}

// IWaypointConverter defines operations for converting graph data to domain Waypoint objects
type IWaypointConverter interface {
	// ConvertGraphToWaypoints converts graph waypoints to Waypoint objects with optional trait enrichment
	// Args:
	//   graph: Graph structure with waypoints data (map[string]interface{})
	//   waypointTraits: Optional lookup map of Waypoint objects with full trait data
	// Returns:
	//   Map of waypoint_symbol -> Waypoint objects
	ConvertGraphToWaypoints(graph map[string]interface{}, waypointTraits map[string]*shared.Waypoint) map[string]*shared.Waypoint
}

// DTOs for system operations

type WaypointAPIData struct {
	Symbol   string
	Type     string
	X        float64
	Y        float64
	Traits   []map[string]interface{}
	Orbitals []map[string]string
}

type WaypointsListResponse struct {
	Data []WaypointAPIData
	Meta PaginationMeta
}

type PaginationMeta struct {
	Total int
	Page  int
	Limit int
}

type GraphLoadResult struct {
	Graph   *NavigationGraph
	Source  string // "database" or "api"
	Message string
}

type WaypointData struct {
	Symbol  string
	X       float64
	Y       float64
	HasFuel bool
}

// GateEdge is one directed jump-gate connection: from a system to a neighbor
// system reachable in a single jump, tagged with that NEIGHBOR's own jump-gate
// waypoint (the raw API connection symbol, e.g. "X1-PA3-I51"). Storing the
// neighbor's gate (not the origin's) is what lets a multi-jump BFS expand an
// UNCHARTED neighbor without first charting its system graph — letting
// satellites push outward past the charted frontier.
type GateEdge struct {
	ConnectedSystem string
	GateWaypoint    string
	// UnderConstruction is true when the NEIGHBOR's own jump gate is still being
	// built (the API's isUnderConstruction). A multi-jump route must never traverse
	// INTO such an edge — a jump to an unbuilt gate fails at hop time. Resolved
	// per-edge on refresh; fails CLOSED (treated true) when the neighbor's build
	// state cannot be read.
	UnderConstruction bool
	// Stale is set only by Adjacency (the `system gates` overview): the row is a
	// raw cache dump whose synced_at is empty/expired, so its UnderConstruction
	// value is UNVERIFIED and will be re-probed on the next routing lookup. Routing
	// reads (Edges) never surface a stale edge — a stale set reads as a miss and is
	// re-fetched — so this is always false on the routing path. The verb
	// annotates a stale edge distinctly so the captain's chart never presents an
	// invalidated row as an authoritative built/unbuilt verdict.
	Stale bool
}

// GateEdgeRepository persists the cross-system jump-gate adjacency.
// Every read is era-scoped (dead-era rows must never leak into live
// routing); writes stamp the open era and REPLACE a system's whole edge set so
// a connection that has since disappeared cannot linger. The gate graph is the
// API's own truth, cached here and refreshed lazily on miss/staleness.
type GateEdgeRepository interface {
	// Edges returns systemSymbol's stored neighbor edges, era-scoped. ok=false on
	// a genuine miss OR when the stored rows are older than the freshness window
	// (both are lazy-refresh signals the caller resolves by fetching live).
	Edges(ctx context.Context, systemSymbol string) (edges []GateEdge, ok bool, err error)
	// GateWaypointOf returns systemSymbol's OWN jump-gate waypoint if any stored
	// edge points AT it (a charted neighbor recorded it as a connection). This is
	// the reverse lookup that lets an uncharted system be fetched without its
	// system graph. ok=false when no neighbor has recorded it yet.
	GateWaypointOf(ctx context.Context, systemSymbol string) (gateWaypoint string, ok bool, err error)
	// Replace atomically swaps systemSymbol's stored edge set for edges, stamping
	// the open era and a fresh sync timestamp. An empty edges slice clears the
	// system's rows (a system whose gate genuinely connects nowhere). A successful
	// Replace also clears any negative-result backoff marker for the system — a gate
	// that becomes readable again re-probes on the normal TTL, not the backoff clock.
	Replace(ctx context.Context, systemSymbol string, edges []GateEdge) error
	// Adjacency returns every stored system's neighbor edges (era-scoped), for the
	// `system gates` overview. Edges carry UnderConstruction so the verb can mark
	// unbuilt gates the captain must not chart a route through. Pure read — no live
	// fetch-through. Negative-result backoff markers are NOT edges and are excluded.
	Adjacency(ctx context.Context) (map[string][]GateEdge, error)
	// UnreadableState returns the persisted negative-result backoff for a system whose
	// live gate fetch keeps failing: the consecutive-failure count and the
	// timestamp of the last failed probe. ok=false when the system is NOT backed off
	// (never failed in this era, or the backoff was cleared by a successful Replace).
	// The caller derives the next-probe time from these two facts and its backoff
	// schedule; storing the raw facts (not a computed deadline) keeps the schedule a
	// config knob (RULINGS #5). Era-scoped exactly like Edges, so an era close resets
	// the backoff along with the rest of the gate cache.
	UnreadableState(ctx context.Context, systemSymbol string) (attempts int, lastProbe time.Time, ok bool, err error)
	// MarkUnreadable records (or extends) the negative-result backoff for a system whose
	// live gate fetch just failed: it increments the consecutive-failure count and stamps
	// now as the last-probe time, returning the new count. Persisted (not in-memory) so a
	// daemon restart resumes the backoff instead of re-storming the API every tick
	// (RULINGS #2). gateWaypoint is the system's own gate (recorded for observability;
	// may be empty when not yet known).
	MarkUnreadable(ctx context.Context, systemSymbol, gateWaypoint string, now time.Time) (attempts int, err error)
}
