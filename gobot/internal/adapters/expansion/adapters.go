// Package expansion holds the infrastructure adapters that back the frontier
// expansion coordinator's optional ports: the live-treasury reader, the
// probe price-and-buy port over the existing purchase_ship machinery, and the
// gate-graph/market-data expansion scanner. The coordinator's decision logic lives in
// internal/application/expansion/commands; these adapters are the seams the daemon
// wires it to at composition time (main.go).
package expansion

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// probeShipType is the SpaceTraders purchase type for a scout/satellite hull, matching
// the coordinator's own constant.
const probeShipType = "SHIP_PROBE"

// ---- TreasuryReader --------------------------------------------------------

// TreasuryReader reads the player's live treasury from the API for the 25% money guard.
// It resolves the player token from ctx (the daemon container runner injects it), so a
// missing token or an API error surfaces as an error the coordinator fails closed on.
type TreasuryReader struct {
	apiClient domainPorts.APIClient
}

// NewTreasuryReader wires the live-treasury reader.
func NewTreasuryReader(apiClient domainPorts.APIClient) *TreasuryReader {
	return &TreasuryReader{apiClient: apiClient}
}

// LiveCredits returns the player's current treasury balance.
func (r *TreasuryReader) LiveCredits(ctx context.Context, playerID shared.PlayerID) (int, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unresolved: %w", err)
	}
	agent, err := r.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, err
	}
	return agent.Credits, nil
}

// ---- ProbePurchaser --------------------------------------------------------

// probeYardFinder is the narrow slice of the ReachableYardFinder the demand-proximal
// selection needs: the scout-scanned probe-selling yards near a target system, ranked
// hops-then-price off the persisted gate graph (a pure store read — no API). Satisfied by
// *shipyardQueries.ReachableYardFinder. Nil-safe: a nil finder disables target-aware selection,
// leaving every buy on the home-yard in-place path.
type probeYardFinder interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

// ProbePurchaser prices and buys one probe through the existing purchase_ship mediator
// path (RULINGS #3, the daemon is the single writer). It is DEMAND-PROXIMAL: given a
// target system it spawns the probe at the scanned probe-yard NEAREST that system (fewest gate
// hops, arbitrated against price by the caller's tunable), so the reconciler's relay is short
// instead of always buying at the home yard and relaying across the network. The purchase runs
// through an idle, UNDEDICATED hull (never a pinned or working one — RULINGS #7); a target-yard
// buy navigates that hull to the yard (the demand-proximal pattern), a home-yard buy uses
// an in-place hull movement-free. FAIL-OPEN by construction: no target, no finder, an empty/sparse
// scan store, or an unreadable rank all fall back to the home yard exactly as before — missing
// shipyard data never fails a buy closed.
type ProbePurchaser struct {
	mediator   common.Mediator
	shipRepo   navigation.ShipRepository
	yardFinder probeYardFinder
}

// NewProbePurchaser wires the price-and-buy port. yardFinder is optional (nil disables
// target-aware selection — every buy stays on the home-yard in-place path).
func NewProbePurchaser(mediator common.Mediator, shipRepo navigation.ShipRepository, yardFinder probeYardFinder) *ProbePurchaser {
	return &ProbePurchaser{mediator: mediator, shipRepo: shipRepo, yardFinder: yardFinder}
}

// probeBuyPlan is one resolved buy: the yard to buy at, the price the guards judge, and the
// in-place buyer already there. An empty buyer marks a target-yard plan, whose hull is resolved
// lazily at purchase time — any idle undedicated hull navigates to the yard.
type probeBuyPlan struct {
	buyer string
	yard  string
	price int
}

