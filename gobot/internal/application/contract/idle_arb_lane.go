package contract

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// IdleArbLane is a scored hub-local lane candidate.
type IdleArbLane struct {
	Good          string
	SellAt        string
	MarginPerUnit int
	Distance      float64
	SourceAsk     int
	DestBid       int
}

// isBlacklisted reports whether good is on the configured excluded list.
func (d *IdleArbDispatcher) isBlacklisted(good string) bool {
	_, ok := d.blacklist[strings.ToUpper(good)]
	return ok
}

// idleArbMinMargin is the effective per-unit floor a leg hands the arb run's
// live-verify gate: the tighter of the absolute floor and the relative one
// (ceil(fraction × quoted margin)). Passing THIS as the run's MinMargin makes
// the run's existing live-refresh + fail-closed abort reject a leg whose live
// margin has slipped below the fraction of its quote.
func idleArbMinMargin(cfg IdleArbConfig, quotedMargin int) int {
	relative := int(math.Ceil(cfg.MarginVerifyFraction * float64(quotedMargin)))
	if relative > cfg.MinMarginPerUnit {
		return relative
	}
	return cfg.MinMarginPerUnit
}

// laneNetPerUnit is the lane's per-unit net at the prices passed in:
// the gross spread (sink bid − hub ask) minus the per-unit fuel estimate. Callers
// pass the FRESHLY-READ ask/bid every pass, so a lane the fleet's own repeated
// buys have inflated (or whose sink its dumps have decayed) is re-scored down
// trip-over-trip — never off a cached spread.
func (d *IdleArbDispatcher) laneNetPerUnit(hubAsk, sinkBid int) int {
	return (sinkBid - hubAsk) - d.cfg.FuelCostPerUnit
}

// netProfitFloor is the BINDING per-unit floor for a lane bought at
// hubAsk: the greater of the absolute after-fuel floor and the fraction-of-buy
// floor. The relative floor is what stops a HIGH-PRICED good with a thin absolute
// spread from clearing on the flat floor alone (e.g. buy 5000/u, +265 net clears
// 100 but not 20%×5000 = 1000).
func (d *IdleArbDispatcher) netProfitFloor(hubAsk int) int {
	relative := int(math.Ceil(d.cfg.NetProfitFraction * float64(hubAsk)))
	if relative > d.cfg.MinNetProfitPerUnit {
		return relative
	}
	return d.cfg.MinNetProfitPerUnit
}

// laneClearsProfitFloor reports whether the lane's live net_per_u meets
// its binding floor — the per-trip go/no-go the dispatcher applies to an otherwise
// eligible lane before launching. A lane that fails auto-re-enters the next pass
// its price recovers (a profitability skip never latches the lane mutex).
func (d *IdleArbDispatcher) laneClearsProfitFloor(hubAsk, sinkBid int) bool {
	return d.laneNetPerUnit(hubAsk, sinkBid) >= d.netProfitFloor(hubAsk)
}

