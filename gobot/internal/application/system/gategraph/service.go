// Package gategraph resolves multi-jump routes over the persisted cross-system
// jump-gate adjacency (sp-7gr2). It is the fix for the single-edge assumption
// that crashed a laden frigate at the home gate: travel() assumed origin→dest
// was ONE jump, but JP61 is three jumps from KA42 (PA3→UQ16→JP61). The service
// caches the API's own gate topology in a GateEdgeRepository, refreshes it
// lazily on miss/staleness, and walks it with a bounded BFS to produce the
// ordered hop path travel() executes and the routability check the pre-buy
// guard runs BEFORE spending.
package gategraph

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateAPI is the narrow slice of the SpaceTraders API the gate graph needs: the
// live gate-connections fetch, plus the per-gate construction probe that learns
// whether a connected gate is still being built (sp-8qhu). Narrowing the
// dependency (vs. the full ports.APIClient) states exactly what this service
// touches and keeps the fetch-through path unit-testable with a tiny fake.
type gateAPI interface {
	GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.JumpGateData, error)
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.WaypointDetail, error)
}

// MaxJumpPath bounds how many jumps a resolved route may contain. The charted
// cluster is a handful of systems wide and the deepest known route (KA42→JP61)
// is three jumps, so five is generous headroom while still capping the BFS
// against a pathological fetch storm over the uncharted frontier.
const MaxJumpPath = 5

// ErrUnroutable wraps every "no path exists within the bound" outcome so callers
// can distinguish a DEFINITIVE unroutable verdict (refuse the buy cleanly) from a
// store/fetch FAILURE (fail closed for a different reason). Both refuse a spend;
// only the latter is an operational error.
var ErrUnroutable = errors.New("no jump-gate route")

// Service resolves and caches gate routes. Its dependencies mirror
// GetJumpGateConnectionsHandler's (apiClient for the live gate fetch, graphProvider
// to find a charted system's own gate, playerRepo for the token) plus the edge
// store that makes the topology persistent and multi-hop-walkable.
type Service struct {
	store         system.GateEdgeRepository
	apiClient     gateAPI
	graphProvider system.ISystemGraphProvider
	playerRepo    player.PlayerRepository
}

// NewService wires the gate-graph service.
func NewService(
	store system.GateEdgeRepository,
	apiClient gateAPI,
	graphProvider system.ISystemGraphProvider,
	playerRepo player.PlayerRepository,
) *Service {
	return &Service{
		store:         store,
		apiClient:     apiClient,
		graphProvider: graphProvider,
		playerRepo:    playerRepo,
	}
}

// Connections returns systemSymbol's directly-reachable neighbor edges,
// fetch-through: a fresh cache hit is returned as-is; a miss or a stale set
// triggers a single live GetJumpGate fetch which is then persisted (replacing the
// system's old set) and returned. Errors are surfaced, never swallowed — a
// routability guard that cannot read the graph must fail closed, not fail open.
func (s *Service) Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error) {
	edges, ok, err := s.store.Edges(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if ok {
		return edges, nil
	}
	return s.fetchAndStore(ctx, systemSymbol, playerID)
}

// fetchAndStore resolves systemSymbol's own gate, fetches its live connections,
// persists the fresh edge set, and returns it. The gate is resolved from the
// store first (a neighbor may have recorded it — this is the path that expands an
// UNCHARTED system), falling back to the charted system's graph.
func (s *Service) fetchAndStore(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error) {
	token, err := s.token(ctx, playerID)
	if err != nil {
		return nil, err
	}

	gateWaypoint, ok, err := s.store.GateWaypointOf(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}
	if !ok {
		gateWaypoint, err = s.gateFromGraph(ctx, systemSymbol, playerID)
		if err != nil {
			return nil, err
		}
	}

	gateData, err := s.apiClient.GetJumpGate(ctx, systemSymbol, gateWaypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch jump gate connections for %s (%s): %w", systemSymbol, gateWaypoint, err)
	}

	logger := logging.LoggerFromContext(ctx)
	seen := make(map[string]bool, len(gateData.Connections))
	edges := make([]system.GateEdge, 0, len(gateData.Connections))
	for _, connWaypoint := range gateData.Connections {
		connSystem := shared.ExtractSystemSymbol(connWaypoint)
		if connSystem == systemSymbol || seen[connSystem] {
			continue
		}
		seen[connSystem] = true
		edges = append(edges, system.GateEdge{
			ConnectedSystem:   connSystem,
			GateWaypoint:      connWaypoint,
			UnderConstruction: s.gateUnderConstruction(ctx, connSystem, connWaypoint, token, logger),
		})
	}

	if err := s.store.Replace(ctx, systemSymbol, edges); err != nil {
		return nil, err
	}
	return edges, nil
}

// gateUnderConstruction resolves a connected gate's build state with a per-gate
// waypoint read (the API's jump-gate connections list carries symbols only). It
// FAILS CLOSED: any read failure is treated as under-construction so an
// unknown-state gate is never routed through (sp-8qhu — routing into an unbuilt
// gate crashes a laden hull at the hop). The cause is logged verbatim so the
// harbormaster can see why an edge went dark. The underlying API client applies
// its own rate limiting, so the one-GET-per-edge refresh needs no extra throttle.
func (s *Service) gateUnderConstruction(ctx context.Context, connSystem, gateWaypoint, token string, logger logging.ContainerLogger) bool {
	detail, err := s.apiClient.GetWaypoint(ctx, connSystem, gateWaypoint, token)
	if err != nil {
		logger.Log("WARNING", "gate construction probe failed — treating edge as under construction (fail closed)", map[string]interface{}{
			"system": connSystem,
			"gate":   gateWaypoint,
			"error":  err.Error(),
		})
		return true
	}
	return detail.IsUnderConstruction
}

// gateFromGraph finds systemSymbol's own jump-gate waypoint via its system graph
// — the charted-system path (mirrors GetJumpGateConnectionsHandler). Only used
// when no stored neighbor edge has already recorded the gate.
func (s *Service) gateFromGraph(ctx context.Context, systemSymbol string, playerID int) (string, error) {
	graphResult, err := s.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", fmt.Errorf("failed to get system graph for %s: %w", systemSymbol, err)
	}
	for _, waypoint := range graphResult.Graph.Waypoints {
		if waypoint.IsJumpGate() {
			return waypoint.Symbol, nil
		}
	}
	return "", fmt.Errorf("no jump gate found in system %s", systemSymbol)
}