// QuoteProbe returns the demand-proximal probe price and yard: the scanned probe-yard nearest
// target, or — with no target / no finder / no scanned yard — the cheapest live in-place
// yard exactly as before. The price feeds the caller's unchanged money guards.
func (p *ProbePurchaser) QuoteProbe(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (int, string, error) {
	plan, err := p.resolveProbeBuy(ctx, playerID, target)
	if err != nil {
		return 0, "", err
	}
	return plan.price, plan.yard, nil
}

// BuyProbe purchases one probe at the demand-proximal (or fallback home) yard, refusing a fill
// whose price exceeds maxBudget (the 25% treasury ceiling the coordinator computed). A target-yard
// plan RELAYS an idle undedicated hull to the winning yard (never a movement-free buy
// where a hull happens to sit) and RE-CHECKS the live dock price
// there so the guard judges the ACTUAL price and not a stale scan; a home-yard fallback plan buys
// through the in-place hull movement-free at its already-live price. It verifies the yard delivered
// a probe and not a substituted hull (money integrity), then returns the price paid and the
// new hull's symbol.
func (p *ProbePurchaser) BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int, target probebuy.ProbeTarget) (int, string, error) {
	plan, err := p.resolveProbeBuy(ctx, playerID, target)
	if err != nil {
		return 0, "", err
	}

	buyer := plan.buyer
	price := plan.price
	if buyer == "" { // target-yard plan: relay an idle hull to the yard, then re-price at the dock
		buyer, price, err = p.prepareTargetYardBuyer(ctx, playerID, plan.yard)
		if err != nil {
			return 0, "", err
		}
	}

	// Guard on the ACTUAL price the buy will pay: the live dock re-check for a relayed
	// target-yard buy, or the already-live in-place price for a home-yard fallback — never a stale
	// scan that lets a depleted yard slip past the 25% treasury ceiling.
	if price > maxBudget {
		return 0, "", fmt.Errorf("probe price %d exceeds budget %d", price, maxBudget)
	}

	resp, err := p.mediator.Send(ctx, &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: buyer,
		ShipType:             probeShipType,
		PlayerID:             playerID,
		ShipyardWaypoint:     plan.yard, // buyer is now AT the yard (relayed or in-place) → movement-free
	})
	if err != nil {
		return 0, "", err
	}
	r, ok := resp.(*shipyardCmd.PurchaseShipResponse)
	if !ok || r.Ship == nil {
		return 0, "", fmt.Errorf("unexpected purchase response")
	}
	if r.ShipType != probeShipType {
		return 0, "", fmt.Errorf("yard delivered %q, not %q", r.ShipType, probeShipType)
	}
	return r.PurchasePrice, r.Ship.ShipSymbol(), nil
}

