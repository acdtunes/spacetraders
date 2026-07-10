// Package expansion holds the infrastructure adapters that back the frontier
// expansion coordinator's optional ports (sp-8w89): the live-treasury reader, the
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

// ProbePurchaser prices and buys one probe through the existing purchase_ship mediator
// path (RULINGS #3, the daemon is the single writer). It buys ONLY through an idle,
// UNDEDICATED ship already stationed AT a shipyard that sells SHIP_PROBE — so the
// purchase triggers no navigation (the buyer is present) and never disturbs a pinned or
// working hull (RULINGS #7). When no such in-place buyer exists it returns an error and
// the coordinator fails closed (no buy this cycle); the captain enables purchases by
// keeping a ship stationed at a probe yard (the HQ shipyard sells probes).
type ProbePurchaser struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewProbePurchaser wires the price-and-buy port.
func NewProbePurchaser(mediator common.Mediator, shipRepo navigation.ShipRepository) *ProbePurchaser {
	return &ProbePurchaser{mediator: mediator, shipRepo: shipRepo}
}

// QuoteProbe returns the cheapest live probe price and the yard, read from an idle
// in-place buyer's shipyard.
func (p *ProbePurchaser) QuoteProbe(ctx context.Context, playerID shared.PlayerID) (int, string, error) {
	_, yard, price, err := p.resolveBuyerAtProbeYard(ctx, playerID)
	if err != nil {
		return 0, "", err
	}
	return price, yard, nil
}

// BuyProbe purchases one probe at the in-place buyer's yard, refusing a fill whose live
// price exceeds maxBudget (the 25% treasury ceiling the coordinator computed). It
// verifies the yard delivered a probe and not a substituted hull (sp-e7je money
// integrity), then returns the price paid and the new hull's symbol.
func (p *ProbePurchaser) BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int) (int, string, error) {
	buyer, yard, price, err := p.resolveBuyerAtProbeYard(ctx, playerID)
	if err != nil {
		return 0, "", err
	}
	if price > maxBudget {
		return 0, "", fmt.Errorf("probe price %d exceeds budget %d", price, maxBudget)
	}

	resp, err := p.mediator.Send(ctx, &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: buyer,
		ShipType:             probeShipType,
		PlayerID:             playerID,
		ShipyardWaypoint:     yard, // the buyer is already here → no navigation
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

// resolveBuyerAtProbeYard scans idle, undedicated ships for one sitting at a shipyard
// that sells SHIP_PROBE, returning the cheapest such (buyer, yard, price). Movement-free
// and poach-free by construction.
func (p *ProbePurchaser) resolveBuyerAtProbeYard(ctx context.Context, playerID shared.PlayerID) (string, string, int, error) {
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return "", "", 0, err
	}
	bestBuyer, bestYard, bestPrice := "", "", 0
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
		if bestBuyer == "" || price < bestPrice {
			bestBuyer, bestYard, bestPrice = ship.ShipSymbol(), loc.Symbol, price
		}
	}
	if bestBuyer == "" {
		return "", "", 0, fmt.Errorf("no idle undedicated ship is stationed at a probe-selling shipyard")
	}
	return bestBuyer, bestYard, bestPrice, nil
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

// AdjacencyProvider is the slice of gategraph.Service the scanner BFS-walks: the whole
// persisted cross-system gate graph in one era-scoped read.
type AdjacencyProvider interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// ExpansionScanner enumerates the gate-reachable frontier for the coordinator's queue.
// It runs one multi-source BFS over the persisted gate adjacency from the anchor set
// (HQ + the systems the fleet currently occupies), bounded by maxHops, and annotates
// each reached system with its known-market count and a charted flag. Charted-only
// reachability: virgin systems surface as edge targets of a charted neighbor (a probe
// relayed there charts it on arrival, nn0y).
type ExpansionScanner struct {
	adjacency  AdjacencyProvider
	marketRepo market.MarketRepository
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
}

// NewExpansionScanner wires the frontier enumerator.
func NewExpansionScanner(
	adjacency AdjacencyProvider,
	marketRepo market.MarketRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
) *ExpansionScanner {
	return &ExpansionScanner{
		adjacency:  adjacency,
		marketRepo: marketRepo,
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
	}
}

// ExpansionCandidates returns every gate-reachable system within maxHops of the anchor
// set, annotated with hop distance, known-market count, and a charted flag.
func (s *ExpansionScanner) ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]expansionCmd.ExpansionCandidate, error) {
	adj, err := s.adjacency.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("gate adjacency unreadable: %w", err)
	}

	anchors := s.anchorSystems(ctx, playerID)
	if len(anchors) == 0 {
		return nil, nil // no anchor to measure from → no queue this cycle
	}

	hops := bfsHops(adj, anchors, maxHops)

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

// bfsHops runs a multi-source BFS over the gate adjacency from the anchor set, returning
// each reachable system's minimum hop distance (<= maxHops). Under-construction edges are
// skipped (a multi-jump route can never traverse them, sp-7gr2) — Adjacency does not
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
