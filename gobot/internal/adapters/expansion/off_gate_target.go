package expansion

import (
	"context"
	"fmt"
	"math"
	"strings"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

const (
	// offGateBaseExplorationValue is the value every off-gate candidate carries for being
	// unexplored (all off-gate systems are uncharted by definition, so this is uniform).
	offGateBaseExplorationValue = 1
	// offGatePromisingTypeBonus is the extra exploration value a promising-type system earns.
	// It is the ONLY value differentiator available at selection time — the universe roster
	// carries no waypoint/market/shipyard counts for uncharted systems, only symbol, coords,
	// and type — so type is what distinguishes one unexplored candidate from another.
	offGatePromisingTypeBonus = 1
)

// gateAdjacencyReader is the narrow slice of the gate graph the off-gate selector reads: the
// whole persisted cross-system adjacency in one era-scoped read. The real *gategraph.Service
// (and the ExpansionScanner's GateGraph) satisfy it.
type gateAdjacencyReader interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// UniverseSystemsProvider serves the whole universe roster of systems and their galaxy
// coordinates. The UniverseSystemsCache satisfies it.
type UniverseSystemsProvider interface {
	AllSystems(ctx context.Context, playerID int) ([]system.SystemAPIData, error)
}

// OffGateWarpTargetSelector ranks off-gate systems — universe systems NOT on our gate
// network — by warp-fuel distance from the frontier EDGE (the nearest gate-connected system)
// and exploration value, and picks the nearest-highest-value one within warp range (slice B).
// It joins the universe roster (UniverseSystemsProvider) against the gate graph
// (gateAdjacencyReader): the adjacency's key set plus its edge targets are the on-network
// systems, and everything else in the roster is off-gate. Warp fuel reuses slice A's model
// (shared.FlightModeCruise.FuelCost of the inter-system distance). It implements the
// coordinator's commands.OffGateTargetSelector driven port. It SELECTS only — nothing warps.
type OffGateWarpTargetSelector struct {
	universe  UniverseSystemsProvider
	gateGraph gateAdjacencyReader
}

// NewOffGateWarpTargetSelector wires the selector over the universe roster and the gate graph.
func NewOffGateWarpTargetSelector(universe UniverseSystemsProvider, gateGraph gateAdjacencyReader) *OffGateWarpTargetSelector {
	return &OffGateWarpTargetSelector{universe: universe, gateGraph: gateGraph}
}

// SelectTarget returns the nearest-highest-value off-gate system within warp range, or
// found=false when no reachable off-gate candidate exists (empty roster, no gate-connected
// frontier edge to warp from, or every off-gate system out of range).
func (s *OffGateWarpTargetSelector) SelectTarget(ctx context.Context, playerID int, params expansionCmd.OffGateSelectionParams) (expansionCmd.OffGateTarget, bool, error) {
	roster, err := s.universe.AllSystems(ctx, playerID)
	if err != nil {
		return expansionCmd.OffGateTarget{}, false, fmt.Errorf("universe roster unreadable: %w", err)
	}
	adjacency, err := s.gateGraph.Adjacency(ctx)
	if err != nil {
		return expansionCmd.OffGateTarget{}, false, fmt.Errorf("gate adjacency unreadable: %w", err)
	}

	gateConnected := gateConnectedSet(adjacency)
	edges := frontierEdges(roster, gateConnected)
	if len(edges) == 0 {
		return expansionCmd.OffGateTarget{}, false, nil // no gate-connected frontier to warp FROM
	}

	best := expansionCmd.OffGateTarget{}
	bestScore := 0
	found := false
	for _, candidate := range roster {
		if gateConnected[candidate.Symbol] {
			continue // on-gate — not an exploration target
		}
		from, fuel := nearestEdgeWarp(candidate, edges)
		if fuel > params.WarpRangeFuel {
			continue // beyond a single warp's reach
		}
		value := explorationValue(candidate)
		score := params.ValueWeight*value - params.FuelWeight*fuel
		target := expansionCmd.OffGateTarget{
			SystemSymbol: candidate.Symbol,
			X:            candidate.X,
			Y:            candidate.Y,
			FromSystem:   from,
			WarpFuelCost: fuel,
			Value:        value,
		}
		if !found || betterOffGateTarget(score, target, bestScore, best) {
			best, bestScore, found = target, score, true
		}
	}
	return best, found, nil
}

// gateConnectedSet is the set of system symbols ON the gate network: every adjacency key
// plus every system those keys connect to. A universe system not in this set is off-gate.
func gateConnectedSet(adjacency map[string][]system.GateEdge) map[string]bool {
	set := make(map[string]bool, len(adjacency))
	for systemSymbol, edges := range adjacency {
		set[systemSymbol] = true
		for _, edge := range edges {
			set[edge.ConnectedSystem] = true
		}
	}
	return set
}

// frontierEdges is the set of gate-connected systems present in the universe roster (so they
// carry coordinates) — the frontier a warp launches FROM.
func frontierEdges(roster []system.SystemAPIData, gateConnected map[string]bool) []system.SystemAPIData {
	edges := make([]system.SystemAPIData, 0, len(gateConnected))
	for _, candidate := range roster {
		if gateConnected[candidate.Symbol] {
			edges = append(edges, candidate)
		}
	}
	return edges
}

// nearestEdgeWarp returns the nearest gate-connected frontier system to a candidate and the
// warp fuel that leg costs (slice A's CRUISE model over the inter-system distance).
func nearestEdgeWarp(target system.SystemAPIData, edges []system.SystemAPIData) (string, int) {
	nearest := ""
	best := math.MaxFloat64
	for _, edge := range edges {
		distance := math.Hypot(target.X-edge.X, target.Y-edge.Y)
		if distance < best {
			best = distance
			nearest = edge.Symbol
		}
	}
	return nearest, shared.FlightModeCruise.FuelCost(best)
}

// explorationValue scores a candidate's exploration worth: the uniform unexplored base plus a
// promising-type bonus.
func explorationValue(candidate system.SystemAPIData) int {
	if isPromisingSystemType(candidate.Type) {
		return offGateBaseExplorationValue + offGatePromisingTypeBonus
	}
	return offGateBaseExplorationValue
}

// isPromisingSystemType is a coarse SEED heuristic for exploration value: star systems (and
// hypergiants) reliably anchor multi-waypoint systems that host markets and shipyards,
// whereas black holes, nebulae, and unstable loci are likelier barren. The universe roster
// carries no density data for uncharted systems, so type is the only signal; slice C can
// refine this once a warped-in explorer charts real waypoint/market counts.
func isPromisingSystemType(systemType string) bool {
	if strings.Contains(systemType, "STAR") {
		return true
	}
	return systemType == "HYPERGIANT"
}

// betterOffGateTarget breaks the max-score comparison with a deterministic symbol tiebreak so
// selection is stable across ticks (a jittering pick would thrash the demand signal).
func betterOffGateTarget(score int, candidate expansionCmd.OffGateTarget, bestScore int, best expansionCmd.OffGateTarget) bool {
	if score != bestScore {
		return score > bestScore
	}
	return candidate.SystemSymbol < best.SystemSymbol
}
