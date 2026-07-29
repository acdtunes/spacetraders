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
// plus every system those keys connect to THROUGH A GATE THAT EXISTS. A universe system not
// in this set is off-gate.
//
// AN UNBUILT GATE CONNECTS NOTHING. GateEdge.UnderConstruction means the neighbour's own gate
// is still being built, and the domain type says it outright: a route "must never traverse INTO
// such an edge — a jump to an unbuilt gate fails at hop time". Counting such a neighbour as
// on-network excluded it from off-gate selection, which is exactly backwards: a system whose
// only inbound gate can never be used is the single most valuable warp target there is. That
// misclassification is what made the whole off-gate slice unable to see the systems it exists
// to reach — measured live, every one of the 53 exits from this fleet's pocket is an
// under-construction edge, so the entire ring beyond the wall read as "already connected".
//
// STALE EDGES STAY CONNECTED, DELIBERATELY. Stale means UnderConstruction is UNVERIFIED
// (synced_at expired), so an unbuilt verdict cannot be trusted. Warping is the expensive move —
// a 769k hull burning fuel to cross interstellar distance — so the unverified case resolves the
// CONSERVATIVE way: treated as connected, hence not a target. Only a VERIFIED unbuilt gate
// promotes its neighbour to a warp candidate. That reading is not local taste, it is what the
// rest of the codebase already does with an unverified row: the escape reader refuses to call a
// stale edge a built gate (warp_escape_reader.hasBuiltGate), the chosen-path verify spends a live
// re-probe on one rather than trust it, and the store-only distance walk refuses to route through
// a stale system at all. A stale row is never an authoritative verdict anywhere, and it is not one
// here either.
//
// THIS SET IS NOT ONE-DIRECTIONAL, so do not read it as a money guard. Membership does two
// opposite things: it EXCLUDES a system as a warp target (a refusal to spend) and it ADMITS that
// system as a frontier origin in frontierEdges (nearestEdgeWarp minimises over origins, so an extra
// origin can only lower the computed fuel, which can only let MORE candidates past
// params.WarpRangeFuel). The stale rule therefore buys a refusal on one side and pays for it with a
// possible fuel understatement on the other, and pretending otherwise would be the dangerous
// comment to leave here. It is acceptable because of where the real guard lives: FromSystem and
// WarpFuelCost are OBSERVABILITY ONLY — nothing navigates to FromSystem, the dispatcher warps the
// hull from wherever it actually is, and slice-A's ExecuteWarpRoute independently refuses an
// unaffordable or stranding leg (ErrWarpWouldStrand) before a drop of fuel is burned. WarpRangeFuel
// is the cheap first filter; the strand check is the safety one, and it is untouched by any of this.
//
// An adjacency KEY is always on-network regardless of its edges. A key exists only because we
// successfully read that system's OWN jump gate (gategraph writes the key after GetJumpGate
// returns), which is the same fact the gate-walking machinery treats as membership in the network.
// Note it is NOT the stronger claim that a hull of ours stood there — GateWaypointOf exists
// precisely so an uncharted neighbour's gate can be read from a neighbour's connection list — but
// membership in the gate graph is the question being asked, and a read gate answers it.
func gateConnectedSet(adjacency map[string][]system.GateEdge) map[string]bool {
	set := make(map[string]bool, len(adjacency))
	for systemSymbol, edges := range adjacency {
		set[systemSymbol] = true
		for _, edge := range edges {
			if edge.UnderConstruction && !edge.Stale {
				continue // verified unbuilt: this gate connects nothing, so the neighbour is off-gate
			}
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
