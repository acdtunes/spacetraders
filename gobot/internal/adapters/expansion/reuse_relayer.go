package expansion

// reuse-before-buy the deep frontier. These adapters back the frontier coordinator's two new
// optional ports:
//   - ProbeReuseRelayer: hop an EXISTING probe from the charted edge onto a target virgin (the
//     deadlock fix), anchoring relay-reach to the nearest usable probe rather than the far buyer home.
//   - FrontierNeighborReader: the uncharted gate-neighbors of a charted system (the snowball walk).
//
// The coordinator's decision logic (reuse-before-buy ordering, the value ceiling threading, the
// snowball declaration) is unit-tested at the driving port; here the mutation-verified cores are the
// PURE selection functions (pickReusableProbe, unchartedNeighborsFrom), mirroring the package's
// bfsHops/growFrontierGraph "pure core, thin adapter" idiom. Everything is default-OFF: the relayer
// borrows off NO system until reuse_value_ceiling is armed, so a merge is byte-identical to today.

import (
	"context"
	"fmt"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ---- ProbeReuseRelayer -----------------------------------------------------

// reuseProbeFinder is the narrow ship-read the relayer needs: the whole fleet, so it can consider
// probes CURRENTLY MANNING low-value freshness posts (not just idle ones — the reconciler already
// relays idle probes; reuse's job is to cannibalize a low-value manning probe). Satisfied by
// navigation.ShipRepository.
type reuseProbeFinder interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// reuseHopGraph measures gate-hop distance between two systems (the reach anchor). Satisfied by
// *gategraph.Service.RepositionPath — the same expendable-class BFS the scout reconciler uses to
// pick the nearest relayable probe, which routes PAST unreadable frontier gates.
type reuseHopGraph interface {
	RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error)
}

// SystemValueReader answers a system's trade-value for the depth-vs-freshness ceiling — reuse borrows
// a probe only off a system whose value is BELOW the ceiling.
type SystemValueReader interface {
	SystemTradeValue(ctx context.Context, systemSymbol string, playerID int) (int, error)
}

// ProbeRelayDispatcher performs the actual cross-gate relay of the selected probe onto the target
// system (charting it on arrival). Kept as a narrow injected seam so the relayer's SELECTION logic
// stays pure/testable and composition wires the concrete reposition path (RepositionerRelayDispatcher).
type ProbeRelayDispatcher interface {
	RelayProbeToSystem(ctx context.Context, shipSymbol, targetSystem string, playerID, maxHops int) error
}

// ProbeReuseRelayer implements expansionCmd.ProbeReuseRelayer. It enumerates the fleet's scout probes,
// measures each one's gate-hops to the target and its current system's trade-value, picks the nearest
// probe within reach sitting in a below-ceiling system, and dispatches the relay. FAIL-SAFE by
// construction: no target / disabled ceiling / no reusable probe / unreadable value all return
// ok=false so the coordinator falls back to the unchanged buy path (never buy blind, never strand).
type ProbeReuseRelayer struct {
	shipRepo    reuseProbeFinder
	hopGraph    reuseHopGraph
	valueReader SystemValueReader
	dispatcher  ProbeRelayDispatcher
}

// NewProbeReuseRelayer wires the reuse-before-buy edge-probe relayer.
func NewProbeReuseRelayer(shipRepo reuseProbeFinder, hopGraph reuseHopGraph, valueReader SystemValueReader, dispatcher ProbeRelayDispatcher) *ProbeReuseRelayer {
	return &ProbeReuseRelayer{shipRepo: shipRepo, hopGraph: hopGraph, valueReader: valueReader, dispatcher: dispatcher}
}

var _ expansionCmd.ProbeReuseRelayer = (*ProbeReuseRelayer)(nil)

// reusableProbeCandidate is one existing probe considered for reuse: its symbol, the system it
// currently sits in, that system's trade-value, and the gate-hops from there to the target.
type reusableProbeCandidate struct {
	shipSymbol  string
	fromSystem  string
	systemValue int
	hops        int
}