// resolveProbeBuy picks the buy yard: the demand-proximal scanned yard nearest the target when one
// is known, else the home-yard in-place buyer. The proximal branch FAILS OPEN — any gap (no
// target, no finder, empty/unreadable scan store) returns ok=false and the in-place fallback runs,
// so missing shipyard data can never fail a buy closed.
func (p *ProbePurchaser) resolveProbeBuy(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (probeBuyPlan, error) {
	if plan, ok := p.resolveProximalBuy(ctx, playerID, target); ok {
		return plan, nil
	}
	return p.resolveInPlaceBuy(ctx, playerID)
}

// resolveProximalBuy selects the scanned probe-yard nearest the target, arbitrating hops against
// price by the target's tunable penalty. ok=false signals "no proximal yard — fall back to home"
// for every fail-open case (no target, no finder, empty/unreadable scan store), so missing
// shipyard data never fails a buy closed. The plan carries no buyer: a target-yard buy
// resolves the navigating hull lazily at purchase time.
func (p *ProbePurchaser) resolveProximalBuy(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (probeBuyPlan, bool) {
	if target.System == "" || p.yardFinder == nil {
		return probeBuyPlan{}, false
	}
	candidates, err := p.yardFinder.NearestYardsSelling(ctx, playerID.Value(), []string{probeShipType}, []string{target.System})
	if err != nil || len(candidates) == 0 {
		return probeBuyPlan{}, false // fail OPEN — a sparse/unreadable scan store falls back to home
	}
	best := pickBuyYard(candidates, target.HopPenaltyCredits, target.SiblingPriceMarginCredits)
	return probeBuyPlan{yard: best.WaypointSymbol, price: best.PurchasePrice}, true
}

// pickBuyYard selects the buy yard across ALL reachable candidates: the demand-proximal
// hop-penalty winner, UNLESS a cheaper reachable sibling undercuts it by more than
// siblingMargin — the supply-depletion override. Repeated buys raise a yard's scanned
// price (LIMITED→SCARCE); the override abandons a yard the moment a sibling beats it by more than
// the margin, so the buy spreads instead of spiraling one market to 4x. A
// siblingMargin<=0 disables the override, degenerating to pure hop-penalty selection (rollback).
func pickBuyYard(candidates []shipyardQueries.YardCandidate, hopPenalty, siblingMargin int) shipyardQueries.YardCandidate {
	proximal := pickProximalYard(candidates, hopPenalty)
	if siblingMargin <= 0 {
		return proximal
	}
	cheapest := cheapestYard(candidates)
	if proximal.PurchasePrice-cheapest.PurchasePrice > siblingMargin {
		return cheapest // depleted near yard — spread the buy to the cheapest sibling
	}
	return proximal
}

// pickProximalYard chooses the scanned probe-yard minimizing effectiveCost = PurchasePrice +
// Hops*hopPenalty — the demand-proximal tradeoff. A high penalty makes proximity
// dominate (buy NEAREST the post); a zero penalty degenerates to the cheapest reachable yard. The
// finder pre-sorts candidates hops-then-price, so a strict-less comparison keeps the FIRST minimum
// — the nearest, then cheapest — on a tie.
func pickProximalYard(candidates []shipyardQueries.YardCandidate, hopPenalty int) shipyardQueries.YardCandidate {
	best := candidates[0]
	bestCost := best.PurchasePrice + best.Hops*hopPenalty
	for _, candidate := range candidates[1:] {
		cost := candidate.PurchasePrice + candidate.Hops*hopPenalty
		if cost < bestCost {
			best, bestCost = candidate, cost
		}
	}
	return best
}

// cheapestYard returns the candidate with the lowest scanned PurchasePrice — the sibling the
// supply-depletion override spreads a buy to when the proximity winner has been priced up. Ties
// keep the first (the finder pre-sorts hops-then-price, so the nearest cheapest wins).
func cheapestYard(candidates []shipyardQueries.YardCandidate) shipyardQueries.YardCandidate {
	cheapest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.PurchasePrice < cheapest.PurchasePrice {
			cheapest = candidate
		}
	}
	return cheapest
}

// resolveInPlaceBuy scans idle, undedicated ships for one sitting at a shipyard that sells
// SHIP_PROBE, returning the cheapest such plan (buyer present, live price). Movement-free and
// poach-free by construction.
func (p *ProbePurchaser) resolveInPlaceBuy(ctx context.Context, playerID shared.PlayerID) (probeBuyPlan, error) {
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return probeBuyPlan{}, err
	}
	best := probeBuyPlan{}
	for _, ship := range ships {
		if ship.DedicatedFleet() != "" {
			continue // never disturb a dedicated hull (RULINGS #7)
		}
		loc := ship.CurrentLocation()
		if loc == nil {
			continue
		}
		price, ok := p.probePriceAt(ctx, playerID, loc.SystemSymbol, loc.Symbol)
		if !ok {
			continue
		}
		if best.buyer == "" || price < best.price {
			best = probeBuyPlan{buyer: ship.ShipSymbol(), yard: loc.Symbol, price: price}
		}
	}
	if best.buyer == "" {
		return probeBuyPlan{}, fmt.Errorf("no idle undedicated ship is stationed at a probe-selling shipyard")
	}
	return best, nil
}