// token loads the player's API token for the live gate fetch.
func (s *Service) token(ctx context.Context, playerID int) (string, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", fmt.Errorf("invalid player id %d: %w", playerID, err)
	}
	playerEntity, err := s.playerRepo.FindByID(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("failed to load player %d: %w", playerID, err)
	}
	return playerEntity.Token, nil
}

// Path returns the ordered system hop path from fromSystem to toSystem inclusive
// (a single element when they are equal; ≥2 for a real cross-system route),
// resolved by a bounded BFS over the fetch-through adjacency. It returns an
// ErrUnroutable-wrapped error naming both systems when no route exists within
// MaxJumpPath, or the underlying store/fetch error otherwise.
func (s *Service) Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error) {
	return bfsPath(fromSystem, toSystem, MaxJumpPath, func(systemSymbol string) ([]string, error) {
		edges, err := s.Connections(ctx, systemSymbol, playerID)
		if err != nil {
			return nil, err
		}
		neighbors := make([]string, 0, len(edges))
		for _, e := range edges {
			// Never traverse INTO an under-construction gate: a jump to an unbuilt
			// gate fails at hop time (sp-8qhu — the BFS picked KA42→AF2(unbuilt)→…
			// over the equal-hop valid PA3 route and the laden frigate crashed at
			// hop 1). Filtering the forward targets is what keeps the search on a
			// fully-built route; if that makes the dest unreachable, the caller gets
			// ErrUnroutable and the pre-buy guard refuses the spend.
			if e.UnderConstruction {
				continue
			}
			neighbors = append(neighbors, e.ConnectedSystem)
		}
		return neighbors, nil
	})
}

// Routable reports whether a route from→to exists. A DEFINITIVE unroutable
// verdict is (false, nil) — the caller refuses the spend but this is not an
// operational error; a store/fetch failure surfaces as (false, err) so the
// caller fails closed. Same-system is trivially routable.
func (s *Service) Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error) {
	if fromSystem == toSystem {
		return true, nil
	}
	_, err := s.Path(ctx, fromSystem, toSystem, playerID)
	if errors.Is(err, ErrUnroutable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Adjacency returns the stored gate adjacency (era-scoped), for the `system
// gates` overview. Pure store read — no fetch-through.
func (s *Service) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	return s.store.Adjacency(ctx)
}

// bfsPath is the pure breadth-first search over a neighbor function, extracted so
// the traversal is unit-testable against an in-memory adjacency with no store,
// API, or clock. It returns the shortest hop path (fewest jumps) from→to
// inclusive, bounded to maxJumps jumps. A path of J jumps has J+1 elements; a
// node is expanded only while it still has room for another jump. from==to is a
// zero-jump path. Neighbor-function errors abort the search immediately (fetch
// failures must not masquerade as unroutable).
func bfsPath(from, to string, maxJumps int, neighbors func(string) ([]string, error)) ([]string, error) {
	if from == to {
		return []string{from}, nil
	}

	visited := map[string]bool{from: true}
	queue := [][]string{{from}}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]

		// A path already at the jump bound cannot be extended without exceeding it.
		if len(path)-1 >= maxJumps {
			continue
		}

		last := path[len(path)-1]
		ns, err := neighbors(last)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if visited[n] {
				continue
			}
			next := make([]string, len(path), len(path)+1)
			copy(next, path)
			next = append(next, n)
			if n == to {
				return next, nil
			}
			visited[n] = true
			queue = append(queue, next)
		}
	}

	return nil, fmt.Errorf("%w from %s to %s within %d jumps", ErrUnroutable, from, to, maxJumps)
}