// RelayNearestProbe relays the nearest reusable probe to target.System. ok=false (fall back to buy)
// whenever there is no target, the ceiling is disabled, or no probe qualifies within reach / under
// the ceiling; a dispatch failure is the only error surfaced (the coordinator logs it and does not
// buy blind on it, but is never stranded — it retries next cycle).
func (r *ProbeReuseRelayer) RelayNearestProbe(ctx context.Context, playerID shared.PlayerID, target expansionCmd.ProbeReuseTarget) (string, bool, error) {
	if target.System == "" || target.ValueCeiling <= 0 {
		return "", false, nil // no target, or ceiling disabled => borrow off no system (safe default)
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return "", false, fmt.Errorf("fleet unreadable for probe reuse: %w", err)
	}
	best, ok := pickReusableProbe(r.gatherCandidates(ctx, playerID, ships, target), target.MaxHops, target.ValueCeiling)
	if !ok {
		return "", false, nil // no reusable probe within reach / under the ceiling => the coordinator buys
	}
	if err := r.dispatcher.RelayProbeToSystem(ctx, best.shipSymbol, target.System, playerID.Value(), target.MaxHops); err != nil {
		return "", false, fmt.Errorf("relay of reused probe %s to %s failed: %w", best.shipSymbol, target.System, err)
	}
	return best.shipSymbol, true, nil
}

// gatherCandidates resolves the reach (gate-hops) and system trade-value of every scout probe, caching
// the value per system so a cluster of probes in one system costs one value read. A probe already AT
// the target, out of reach, or in a system with an unreadable value is dropped (never cannibalize blind).
func (r *ProbeReuseRelayer) gatherCandidates(ctx context.Context, playerID shared.PlayerID, ships []*navigation.Ship, target expansionCmd.ProbeReuseTarget) []reusableProbeCandidate {
	valueBySystem := make(map[string]int)
	valueReadable := make(map[string]bool)
	out := make([]reusableProbeCandidate, 0, len(ships))
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		loc := ship.CurrentLocation()
		if loc == nil || loc.SystemSymbol == "" || loc.SystemSymbol == target.System {
			continue // no location, or already in the target system (nothing to relay)
		}
		hops := r.hopsBetween(ctx, loc.SystemSymbol, target.System, target.MaxHops)
		if hops < 1 {
			continue // unreachable within the relay reach
		}
		value, ok := r.systemValue(ctx, loc.SystemSymbol, playerID.Value(), valueBySystem, valueReadable)
		if !ok {
			continue // unreadable value => never cannibalize blind
		}
		out = append(out, reusableProbeCandidate{shipSymbol: ship.ShipSymbol(), fromSystem: loc.SystemSymbol, systemValue: value, hops: hops})
	}
	return out
}

func (r *ProbeReuseRelayer) hopsBetween(ctx context.Context, fromSystem, toSystem string, maxJumps int) int {
	path, err := r.hopGraph.RepositionPath(ctx, fromSystem, toSystem, maxJumps)
	if err != nil || len(path) == 0 {
		return -1
	}
	return len(path) - 1
}

func (r *ProbeReuseRelayer) systemValue(ctx context.Context, systemSymbol string, playerID int, cache map[string]int, readable map[string]bool) (int, bool) {
	if v, seen := cache[systemSymbol]; seen {
		return v, readable[systemSymbol]
	}
	v, err := r.valueReader.SystemTradeValue(ctx, systemSymbol, playerID)
	cache[systemSymbol] = v
	readable[systemSymbol] = err == nil
	return v, err == nil
}

// pickReusableProbe selects the probe to relay: the FEWEST-hop candidate that is within maxHops AND
// sits in a system whose trade-value is strictly BELOW ceiling (the depth-vs-freshness guard — never
// strip a probe off a high-value core market). ceiling<=0 disqualifies EVERY candidate (borrow off no
// system — the safe default). Ties on hops break on the lowest ship symbol for determinism. It is pure
// over its inputs, so the value ceiling and reach guards are unit-testable with no store, API, or repo.
func pickReusableProbe(candidates []reusableProbeCandidate, maxHops, ceiling int) (reusableProbeCandidate, bool) {
	best := reusableProbeCandidate{}
	found := false
	for _, candidate := range candidates {
		if candidate.hops < 1 || candidate.hops > maxHops {
			continue // out of reach (or already at the target)
		}
		if ceiling <= 0 || candidate.systemValue >= ceiling {
			continue // never borrow off a system at/above the value ceiling (or ceiling disabled)
		}
		if !found || candidate.hops < best.hops || (candidate.hops == best.hops && candidate.shipSymbol < best.shipSymbol) {
			best, found = candidate, true
		}
	}
	return best, found
}