// prepareTargetYardBuyer readies a target-yard buy: it resolves an idle undedicated
// hull, RELAYS it to the winning yard when it is not already there (never a movement-free buy
// wherever a hull sits), and RE-CHECKS the live dock price so the money guard
// judges the ACTUAL price rather than a stale scan. Returns the buyer symbol and the re-checked
// live price. A relay that cannot route, or a dock whose live price is unreadable after arrival,
// fails the buy CLOSED — the fail-OPEN home fallback covers only MISSING selection data, not a
// committed relay that then cannot be priced (never overpay blind).
func (p *ProbePurchaser) prepareTargetYardBuyer(ctx context.Context, playerID shared.PlayerID, yard string) (string, int, error) {
	buyer, err := p.resolveIdleUndedicatedBuyer(ctx, playerID, yard)
	if err != nil {
		return "", 0, err
	}
	if !buyerAtYard(buyer, yard) {
		if err := p.navigateBuyerToYard(ctx, playerID, buyer.ShipSymbol(), yard); err != nil {
			return "", 0, err
		}
	}
	price, ok := p.probePriceAt(ctx, playerID, shared.ExtractSystemSymbol(yard), yard)
	if !ok {
		return "", 0, fmt.Errorf("live dock price at %s unreadable after relay", yard)
	}
	return buyer.ShipSymbol(), price, nil
}

// navigateBuyerToYard relays the buyer to the winning yard via the shared high-level navigation
// command, which routes CROSS-SYSTEM through the gate-crosser wired on the NavigateRoute
// handler — a frontier yard is almost always in another system. A relay error surfaces so the buy
// fails closed rather than buying at the wrong (current) location.
func (p *ProbePurchaser) navigateBuyerToYard(ctx context.Context, playerID shared.PlayerID, buyerSymbol, yard string) error {
	if _, err := p.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
		ShipSymbol:  buyerSymbol,
		Destination: yard,
		PlayerID:    playerID,
	}); err != nil {
		return fmt.Errorf("failed to relay probe buyer to %s: %w", yard, err)
	}
	return nil
}

// buyerAtYard reports whether the resolved hull already sits at the winning yard, so a relay is
// skipped (movement-free) when a hull happens to be present there already.
func buyerAtYard(buyer *navigation.Ship, yard string) bool {
	loc := buyer.CurrentLocation()
	return loc != nil && loc.Symbol == yard
}

// resolveIdleUndedicatedBuyer returns an idle, undedicated hull to execute a target-yard buy,
// PREFERRING one already at preferYard (movement-free) so no relay is needed when a hull is already
// present. It never selects a dedicated hull (RULINGS #7). No idle hull → error (the buy fails
// closed exactly as the in-place path does when no buyer exists — never a data-driven fail-close,
// just no hull to buy through).
func (p *ProbePurchaser) resolveIdleUndedicatedBuyer(ctx context.Context, playerID shared.PlayerID, preferYard string) (*navigation.Ship, error) {
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return nil, err
	}
	var fallback *navigation.Ship
	for _, ship := range ships {
		if ship.DedicatedFleet() != "" {
			continue
		}
		loc := ship.CurrentLocation()
		if loc != nil && loc.Symbol == preferYard {
			return ship, nil // already at the yard → movement-free
		}
		if fallback == nil {
			fallback = ship
		}
	}
	if fallback == nil {
		return nil, fmt.Errorf("no idle undedicated ship available to buy the probe")
	}
	return fallback, nil
}

// probePriceAt reads the live SHIP_PROBE purchase price at a waypoint's shipyard, or
// reports ok=false if the waypoint is not a probe-selling shipyard (or has no priced
// listing — which happens unless a ship is present, exactly the in-place buyers we scan).
func (p *ProbePurchaser) probePriceAt(ctx context.Context, playerID shared.PlayerID, systemSymbol, waypointSymbol string) (int, bool) {
	resp, err := p.mediator.Send(ctx, &shipyardQueries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerID:       playerID,
	})
	if err != nil {
		return 0, false
	}
	r, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok {
		return 0, false
	}
	listing, found := r.Shipyard.FindListingByType(probeShipType)
	if !found || listing.PurchasePrice <= 0 {
		return 0, false
	}
	return listing.PurchasePrice, true
}