// pickHubLocalLane scores every (good, sell-market) pair reachable from the
// hull's CURRENT waypoint and returns the best positive-margin lane that PASSES
// every dispatch-time guard, together with a skip reason. Prices are the scanned
// cache — deliberately: the arb run itself live-refreshes the source and
// re-gates the margin fail-closed (now against the tighter relative floor) before
// any credit moves, so a stale pick here costs at worst a wasted (refused) leg.
//
// The return distinguishes three outcomes for the skip counters:
//   - a lane + skipNone: fly it.
//   - nil + a guard reason: a profitable lane existed but EVERY candidate was
//     refused by a guard (blacklist / open-contract good / leash) — a
//     skipped-by-guard leg, attributed to the reason of the best refused lane.
//   - nil + skipNone: no profitable local lane at all — idle for lack of
//     opportunity, not a guard skip.
func (d *IdleArbDispatcher) pickHubLocalLane(ctx context.Context, hull *navigation.Ship, excludedContractGoods map[string]struct{}, consult absorptionConsult) (*IdleArbLane, idleArbSkipReason) {
	logger := common.LoggerFromContext(ctx)
	origin := hull.CurrentLocation()
	if origin == nil {
		return nil, skipNone
	}

	hubMarket, err := d.marketRepo.GetMarketData(ctx, origin.Symbol, d.playerID.Value())
	if err != nil || hubMarket == nil || hubMarket.GoodsCount() == 0 {
		return nil, skipNone // the hub standby station isn't a scanned market — nothing to fly
	}

	systemSymbol := shared.ExtractSystemSymbol(origin.Symbol)
	graphResult, err := d.graphProvider.GetGraph(ctx, systemSymbol, false, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: no system graph for %s: %v", systemSymbol, err), nil)
		return nil, skipNone
	}

	marketWaypoints, err := d.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: market listing failed for %s: %v", systemSymbol, err), nil)
		return nil, skipNone
	}

	// bestAllowed is the best lane passing every guard; bestExcluded is the best
	// profitable lane a guard refused (with its reason). If nothing passes but a
	// profitable lane was refused, the reason of bestExcluded is what skipped the
	// leg — a fair attribution because bestAllowed==nil means ALL profitable
	// candidates were refused, so the best one's reason is representative.
	var bestAllowed *IdleArbLane
	bestAllowedScore := 0
	var bestExcluded *IdleArbLane
	bestExcludedScore := 0
	bestExcludedReason := skipNone

	for _, wp := range marketWaypoints {
		if wp == origin.Symbol {
			continue
		}
		coord, ok := graphResult.Graph.Waypoints[wp]
		if !ok {
			continue
		}
		distance := origin.DistanceTo(coord)
		if distance > d.cfg.HubRadius {
			continue // hub-LOCAL outer bound: the hull must stay a short hop from home
		}

		destMarket, err := d.marketRepo.GetMarketData(ctx, wp, d.playerID.Value())
		if err != nil || destMarket == nil {
			continue
		}

		for _, hubGood := range hubMarket.TradeGoods() {
			destGood := destMarket.FindGood(hubGood.Symbol())
			lane, score, units, ok := quoteHubLane(hull, hubGood, destGood, wp, distance, d.cfg)
			if !ok {
				continue
			}

			// The sink's absorptive depth (its trade volume) and this leg's
			// lot feed the depth-aware absorption consult, so a partially-reserved sink
			// with room for the tranche is not vetoed.
			candidate := laneCandidate{
				hull: hull, lane: lane,
				buy: origin, sell: coord,
				buyMarket: hubMarket, sellMarket: destMarket,
				sinkDepthCap: destGood.TradeVolume(), legUnits: units,
			}
			reason := d.laneSkipReason(candidate, excludedContractGoods, consult)

			// The FINAL money guard on an otherwise-eligible lane. Re-priced
			// this pass from the ask/bid read above (never a cached spread), it
			// refuses a lane whose live net_per_u (spread - fuel) is below the
			// binding floor. Only checked when nothing cheaper already excluded
			// the lane, so a below-floor lane is attributed to this gate (not
			// masked by a policy/leash skip), and it auto-re-enters the pass
			// its price recovers.
			if reason == skipNone && !d.laneClearsProfitFloor(lane.SourceAsk, lane.DestBid) {
				reason = skipReasonUnprofitable
			}

			// Per-candidate verdict logging: emit one terse line for
			// every positive-margin candidate with the COMPUTED distance the leash
			// check used, the two endpoints (with coordinates) it measured between,
			// the quoted margin, the buy/sell market read ages, and the verdict.
			// This is the candidate list an all-pairs analyst scan is diffed
			// against: a masked mis-pick (a wrong distance/endpoint pair, a stale
			// cache row, an over-broad exclusion) is visible here per lane. Log
			// only — it never alters the pick, a threshold, or a guard (RULINGS #4).
			d.logCandidate(ctx, candidate, reason)

			if reason != skipNone {
				if bestExcluded == nil || score > bestExcludedScore {
					bestExcluded = lane
					bestExcludedScore = score
					bestExcludedReason = reason
				}
				continue
			}
			if bestAllowed == nil || score > bestAllowedScore {
				bestAllowed = lane
				bestAllowedScore = score
			}
		}
	}

	if bestAllowed != nil {
		return bestAllowed, skipNone
	}
	if bestExcluded != nil {
		return nil, bestExcludedReason
	}
	return nil, skipNone
}

// quoteHubLane prices one good's hub->sink lane, scoring margin x tranche. ok is
// false when the good makes no lane; an EXPORT sink is never sold into.
func quoteHubLane(hull *navigation.Ship, hubGood market.TradeGood, destGood *market.TradeGood, sellAt string, distance float64, cfg IdleArbConfig) (*IdleArbLane, int, int, bool) {
	ask := hubGood.PurchasePrice() // what the hull pays at the hub
	if ask <= 0 || destGood == nil || destGood.TradeType() == market.TradeTypeExport {
		return nil, 0, 0, false
	}
	bid := destGood.SellPrice() // what the hull receives at the destination
	margin := bid - ask
	if margin < cfg.MinMarginPerUnit {
		return nil, 0, 0, false
	}
	units := hull.AvailableCargoSpace()
	if affordable := cfg.MaxSpendPerLeg / ask; affordable < units {
		units = affordable
	}
	if units <= 0 {
		return nil, 0, 0, false
	}
	return &IdleArbLane{
		Good:          hubGood.Symbol(),
		SellAt:        sellAt,
		MarginPerUnit: margin,
		Distance:      distance,
		SourceAsk:     ask,
		DestBid:       bid,
	}, margin * units, units, true
}