// ---- FrontierNeighborScanner -----------------------------------------------

// neighborAdjGraph is the persisted gate adjacency the snowball walk reads (satisfied by
// *gategraph.Service.Adjacency).
type neighborAdjGraph interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// neighborMarketReader answers whether a system has known markets — the second charted-predicate leg
// (a swept system has market_data even before its own gate is charted). Satisfied by the market repo.
type neighborMarketReader interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
}

// FrontierNeighborScanner implements expansionCmd.FrontierNeighborReader: the uncharted gate-neighbors
// of a charted system — the next ring the snowball walk enqueues. A still-virgin system has no
// adjacency entry and yields none, so the walk self-gates to genuinely-charted systems.
type FrontierNeighborScanner struct {
	gateGraph  neighborAdjGraph
	marketRepo neighborMarketReader
}

// NewFrontierNeighborScanner wires the snowball neighbor source.
func NewFrontierNeighborScanner(gateGraph neighborAdjGraph, marketRepo neighborMarketReader) *FrontierNeighborScanner {
	return &FrontierNeighborScanner{gateGraph: gateGraph, marketRepo: marketRepo}
}

var _ expansionCmd.FrontierNeighborReader = (*FrontierNeighborScanner)(nil)

// UnchartedNeighbors returns systemSymbol's gate-adjacent systems that are NOT yet charted. A neighbor
// is charted iff it carries its own persisted gate edges OR has known markets (mirrors the
// ExpansionScanner's charted predicate). An unreadable adjacency fails the read (the coordinator logs
// and skips the walk this cycle), never a wrong-but-silent empty.
func (s *FrontierNeighborScanner) UnchartedNeighbors(ctx context.Context, playerID int, systemSymbol string) ([]string, error) {
	adj, err := s.gateGraph.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("gate adjacency unreadable: %w", err)
	}
	charted := func(sys string) bool {
		if _, hasEdges := adj[sys]; hasEdges {
			return true
		}
		markets, err := s.marketRepo.FindAllMarketsInSystem(ctx, sys, playerID)
		return err == nil && len(markets) > 0
	}
	return unchartedNeighborsFrom(adj, systemSymbol, charted), nil
}

// unchartedNeighborsFrom returns systemSymbol's gate-adjacent systems that are NOT charted, pure over
// its inputs (mirrors bfsHops/growFrontierGraph). A system with no adjacency entry yields none (still
// virgin — no edges to walk); under-construction edges and already-charted neighbors are excluded; the
// result is deduped.
func unchartedNeighborsFrom(adj map[string][]system.GateEdge, systemSymbol string, charted func(string) bool) []string {
	edges, ok := adj[systemSymbol]
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, edge := range edges {
		if edge.UnderConstruction || edge.ConnectedSystem == "" {
			continue
		}
		neighbor := edge.ConnectedSystem
		if seen[neighbor] {
			continue
		}
		seen[neighbor] = true
		if charted(neighbor) {
			continue
		}
		out = append(out, neighbor)
	}
	return out
}

// ---- MarketSystemValueReader -----------------------------------------------

// valueMarketReader reads a system's markets and their good listings for the trade-value proxy.
type valueMarketReader interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// MarketSystemValueReader implements SystemValueReader as the summed trade depth of a system: over
// every market waypoint, over every listed good, sell-price × trade-volume. It is a PROXY for "how
// much this system is worth keeping fresh" — higher means a deeper, more valuable market the reuse
// ceiling protects from cannibalization; a sparse deep-edge system sums low and is borrowable. A
// missing/unreadable market contributes 0 rather than failing the whole read.
type MarketSystemValueReader struct {
	markets valueMarketReader
}

