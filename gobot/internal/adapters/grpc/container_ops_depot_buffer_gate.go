package grpc

import (
	"context"
	"math"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/buffer"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// This file is the LIVE-path half of the sp-rxrg buffer-candidate gating: the filter stage the
// destination-depot warehouse-cap selector (depotWarehouseTargetUnits) runs IN FRONT of the reward
// knapsack (PlanReceiptCaps), so a good that is not contracted TO the hub, that the hub produces
// locally, or that is sourced too near is never even a candidate. It shares the ONE domain gate
// (domain/buffer) with the reconciler planner so the three gates can never drift between the paths;
// only the DATA WIRING (contract history, market trade-type, coordinates) is per-path and lives here.

// bufferGateContext carries the resolved per-hub inputs the three sp-rxrg gates decide on plus the
// tunable gate. Its ZERO VALUE is the fail-open context (nil maps, zero distance floor): gate 1 and
// gate 2 admit everything and gate 3 excludes only a co-located (distance <= 0) source — the
// regression-safe default a caller with no resolved hub data (a degraded launch, a unit test that is
// not exercising the gates) passes.
type bufferGateContext struct {
	gate buffer.Gate
	// hubContractGoods maps a good to how many DISTINCT contracts deliver it TO the hub waypoint
	// (gate 1). A nil OR EMPTY map means membership is UNKNOWN/THIN — a mining error or a hub with no
	// resolvable contract history — and gate 1 fails OPEN so a transient data gap never EMPTIES a
	// warehouse (deploy-safety). A NON-EMPTY map is authoritative: a good absent from it is not a hub
	// contract good (the DRUGS@J58 case — J58's membership is rich, so DRUGS is genuinely excluded).
	hubContractGoods map[string]int
	// hubLocalProduction is the set of goods the hub's OWN market EXPORTS/EXCHANGES (gate 2). Nil/absent
	// leaves gate 2 fail-open per good.
	hubLocalProduction map[string]bool
}

// hubContractFrequency is the gate-1 input for one good: how many contracts deliver it to the hub.
// An empty OR nil membership map is a data gap (mining error, or a hub with no resolvable contract
// history) and fails OPEN — a positive frequency so no good is excluded on missing data and a thin
// history never empties a warehouse. A NON-EMPTY map is authoritative.
func (c bufferGateContext) hubContractFrequency(good string) float64 {
	if len(c.hubContractGoods) == 0 {
		return 1
	}
	return float64(c.hubContractGoods[normalizeGood(good)])
}

// producesLocally is the gate-2 input for one good.
func (c bufferGateContext) producesLocally(good string) bool {
	return c.hubLocalProduction[normalizeGood(good)]
}

// applyBufferGates filters mined demand candidates through the three sp-rxrg gates BEFORE the reward
// knapsack, keyed on the hub. destinationSystem is the hub's own system (the SAME anchor
// PlanReceiptCaps uses for its cross-system residual, so the two agree on what "external" means) and
// hubWaypoint + coords turn an in-system source into a real dist(hub, source). A candidate that
// clears all three gates survives; the rest are dropped so they never reach the ranking.
func applyBufferGates(
	candidates []persistence.DemandCandidate,
	destinationSystem string,
	hubWaypoint string,
	gateCtx bufferGateContext,
	coords tradingsvc.WaypointCoordsLookup,
) []persistence.DemandCandidate {
	var hubX, hubY float64
	hubKnown := false
	if coords != nil && hubWaypoint != "" {
		hubX, hubY, hubKnown = coords(hubWaypoint)
	}
	kept := make([]persistence.DemandCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		distance, distanceKnown := externalSourceDistance(candidate, destinationSystem, hubX, hubY, hubKnown, coords)
		if gateCtx.gate.Admits(buffer.Facts{
			Good:                        candidate.Good,
			HubContractFrequency:        gateCtx.hubContractFrequency(candidate.Good),
			HubProducesLocally:          gateCtx.producesLocally(candidate.Good),
			ExternalSourceDistance:      distance,
			ExternalSourceDistanceKnown: distanceKnown,
		}) {
			kept = append(kept, candidate)
		}
	}
	return kept
}

// externalSourceDistance resolves the gate-3 distance for one candidate: a CROSS-system source is
// unboundedly far (always clears the floor — the single-system worker can never reach it, so the
// buffer is most valuable there); an IN-system source with resolvable coordinates gets the real
// dist(hub, source); an unresolvable in-system source is UNKNOWN, which gate 3 treats as fail-open so
// a far good with uncached coordinates is never wrongly excluded. It mirrors receiptResidualLeg's
// cross-vs-in-system split so the gate and the ranking never disagree on a source's reach.
func externalSourceDistance(
	c persistence.DemandCandidate,
	destinationSystem string,
	hubX, hubY float64,
	hubKnown bool,
	coords tradingsvc.WaypointCoordsLookup,
) (float64, bool) {
	if c.ForeignSystem != "" && c.ForeignSystem != destinationSystem {
		return math.Inf(1), true // cross-system: unboundedly far
	}
	if !hubKnown || coords == nil || c.ForeignMarket == "" {
		return 0, false // in-system but coordinates unavailable: unknown -> gate 3 fails open
	}
	sourceX, sourceY, ok := coords(c.ForeignMarket)
	if !ok {
		return 0, false // uncached source waypoint: unknown -> gate 3 fails open
	}
	return euclidDistance(hubX, hubY, sourceX, sourceY), true
}

// euclidDistance is the in-system Euclidean distance between two positions (mirrors the trading
// service's own euclidDist; kept local so this adapter does not reach across the boundary for it).
func euclidDistance(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

// localProductionGoods is the gate-2 signal: the set of goods a hub's OWN market EXPORTS or
// EXCHANGES — the goods a delivery hull buys on-site, so warehousing them wastes the slot. An IMPORT
// good is NOT local production (the hub consumes it, does not make it) and is not in the set. A nil
// market (unscanned hub) yields nil — gate 2 then fails open (no local exclusion).
func localProductionGoods(m *market.Market) map[string]bool {
	if m == nil {
		return nil
	}
	produced := map[string]bool{}
	for _, good := range m.TradeGoods() {
		if good.TradeType() == market.TradeTypeExport || good.TradeType() == market.TradeTypeExchange {
			produced[normalizeGood(good.Symbol())] = true
		}
	}
	return produced
}

// normalizeGood upper-cases and trims a trade symbol so gate lookups never miss on incidental casing
// or whitespace drift between the contract-history, market, and demand-mining sources.
func normalizeGood(good string) string {
	return strings.ToUpper(strings.TrimSpace(good))
}

// depotBufferGateContext resolves the LIVE sp-rxrg gate inputs for a hub warehouse (re)solve:
//   - gate 1: the goods CONTRACTED to this hub waypoint (ContractGoodCountsForDeliveryWaypoint,
//     current-era-scoped like the demand mine so a reused system symbol from a past universe cannot
//     pollute membership);
//   - gate 2: the goods the hub's OWN market EXPORTS/EXCHANGES (localProductionGoods);
//   - gate 3: the live-tunable source-distance floor.
//
// Fail-open throughout: a nil DB, an empty waypoint, or a per-source error leaves that gate's input
// unresolved (gate 1/2 admit, gate 3 still applies its floor) so a transient read never empties a
// warehouse. It is the production resolver; tests construct a bufferGateContext directly.
func (s *DaemonServer) depotBufferGateContext(ctx context.Context, hubWaypoint string, playerID int) bufferGateContext {
	gateCtx := bufferGateContext{
		gate: buffer.Gate{MinExternalSourceDistance: float64(s.liveDepotBufferMinSourceDistance(ctx, playerID))},
	}
	if s.db == nil || hubWaypoint == "" {
		return gateCtx
	}
	history := persistence.NewHistoryRepository(s.db)
	eraID, _ := history.CurrentEraID(ctx, playerID) // nil on error -> all-eras (fail-open); membership stays a filter
	if goods, err := history.ContractGoodCountsForDeliveryWaypoint(ctx, eraID, hubWaypoint); err == nil {
		gateCtx.hubContractGoods = goods
	}
	if mkt, err := persistence.NewMarketRepository(s.db).GetMarketData(ctx, hubWaypoint, playerID); err == nil {
		gateCtx.hubLocalProduction = localProductionGoods(mkt)
	}
	return gateCtx
}