// ---- ExpansionScanner ------------------------------------------------------

// GateGraph is the slice of gategraph.Service the scanner uses. Adjacency is the whole
// persisted cross-system gate graph in one era-scoped read (what the BFS walks). Connections
// is the per-system fetch-through read that CHARTS a covered frontier system's own jump gate
// on a miss (a single live GetJumpGate that persists the edge set) — the seam that grows the
// walkable graph one ring at a time so expansion does not freeze at the last charted ring.
// The real *gategraph.Service satisfies both.
type GateGraph interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
	Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error)
}

// WaypointCatalog is the narrow slice of system.WaypointRepository the scanner reads to decide
// whether a system's full waypoint set was SWEPT. ListBySystem returns the persisted
// waypoint rows for a system — non-empty (with real bodies) only after a sweep persisted them
// via BuildSystemGraph. Narrowing to the one method keeps the port intent-revealing and the
// scanned-discriminator trivially fakeable.
type WaypointCatalog interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// ExpansionScanner enumerates the gate-reachable frontier for the coordinator's queue.
// It runs one multi-source BFS over the persisted gate adjacency from the anchor set
// (HQ + the systems the fleet currently occupies), bounded by maxHops, and annotates
// each reached system with its known-market count, a charted flag, and a scanned flag
// (whether its full waypoint set was swept). Charted-only reachability: virgin
// systems surface as edge targets of a charted neighbor (a probe relayed there charts it
// on arrival).
type ExpansionScanner struct {
	gateGraph    GateGraph
	marketRepo   market.MarketRepository
	shipRepo     navigation.ShipRepository
	playerRepo   player.PlayerRepository
	waypointRepo WaypointCatalog
}

// NewExpansionScanner wires the frontier enumerator.
func NewExpansionScanner(
	gateGraph GateGraph,
	marketRepo market.MarketRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	waypointRepo WaypointCatalog,
) *ExpansionScanner {
	return &ExpansionScanner{
		gateGraph:    gateGraph,
		marketRepo:   marketRepo,
		shipRepo:     shipRepo,
		playerRepo:   playerRepo,
		waypointRepo: waypointRepo,
	}
}

// ExpansionCandidates returns every gate-reachable system within maxHops of the anchor
// set, annotated with hop distance, known-market count, and a charted flag.
func (s *ExpansionScanner) ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]expansionCmd.ExpansionCandidate, error) {
	adj, err := s.gateGraph.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("gate adjacency unreadable: %w", err)
	}

	anchors := s.anchorSystems(ctx, playerID)
	if len(anchors) == 0 {
		return nil, nil // no anchor to measure from → no queue this cycle
	}

	// Grow the walkable graph one ring BEFORE the BFS enumerates candidates. The
	// sweep charts a scouted frontier system's markets but not its jump gate, so its onward
	// edges never reach the persisted adjacency and the BFS below would dead-end at the last
	// charted ring — the frozen frontier / empty queue. Charting the covered ring's gate here
	// (fetch-through+persist, at most once per system) lets the BFS reach the next ring.
	s.growReachableFrontierGates(ctx, playerID, adj, anchors, maxHops)

	hops := bfsHops(adj, anchors, maxHops)
	// Corridor (branch) identity per reachable system — the depth slice's bearing.
	branchRoots := bfsBranchRoots(adj, anchors, maxHops)

	candidates := make([]expansionCmd.ExpansionCandidate, 0, len(hops))
	for sys, h := range hops {
		marketCount := s.knownMarketCount(ctx, playerID, sys)
		_, hasEdges := adj[sys]
		charted := hasEdges || marketCount > 0
		candidates = append(candidates, expansionCmd.ExpansionCandidate{
			SystemSymbol: sys,
			Hops:         h,
			KnownMarkets: marketCount,
			Charted:      charted,
			Scanned:      s.systemScanned(ctx, sys),
			BranchRoot:   branchRoots[sys],
		})
	}
	return candidates, nil
}