// NewMarketSystemValueReader wires the market-backed system trade-value proxy.
func NewMarketSystemValueReader(markets valueMarketReader) *MarketSystemValueReader {
	return &MarketSystemValueReader{markets: markets}
}

var _ SystemValueReader = (*MarketSystemValueReader)(nil)

// SystemTradeValue sums sell-price × trade-volume across every good in every market of the system.
func (r *MarketSystemValueReader) SystemTradeValue(ctx context.Context, systemSymbol string, playerID int) (int, error) {
	waypoints, err := r.markets.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("markets unreadable for %s: %w", systemSymbol, err)
	}
	total := 0
	for _, waypoint := range waypoints {
		data, err := r.markets.GetMarketData(ctx, waypoint, playerID)
		if err != nil || data == nil {
			continue
		}
		for _, good := range data.TradeGoods() {
			total += good.SellPrice() * good.TradeVolume()
		}
	}
	return total, nil
}

// ---- RepositionerRelayDispatcher -------------------------------------------

// reuseRepositioner is the cross-gate relay primitive (satisfied by the trade coordinator's
// *RunTradeRouteCoordinatorHandler.RepositionToWaypointWithinJumps — the same expendable-class relay
// the scout reconciler uses to man a post).
type reuseRepositioner interface {
	RepositionToWaypointWithinJumps(ctx context.Context, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error
}

// RepositionerRelayDispatcher performs the reuse relay over the proven reposition path. It resolves the
// target system's inbound jump-gate waypoint from the persisted adjacency (the edge charted neighbors
// carry into the target — how growFrontierGraph reached the virgin in the first place) and relays the
// probe onto it. If no inbound gate is known it fails closed, so the coordinator falls back to buying.
//
// ARMING NOTE: this dispatch is the one pre-arming validation point — the inbound-gate
// resolution and charting-on-arrival must be confirmed against the live gate semantics before
// probe_reuse_enabled is turned on next era. The unify follow-up (route virgin relays through the
// scout coordinator's proven virgin-relay) would retire this seam entirely.
type RepositionerRelayDispatcher struct {
	repositioner reuseRepositioner
	gates        neighborAdjGraph
}

// NewRepositionerRelayDispatcher wires the reuse relay over the reposition path + the gate adjacency.
func NewRepositionerRelayDispatcher(repositioner reuseRepositioner, gates neighborAdjGraph) *RepositionerRelayDispatcher {
	return &RepositionerRelayDispatcher{repositioner: repositioner, gates: gates}
}

var _ ProbeRelayDispatcher = (*RepositionerRelayDispatcher)(nil)

// RelayProbeToSystem routes shipSymbol onto targetSystem's inbound jump gate within maxHops.
func (d *RepositionerRelayDispatcher) RelayProbeToSystem(ctx context.Context, shipSymbol, targetSystem string, playerID, maxHops int) error {
	gate, ok := d.inboundGateWaypoint(ctx, targetSystem)
	if !ok {
		return fmt.Errorf("no known jump gate into %s to relay a reused probe onto", targetSystem)
	}
	return d.repositioner.RepositionToWaypointWithinJumps(ctx, shipSymbol, gate, playerID, maxHops)
}

// inboundGateWaypoint finds a jump-gate waypoint that leads to targetSystem, scanning the persisted
// adjacency for any edge whose ConnectedSystem is the target (the covered ring's charted gate reaches
// the virgin over exactly such an edge). Empty when the target is not yet reachable in the graph.
func (d *RepositionerRelayDispatcher) inboundGateWaypoint(ctx context.Context, targetSystem string) (string, bool) {
	adj, err := d.gates.Adjacency(ctx)
	if err != nil {
		return "", false
	}
	if edges, ok := adj[targetSystem]; ok {
		for _, edge := range edges {
			if edge.GateWaypoint != "" {
				return edge.GateWaypoint, true // target already charted its own gate
			}
		}
	}
	for _, edges := range adj {
		for _, edge := range edges {
			if edge.ConnectedSystem == targetSystem && edge.GateWaypoint != "" {
				return edge.GateWaypoint, true
			}
		}
	}
	return "", false
}
