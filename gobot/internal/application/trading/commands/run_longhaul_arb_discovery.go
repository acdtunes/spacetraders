package commands

// run_longhaul_arb_discovery.go — out-of-horizon lane discovery for the long-haul engine
// (sp-mepj §2). It REUSES the read-only global-best scanners on the market repository (the
// sink half is sp-mtvg's BestSinksAcrossSystems; the source half its new symmetric
// BestSourcesAcrossSystems) and pairs their results per good into cross-system directed
// candidate lanes — the multi-hop lanes the 1-gate-hop tour/arb discovery horizon
// structurally cannot see. Pricing/ranking is run_longhaul_arb_pricing.go.

import (
	"context"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// longHaulSinkScanner / longHaulSourceScanner are the read-only global-best discovery ports.
// Declared as one-method local ports (mirroring the tour coordinator's outOfHorizonSinkScanner)
// so the engine depends on the narrow capability and both are faked trivially in tests; the
// concrete *persistence.MarketRepositoryGORM already satisfies both.
type longHaulSinkScanner interface {
	BestSinksAcrossSystems(ctx context.Context, goods []string, playerID int, maxAge time.Duration, now time.Time) (map[string]market.GlobalSinkResult, error)
}

type longHaulSourceScanner interface {
	BestSourcesAcrossSystems(ctx context.Context, goods []string, playerID int, maxAge time.Duration, now time.Time) (map[string]market.GlobalSourceResult, error)
}

// gateHopResolver reports the REAL gate-graph hop count between two systems and whether a
// route exists at all. The worker fills it from the shared gategraph.Service (the sp-s232
// travel machinery); discovery stays unit-testable behind this narrow func. An unroutable
// pair (routable=false) is never a candidate — the same pre-spend routability guard the
// one-shot arb applies before buying.
type gateHopResolver func(fromSystem, toSystem string) (hops int, routable bool)

// routeCostEstimator fills a candidate's within-system travel time and route fuel from the
// resolved hop count (both legs). The worker supplies the real estimator (route planner +
// per-hop refuel); a nil estimator leaves fuel/intra zero (the pricing then ranks on the
// gate-hop time alone).
type routeCostEstimator func(gateHops int) (fuelCredits int, intraSeconds float64)

// assembleLongHaulCandidates pairs each good's cheapest cross-system SOURCE with its best
// cross-system SINK into a directed long-haul lane, keeping only OUT-OF-HORIZON,
// positive-spread, ROUTABLE lanes:
//   - a good with no source (or no sink) is dropped;
//   - a same-system pair is the tour's 1-hop horizon, not long-haul's — dropped;
//   - a non-positive spread (DestBid <= SourceAsk) is dropped;
//   - an unroutable system pair is dropped (never buy cargo that cannot be delivered).
//
// VolumeCap = min(source, sink) trade_volume — the market-absorption depth both sides
// impose, exactly as trading.RankSpreads computes it for same-system lanes. Deterministic
// good order so selection and logs are stable.
func assembleLongHaulCandidates(
	sinks map[string]market.GlobalSinkResult,
	sources map[string]market.GlobalSourceResult,
	hops gateHopResolver,
	cost routeCostEstimator,
) []longHaulCandidate {
	goods := make([]string, 0, len(sinks))
	for good := range sinks {
		goods = append(goods, good)
	}
	sort.Strings(goods)

	candidates := make([]longHaulCandidate, 0, len(goods))
	for _, good := range goods {
		sink := sinks[good]
		source, ok := sources[good]
		if !ok {
			continue // no cheapest source for this sink's good
		}
		if source.SystemSymbol == sink.SystemSymbol {
			continue // same-system lane: the tour solver's 1-hop horizon, not long-haul's
		}
		spread := sink.Bid - source.Ask
		if spread <= 0 {
			continue
		}
		gateHops, routable := hops(source.SystemSymbol, sink.SystemSymbol)
		if !routable {
			continue // no jump-gate route: never a candidate (pre-spend routability guard)
		}
		volumeCap := source.TradeVolume
		if sink.TradeVolume < volumeCap {
			volumeCap = sink.TradeVolume
		}
		fuel, intra := 0, 0.0
		if cost != nil {
			fuel, intra = cost(gateHops)
		}
		candidates = append(candidates, longHaulCandidate{
			Lane: trading.ArbitrageLane{
				Good:           good,
				SourceWaypoint: source.WaypointSymbol,
				DestWaypoint:   sink.WaypointSymbol,
				SourceAsk:      source.Ask,
				DestBid:        sink.Bid,
				SpreadPerUnit:  spread,
				VolumeCap:      volumeCap,
			},
			GateHops:             gateHops,
			IntraSeconds:         intra,
			FuelCreditsOverRoute: fuel,
		})
	}
	return candidates
}

// goodUniverse lists the goods with fresh market data for the player — the discovery scan set.
// Backed by the market repo's DistinctTradedGoods.
type goodUniverse interface {
	DistinctTradedGoods(ctx context.Context, playerID int, maxAge time.Duration, now time.Time) ([]string, error)
}

// gateGraphPathfinder returns the ordered gate-graph system path between two systems (J jumps
// -> J+1 elements) for a player — the shared gategraph.Service satisfies it (Path). The
// discoverer turns it into the context-free gateHopResolver assembleLongHaulCandidates wants
// (hop count = len(path)-1, routable = a non-empty path), threading ctx+playerID that the pure
// assembly must not carry.
type gateGraphPathfinder interface {
	Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error)
}

