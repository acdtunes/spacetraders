package mvt

import (
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// Hull is the ranking subject. CreditsPerSec is the hull's own recent earning rate;
// zero means "no estimate yet" and Rank substitutes Costs.FleetCreditsPerSec.
type Hull struct {
	Symbol        string
	System        string
	CargoCapacity int
	CreditsPerSec float64
}

// Candidate is a reachable system with its ledger-derived yield. YieldCredits is the
// summed unoccupied depth × spread; DepthUnits the summed unoccupied depth; InTransit the
// number of other hulls that have claimed it and not arrived.
type Candidate struct {
	System        string
	Hops          int
	YieldCredits  float64
	DepthUnits    int
	InTransit     int
	EntryWaypoint string
}

// Costs are the fleet-level terms of the travel penalty and the in-transit penalty.
type Costs struct {
	TollSecondsPerHop  int
	GateFeeFromCurrent int64
	FleetDrawPerVisit  float64
	FleetCreditsPerSec float64
}

// ScoredSystem is a ranked candidate in credits per unit of the hull's next load.
type ScoredSystem struct {
	System          string
	Hops            int
	ExpectedPerUnit float64
	TravelPerUnit   float64
	Score           float64
	EntryWaypoint   string
}

const (
	tradeTypeExport   = "EXPORT"
	tradeTypeImport   = "IMPORT"
	tradeTypeExchange = "EXCHANGE"
)

// SystemYield is the money a hull could take out of one system's own markets and the depth
// behind it. Per good it MATCHES unoccupied source depth against unoccupied sink depth,
// richest spread first, consuming both sides as it goes — so a good's units can never exceed
// min(Σ source depth, Σ sink depth), the depth that is actually there.
//
// Summing every source×sink pair instead (this function's first form, review round 1 of
// sp-t5xe6) multiplies a good that trades at k sources and k sinks by about k(k−1): Rank's
// load = min(DepthUnits, cap) then saturates on depth no hull can lift and the in-transit
// penalty is diluted against credits that do not exist, which is exactly the fleet-spreading
// property the ranker is for. Matching is not an approximation of the true figure: profit is
// separable (destination Bid − source Ask), so richest-spread-first is the exact optimum of
// the underlying transportation problem.
//
// Contributing nothing: rows stale per caps; crossed quotes (trading.GoodListing.IsCrossed —
// the sp-en5h7 transposed market_data rows, bad data and never a bargain, refused the same way
// by the sibling ranker's tradeableByGood); spreads not positive or under minSpread (0 = no
// floor); and a pair whose two ends are the same waypoint (an EXCHANGE selling to itself).
// entryWaypoint is the source waypoint of the richest matched pair of the good contributing most.
func SystemYield(lanes []LaneDepth, caps trading.RankerAgeCaps, now time.Time, minSpread float64) (credits float64, units int, entryWaypoint string) {
	type side struct {
		wp    string
		price int
		depth float64
	}
	sources := map[string][]side{}
	sinks := map[string][]side{}
	for _, l := range lanes {
		if !caps.Fresh(l.Listing, now) || l.Listing.IsCrossed() {
			continue
		}
		g := l.Listing.Good
		if l.Listing.TradeType == tradeTypeExport || l.Listing.TradeType == tradeTypeExchange {
			d := math.Max(0, float64(l.Listing.Volume)-float64(l.BuyPlanned)-l.BuyResidual)
			sources[g] = append(sources[g], side{l.Listing.Waypoint, l.Listing.Ask, d})
		}
		if l.Listing.TradeType == tradeTypeImport || l.Listing.TradeType == tradeTypeExchange {
			d := math.Max(0, float64(l.Listing.Volume)-float64(l.SellPlanned)-l.SellResidual)
			sinks[g] = append(sinks[g], side{l.Listing.Waypoint, l.Listing.Bid, d})
		}
	}

	// Goods in name order, not map order: two callers ranking the same system must get the
	// same entryWaypoint out of a tie.
	goods := make([]string, 0, len(sources))
	for g := range sources {
		goods = append(goods, g)
	}
	sort.Strings(goods)

	type pair struct {
		src, sink int
		spread    float64
	}
	depth, bestGood := 0.0, 0.0
	for _, g := range goods {
		srcs := append([]side(nil), sources[g]...)
		snks := append([]side(nil), sinks[g]...)
		pairs := make([]pair, 0, len(srcs)*len(snks))
		for si := range srcs {
			for ki := range snks {
				if srcs[si].wp == snks[ki].wp {
					continue
				}
				spread := float64(snks[ki].price - srcs[si].price)
				if spread <= 0 || spread < minSpread {
					continue
				}
				pairs = append(pairs, pair{si, ki, spread})
			}
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].spread != pairs[j].spread {
				return pairs[i].spread > pairs[j].spread
			}
			if srcs[pairs[i].src].wp != srcs[pairs[j].src].wp {
				return srcs[pairs[i].src].wp < srcs[pairs[j].src].wp
			}
			return snks[pairs[i].sink].wp < snks[pairs[j].sink].wp
		})

		goodCredits, bestPair, goodEntry := 0.0, 0.0, ""
		for _, p := range pairs {
			flow := math.Min(srcs[p.src].depth, snks[p.sink].depth)
			if flow <= 0 {
				continue
			}
			srcs[p.src].depth -= flow
			snks[p.sink].depth -= flow
			c := flow * p.spread
			goodCredits += c
			depth += flow
			if c > bestPair {
				bestPair, goodEntry = c, srcs[p.src].wp
			}
		}
		credits += goodCredits
		if goodCredits > bestGood {
			bestGood, entryWaypoint = goodCredits, goodEntry
		}
	}
	return credits, int(depth), entryWaypoint
}

// Rank scores every candidate in credits per unit of the hull's next load, net of travel,
// and sorts best-first. A candidate with no depth is dropped, never scored zero.
func Rank(hull Hull, cands []Candidate, costs Costs) []ScoredSystem {
	if hull.CargoCapacity <= 0 {
		return nil
	}
	cap := float64(hull.CargoCapacity)
	rate := hull.CreditsPerSec
	if rate <= 0 {
		rate = costs.FleetCreditsPerSec
	}
	out := make([]ScoredSystem, 0, len(cands))
	for _, c := range cands {
		if c.DepthUnits <= 0 {
			continue
		}
		credits := math.Max(0, c.YieldCredits-float64(c.InTransit)*costs.FleetDrawPerVisit)
		w := credits / float64(c.DepthUnits)
		load := math.Min(float64(c.DepthUnits), cap)
		expected := load * w / cap
		travel := 0.0
		if c.System != hull.System {
			hops := float64(c.Hops)
			travel = (hops*float64(costs.TollSecondsPerHop)*rate + hops*float64(costs.GateFeeFromCurrent)) / cap
		}
		out = append(out, ScoredSystem{System: c.System, Hops: c.Hops, ExpectedPerUnit: expected,
			TravelPerUnit: travel, Score: expected - travel, EntryWaypoint: c.EntryWaypoint})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].System < out[j].System
	})
	return out
}

// BestAlternative is the best-ranked system other than current.
func BestAlternative(ranked []ScoredSystem, current string) (ScoredSystem, bool) {
	for _, s := range ranked {
		if s.System != current {
			return s, true
		}
	}
	return ScoredSystem{}, false
}