// anchorSystems is the set of systems hop distance is measured FROM: the agent's HQ
// system plus every system the fleet currently occupies (where we already operate).
func (s *ExpansionScanner) anchorSystems(ctx context.Context, playerID int) map[string]bool {
	anchors := make(map[string]bool)

	if p, err := s.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID)); err == nil && p != nil {
		if hq, ok := p.Metadata["headquarters"].(string); ok && hq != "" {
			anchors[shared.ExtractSystemSymbol(hq)] = true
		}
	}

	if ships, err := s.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID)); err == nil {
		for _, ship := range ships {
			if loc := ship.CurrentLocation(); loc != nil && loc.SystemSymbol != "" {
				anchors[loc.SystemSymbol] = true
			}
		}
	}
	return anchors
}

// knownMarketCount is the number of known marketplace waypoints in a system — the queue's
// "known markets" ranking signal. A virgin (uncharted) system returns 0.
func (s *ExpansionScanner) knownMarketCount(ctx context.Context, playerID int, systemSymbol string) int {
	markets, err := s.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return 0
	}
	return len(markets)
}

// systemScanned reports whether a system's FULL waypoint set has been SWEPT — the signal
// distinct from KnownMarkets (market_data rows exist only for systems that HAVE a market, so it
// cannot tell a swept-but-barren system from a never-scanned one) and from Charted (a gate edge
// means reachable, not swept). It reads the persisted waypoint catalog and asks hasNonGateWaypoint
// whether a real body was recorded (a sweep persists the whole set via BuildSystemGraph; gate
// charting persists edges, not waypoints). An unreadable catalog fails SAFE to not-scanned: the
// system stays a scout target rather than being wrongly dropped as barren.
func (s *ExpansionScanner) systemScanned(ctx context.Context, systemSymbol string) bool {
	waypoints, err := s.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return false
	}
	return hasNonGateWaypoint(waypoints)
}

// growReachableFrontierGates charts+persists the jump-gate edges of covered frontier systems
// so the reachability graph grows one ring per cycle. It supplies growFrontierGraph
// with the two live collaborators: a charted-predicate (a system a probe has SWEPT has known
// markets — only then is its gate chartable; a virgin system's live gate 400s "no ship
// present") and the fetch-through Connections call (a miss triggers one live GetJumpGate that
// PERSISTS the edge set, so the fetch is amortized to at most once per system).
func (s *ExpansionScanner) growReachableFrontierGates(ctx context.Context, playerID int, adj map[string][]system.GateEdge, anchors map[string]bool, maxHops int) {
	growFrontierGraph(adj, anchors, maxHops,
		func(systemSymbol string) bool { return s.knownMarketCount(ctx, playerID, systemSymbol) > 0 },
		func(systemSymbol string) ([]system.GateEdge, error) {
			return s.gateGraph.Connections(ctx, systemSymbol, playerID)
		},
	)
}

// hasNonGateWaypoint reports whether a system's persisted waypoint rows prove its FULL set was
// actually SWEPT, as opposed to merely gate-charted. It is true iff at least one
// persisted waypoint is NOT a jump gate. The ONLY writer of waypoint rows is BuildSystemGraph
// (graph_builder.go), which persists a system's ENTIRE paginated waypoint list — planets, moons,
// asteroids, the gate — the moment a probe sweeps it; gate-charting persists jump-gate EDGES to
// the gate_edges table (gategraph Service.fetchAndStore → store.Replace), never a waypoints row.
// So a persisted non-gate waypoint is proof of a real sweep, whereas a never-swept system has no
// such row. Requiring a NON-gate row (not merely "≥1 row") is deliberate: even if a lone
// jump-gate row were ever persisted, a gate-only-charted system must still read as NOT scanned so
// it stays a scout target. It is pure over its input (a resolved waypoint list), so the
// discriminator is unit-testable with no store, mirroring bfsHops/growFrontierGraph.
func hasNonGateWaypoint(waypoints []*shared.Waypoint) bool {
	for _, waypoint := range waypoints {
		if !waypoint.IsJumpGate() {
			return true
		}
	}
	return false
}