// longHaulDiscoverer composes the full discovery pass behind the worker's laneDiscoverer port:
// the goods universe -> the shared BestSinks/BestSources scanners -> assembleLongHaulCandidates
// -> rankLongHaulLanes. It holds only reused components + the ranking parameters; the daemon
// binds gateGraph to gategraph.Service and cost to a route-cost estimator. maxAge bounds
// staleness (a lane priced from an observation older than this cannot win).
type longHaulDiscoverer struct {
	universe    goodUniverse
	sinks       longHaulSinkScanner
	sources     longHaulSourceScanner
	gateGraph   gateGraphPathfinder
	cost        routeCostEstimator
	model       laneImpactModel
	clock       shared.Clock
	maxAge      time.Duration
	minSpread   int
	marginFloor float64
}

// DiscoverLanes runs one discovery pass, returning the ranked profitable out-of-horizon lanes.
// Any scanner error aborts the pass (the worker treats it as "no lane this scan"); an empty
// universe or no candidate short-circuits to nil. The gate-hop closure threads this pass's
// ctx+playerID into the shared gate graph while keeping the assembly pure.
func (d *longHaulDiscoverer) DiscoverLanes(ctx context.Context, playerID int) ([]pricedLongHaulLane, error) {
	now := d.clock.Now()
	goods, err := d.universe.DistinctTradedGoods(ctx, playerID, d.maxAge, now)
	if err != nil {
		return nil, err
	}
	if len(goods) == 0 {
		return nil, nil
	}
	sinks, err := d.sinks.BestSinksAcrossSystems(ctx, goods, playerID, d.maxAge, now)
	if err != nil {
		return nil, err
	}
	sources, err := d.sources.BestSourcesAcrossSystems(ctx, goods, playerID, d.maxAge, now)
	if err != nil {
		return nil, err
	}
	hops := func(fromSystem, toSystem string) (int, bool) {
		if fromSystem == toSystem {
			return 0, true
		}
		path, perr := d.gateGraph.Path(ctx, fromSystem, toSystem, playerID)
		if perr != nil || len(path) < 2 {
			return 0, false
		}
		return len(path) - 1, true
	}
	candidates := assembleLongHaulCandidates(sinks, sources, hops, d.cost)
	return rankLongHaulLanes(d.model, candidates, d.minSpread, d.marginFloor), nil
}
