// Package queries holds read-only trading read-models. ProfitableLaneReader counts the
// profitable, feasible arbitrage lanes visible in a player's trading grounds — the "unserved trade
// demand" signal the fleet autosizer's heavy-demand provider sizes to.
//
// It is a READ-ONLY twin of the trade-route coordinator's lane scan — same market cache, freshness
// table and domain primitives — that never touches the coordinator's state or flies anything: a
// read that influences a spend must not have a write side effect (RULINGS #4).
package queries

import (
	"context"
	"errors"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// laneSpanHops is the widest one-way distance whose two ends can land in ONE of the trade
// executor's lane pools. It ranks over its own system plus its DIRECTLY-gated neighbours (the tour
// coordinator's candidate set is the same closed neighbourhood), so a pool spans a hop in and a hop
// back out. A pair further apart is never ranked together by any hull this count sizes, so counting
// it demands a heavy for unselectable work (RULINGS #4) — deliberately tighter than the pre-buy
// Routable bound, which asks whether a bought hull can be FERRIED to a post, not whether a lane can
// be flown from one.
const laneSpanHops = 2

// laneMarketReader is the narrow slice of market.MarketRepository the lane count consumes: the
// per-system waypoint list and each waypoint's cached market (*MarketRepositoryGORM satisfies it).
type laneMarketReader interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// laneReachabilityReader is the narrow slice of gategraph.Service the census needs: gate-hop
// distances over the STORED adjacency only.
//
// StoredHopDistances is the load-bearing choice among the resolvers: it never fetches, so a census
// over the pooled surface cannot become an API storm; it refuses to route through under-construction
// or stale topology; and it expresses refusal by ABSENCE, the fail-closed reading a spend needs.
type laneReachabilityReader interface {
	StoredHopDistances(ctx context.Context, fromSystem string, targets []string, maxJumps int) (map[string]int, error)
}

// ProfitableLaneReader counts the profitable, feasible arbitrage lanes across a set of systems from
// the persisted market cache.
type ProfitableLaneReader struct {
	markets laneMarketReader
	reach   laneReachabilityReader
	clock   shared.Clock
	// ageCaps drops stale listings. Its ZERO VALUE is the fitted armed default (RankerAgeCaps.For);
	// a captain's RETUNE reaches the census only through SetRankerAgeCaps.
	ageCaps trading.RankerAgeCaps
	// staleness charges each surviving listing for its age, so the census counts the lanes
	// still worth flying rather than the lanes an hours-old quote makes look worth flying.
	staleness trading.StalenessDiscount
}

// NewProfitableLaneReader wires the reader over its market and reachability sources. Both are
// required: without reachability the count fails CLOSED rather than counting pairings nothing
// established a route to (RULINGS #4).
func NewProfitableLaneReader(markets laneMarketReader, reach laneReachabilityReader) *ProfitableLaneReader {
	return &ProfitableLaneReader{markets: markets, reach: reach, clock: shared.NewRealClock()}
}

// SetRankerAgeCaps wires the config-resolved freshness table the executor's ranker also runs. Left
// on the fitted defaults under a TIGHTENED knob the census keeps counting lanes the ranker already
// dropped, and each one sizes a hull purchase (RULINGS #4) — so every construction calls this.
func (r *ProfitableLaneReader) SetRankerAgeCaps(caps trading.RankerAgeCaps) {
	r.ageCaps = caps
}

// SetStalenessDiscount wires the same quote-age discount the ranker charges. Without it the
// census counts at face value lanes the ranker has already discounted out of contention.
func (r *ProfitableLaneReader) SetStalenessDiscount(discount trading.StalenessDiscount) {
	r.staleness = discount
}

// LaneCensus is one census reading: Profitable is the count the wave consumes, CrossSystem how many
// of those lanes end in another system, AbsorbableUnits what they can move in one trip (the consumer
// buys one hull per lane, which is hull-trips of work only while the lanes are as deep as a hold),
// and the rejections are what the narrowing tests refused — at the FIRST test that refused each
// pair, and only where it WOULD have been work. A bare count cannot tell a thin surface from a
// reachability failure; both print as a small number.
type LaneCensus struct {
	Profitable          int
	CrossSystem         int
	AbsorbableUnits     int
	RejectedUnreachable int
	RejectedJumpCost    int
}

// CountProfitableLanes returns how many distinct profitable arbitrage lanes the given systems
// offer: every (good, source, dest) circuit that survives all three tests below.
//
// It is a CENSUS, and both halves matter. Listings are POOLED across the systems, so a lane whose
// ends sit in different systems counts; and every surviving pair counts, not the best one per good —
// trading.RankSpreads is the SELECTION primitive and would keep one lane per good, so WalkLanes is
// the census.
//
// The three tests are all NARROWING, and each is a money guard on the hull-purchase path this count
// feeds (demand = current heavies + unserved lanes), so each refuses rather than guesses:
//
//   - FRESHNESS: a listing past its activity's cap forms no pair, so a system the fleet visited
//     once cannot cross-product its months-old quotes with every fresh market.
//   - REACHABILITY: BOTH legs of the circuit must be PROVABLE over the stored graph, within
//     laneSpanHops — the span of one of the executor's lane pools.
//   - TRIP VALUE: ClearsFloorAfterGates — MinBidMargin survives a gate fee per crossing, both ways.
//
// readable is FALSE only on a genuine read FAILURE (a system's market-list read, or the gate store)
// — the heavy provider then fails closed. An empty cache or a system with no surviving lane is a
// readable ZERO. An individual unreadable waypoint market is skipped, fail-open at the finest grain.
func (r *ProfitableLaneReader) CountProfitableLanes(ctx context.Context, playerID int, systems []string) (LaneCensus, bool, error) {
	if r.reach == nil {
		return LaneCensus{}, false, errors.New("profitable lane census: no reachability source wired")
	}
	now := r.clock.Now()
	scanned := dedupeStrings(systems)

	var pooled []trading.GoodListing
	for _, system := range scanned {
		listings, err := r.collectSystemListings(ctx, system, playerID, now)
		if err != nil {
			// A genuine market-list read failure fails the WHOLE count closed — never a silent
			// under-count feeding a spend decision (mirrors the coordinator's scanLanes, which
			// returns the error rather than ranking a partial surface).
			return LaneCensus{}, false, err
		}
		pooled = append(pooled, listings...)
	}

	hops, err := r.hopDistances(ctx, scanned)
	if err != nil {
		return LaneCensus{}, false, err
	}

	// WalkLanes, not EnumerateLanes: pairs are quadratic in the POOLED market count, and holding
	// that slice to rank it and discard the ranking is a per-tick cost growing with the rotation.
	census := LaneCensus{}
	good := ""
	seen := map[laneEnds]struct{}{}
	trading.WalkLanes(pooled, func(lane trading.ArbitrageLane) {
		if lane.Good != good {
			good = lane.Good
			clear(seen)
		}
		crossings, reachable := laneRoundTrip(hops, lane)
		if !reachable {
			// Both terms carry the SAME counterfactual, or the larger one reads as a routing gap
			// when it is mostly pairs that were never worth flying.
			if worthCrossingFor(lane) {
				census.RejectedUnreachable++
			}
			return
		}
		if !lane.ClearsFloorAfterGates(crossings, domainSensing.DefaultGateFeeCredits) {
			if worthCrossingFor(lane) {
				census.RejectedJumpCost++
			}
			return
		}
		ends := laneEnds{source: lane.SourceWaypoint, dest: lane.DestWaypoint}
		if _, dup := seen[ends]; dup {
			return
		}
		seen[ends] = struct{}{}
		census.Profitable++
		census.AbsorbableUnits += lane.VolumeCap
		// Read off the SAME hop count that priced the crossing, so the term cannot disagree with it.
		if crossings > 0 {
			census.CrossSystem++
		}
	})
	return census, true, nil
}

// laneEnds identifies one good's lane by the circuit it flies, so two listings quoting that good at
// one waypoint cannot present the same lane twice and have it counted twice.
type laneEnds struct{ source, dest string }

// hopDistances resolves, for every scanned system, the gate-hop distance to every other scanned
// system over the stored graph. One bounded walk per origin covers all of that origin's lanes.
// A store failure propagates so the whole count fails closed.
func (r *ProfitableLaneReader) hopDistances(ctx context.Context, systems []string) (map[string]map[string]int, error) {
	out := make(map[string]map[string]int, len(systems))
	for _, from := range systems {
		distances, err := r.reach.StoredHopDistances(ctx, from, systems, laneSpanHops)
		if err != nil {
			return nil, err
		}
		out[from] = distances
	}
	return out, nil
}

// worthCrossingFor reports whether the pair would have been work with nothing to cross — the shared
// counterfactual behind both rejection terms. It is ClearsFloorAfterGates at zero crossings and not
// ClearsFloor because a sink absorbing nothing earns nothing at any spread, and blaming that on the
// gates reports a fee problem where there is no cargo to move.
func worthCrossingFor(lane trading.ArbitrageLane) bool {
	return lane.ClearsFloorAfterGates(0, domainSensing.DefaultGateFeeCredits)
}

// laneRoundTrip returns the crossings a hull dedicated to this lane pays — out with a cargo, back to
// re-buy — and whether BOTH legs were proven. The fee is per ORIGIN gate, so the return is billed at
// the same rate, and the stored graph is directed in ordinary states (one end charted and not the
// other, one end's rows aged out): asking once answers half the question at the full price, which
// buys a hull against a circuit nothing proves it can complete (RULINGS #4).
//
// A missing distance is unproven reach, not zero: absence is how the stored walk expresses refusal.
func laneRoundTrip(hops map[string]map[string]int, lane trading.ArbitrageLane) (int, bool) {
	source := shared.ExtractSystemSymbol(lane.SourceWaypoint)
	dest := shared.ExtractSystemSymbol(lane.DestWaypoint)
	outbound, proven := hops[source][dest]
	if !proven {
		return 0, false
	}
	inbound, proven := hops[dest][source]
	if !proven {
		return 0, false
	}
	return outbound + inbound, true
}

// collectSystemListings reads every cached market in one system into flat trading.GoodListing rows —
// the read-only mirror of the trade-route coordinator's own, and it must keep that mirror EXACT:
// Bid = SellPrice() (what we receive), Ask = PurchasePrice() (what we pay), or this reader's lane
// COUNT disagrees with the lanes the coordinator will fly. Rows past their activity's freshness cap
// are dropped for the same reason, through the same shared predicate. A system's market-list read
// error propagates (fail-closed); an unreadable waypoint market contributes no listings.
func (r *ProfitableLaneReader) collectSystemListings(ctx context.Context, systemSymbol string, playerID int, now time.Time) ([]trading.GoodListing, error) {
	waypoints, err := r.markets.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}
	var listings []trading.GoodListing
	for _, wp := range waypoints {
		mkt, err := r.markets.GetMarketData(ctx, wp, playerID)
		if err != nil || mkt == nil {
			continue // an unreadable/missing market simply doesn't contribute lanes
		}
		for _, g := range mkt.TradeGoods() {
			listing := trading.GoodListing{
				Good:       g.Symbol(),
				Waypoint:   mkt.WaypointSymbol(),
				TradeType:  string(g.TradeType()),
				Bid:        g.SellPrice(),     // sell_price = what we RECEIVE selling TO it
				Ask:        g.PurchasePrice(), // purchase_price = what we PAY buying FROM it
				Supply:     derefString(g.Supply()),
				Activity:   derefString(g.Activity()),
				Volume:     g.TradeVolume(),
				ObservedAt: mkt.LastUpdated(),
			}
			if !r.ageCaps.Fresh(listing, now) {
				continue
			}
			if !listing.ObservedAt.IsZero() {
				age := now.Sub(listing.ObservedAt)
				listing.Ask = r.staleness.AdjustedAsk(listing.Ask, listing.Activity, age)
				listing.Bid = r.staleness.AdjustedBid(listing.Bid, listing.Activity, age)
			}
			listings = append(listings, listing)
		}
	}
	return listings, nil
}

// dedupeStrings returns the distinct, non-empty entries of s, preserving first-seen order — so a
// system a player has several hulls in is scanned once.
func dedupeStrings(s []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// derefString returns the pointed-to string, or "" for a nil pointer (the market Supply/Activity
// fields are optional).
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