// bfsHops runs a multi-source BFS over the gate adjacency from the anchor set, returning
// each reachable system's minimum hop distance (<= maxHops). Under-construction edges are
// skipped (a multi-jump route can never traverse them) — Adjacency does not
// filter them, so the BFS must. Anchors are included at hop 0.
func bfsHops(adj map[string][]system.GateEdge, anchors map[string]bool, maxHops int) map[string]int {
	hops := make(map[string]int)
	type node struct {
		system string
		depth  int
	}
	queue := make([]node, 0, len(anchors))
	for a := range anchors {
		hops[a] = 0
		queue = append(queue, node{system: a, depth: 0})
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxHops {
			continue
		}
		for _, edge := range adj[cur.system] {
			if edge.UnderConstruction {
				continue
			}
			next := edge.ConnectedSystem
			if next == "" {
				continue
			}
			nd := cur.depth + 1
			if existing, seen := hops[next]; seen && existing <= nd {
				continue
			}
			hops[next] = nd
			queue = append(queue, node{system: next, depth: nd})
		}
	}
	return hops
}

// growFrontierGraph grows the walkable gate graph by ONE ring, mutating adj in place.
// The frozen-frontier bug: a scouted ring's MARKETS are charted by the sweep but
// its JUMP GATE is not, so its onward edges never enter the persisted adjacency and the
// multi-source BFS dead-ends at the last charted ring — the expansion queue empties. For each
// system reachable within the hop bound that is CHARTED (a probe reached it) but whose OWN
// gate edges are not yet persisted, it fetches them once via fetchConnections (fetch-through:
// a miss persists the edge set) and merges them into adj, so the following bfsHops reaches the
// next ring's virgins.
//
// It is pure over its inputs — the scanner supplies the real charted-predicate (known-market
// count) and fetch (gategraph Connections) — so the ring-growth is unit-testable with no
// store, API, or repo, mirroring bfsHops.
//
// API-frugality is structural, so a wide frontier is never stormed:
//   - a system already carrying edges is never fetched (served from the map, zero API);
//   - an UNCHARTED system (charted==false — no probe has arrived) is never probed, because its
//     live gate would 400 "no ship present" and trip the negative-result backoff;
//   - a system AT the hop bound is never charted onward (its neighbors fall beyond maxHops and
//     the bounded BFS would discard them).
//
// Each qualifying system is therefore fetched at most ONCE ever: the next cycle it carries
// persisted edges and is skipped. A fetch error (an unreadable / backed-off gate) leaves the
// node ungrown — the BFS already treats it as a dead-end — so it is swallowed here.
func growFrontierGraph(
	adj map[string][]system.GateEdge,
	anchors map[string]bool,
	maxHops int,
	charted func(systemSymbol string) bool,
	fetchConnections func(systemSymbol string) ([]system.GateEdge, error),
) {
	reachable := bfsHops(adj, anchors, maxHops)
	for systemSymbol, hop := range reachable {
		if hop >= maxHops {
			continue // its onward neighbors would fall beyond the bound — not worth a fetch
		}
		if _, hasEdges := adj[systemSymbol]; hasEdges {
			continue // gate already persisted — never re-fetch (frugality)
		}
		if !charted(systemSymbol) {
			continue // uncharted virgin — its live gate would 400/back off; leave it for a probe
		}
		edges, err := fetchConnections(systemSymbol)
		if err != nil || len(edges) == 0 {
			continue // unreadable/backed-off — the graph just doesn't grow through it
		}
		adj[systemSymbol] = edges
	}
}
