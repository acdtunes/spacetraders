package commands

import (
	"math"
	"sort"
)

// This file is the SCORE + PLACE engine for the contract-hub coordinator (sp-q2zq). It is pure
// (no ports), so the economic algorithm — the Analyst owns every weight — is unit-tested directly.

// hubPosition is a homed hub's in-system position (feeds the coverage baseline).
type hubPosition struct {
	X, Y float64
}

// hubPlacement is one placement decision: home ShipSymbol at HubWaypoint.
type hubPlacement struct {
	ShipSymbol  string
	HubWaypoint string
}

// computeDemandWeights folds the recent contracts (oldest→newest) into an EWMA per contract good:
// demand w_G = smoothed(payment_on_fulfilled × recurrence). Smoothing is MANDATORY (bead) — the
// raw signal is thin/noisy (a ~46-contract era, most goods seen once), so single-contract noise
// must not move a home.
//
// The half-life is expressed in CONTRACTS (not wall-clock): alpha = 1 − 2^(−1/halfLife). Each
// contract step advances EVERY good tracked so far — a present good samples its payment, an absent
// good samples 0 — so a good that RECURS keeps a high weight while a one-off decays. A brand-new
// good first seen at step i starts from 0, so its first contribution is only alpha × payment
// (~3% at half-life 23): exactly the noise-rejection the placement stability relies on.
func computeDemandWeights(contracts []ContractDemandRecord, halfLife float64) map[string]float64 {
	weights := make(map[string]float64)
	if len(contracts) == 0 {
		return weights
	}
	if halfLife <= 0 {
		halfLife = defaultContractHubEWMAHalfLife
	}
	alpha := 1.0 - math.Pow(2.0, -1.0/halfLife)

	for _, c := range contracts {
		present := make(map[string]bool, len(c.Goods))
		for _, g := range c.Goods {
			present[g] = true
			if _, tracked := weights[g]; !tracked {
				weights[g] = 0 // begin tracking a newly-seen good from a zero baseline
			}
		}
		for g := range weights {
			sample := 0.0
			if present[g] {
				sample = float64(c.PaymentOnFulfilled)
			}
			weights[g] = alpha*sample + (1.0-alpha)*weights[g]
		}
	}
	return weights
}

// buildCoverage computes coverage_G for each contract good: the min distance from any already-homed
// hub position to G's cheapest source S_G — i.e. the buy-leg a contract for G would incur under
// closest-ship-wins. With NO homes yet, coverage is the large baseline constant so the first hub
// captures the highest-demand cluster.
func buildCoverage(sources []GoodSource, homedPositions []hubPosition, baseline float64) map[string]float64 {
	coverage := make(map[string]float64, len(sources))
	for _, s := range sources {
		if len(homedPositions) == 0 {
			coverage[s.Good] = baseline
			continue
		}
		best := math.MaxFloat64
		for _, h := range homedPositions {
			if d := euclid(h.X, h.Y, s.X, s.Y); d < best {
				best = d
			}
		}
		coverage[s.Good] = best
	}
	return coverage
}

// hubMarginal is a candidate hub's SCORE: the marginal payment-weighted buy-leg it eliminates
// given the current homes —
//
//	marginal(C) = Σ_G  w_G × max(0, coverage_G − dist(C, S_G))
//
// This is a greedy max-coverage / facility-location value that BAKES IN the geometry with no
// special-casing: a source cluster already served by a home contributes ~0 (coverage_G is already
// small), so a 2nd central hub self-limits; an outlier whose source no hub is near contributes its
// full weighted gain, so it scores high. A naive raw-payment ranker would over-provision the
// central cluster — this avoids that.
func hubMarginal(c HubCandidate, weights map[string]float64, sources []GoodSource, coverage map[string]float64) float64 {
	sum := 0.0
	for _, s := range sources {
		w := weights[s.Good]
		if w <= 0 {
			continue
		}
		if gain := coverage[s.Good] - euclid(c.X, c.Y, s.X, s.Y); gain > 0 {
			sum += w * gain
		}
	}
	return sum
}

// planPlacements assigns each new / idle-unhomed hauler to argmax_C marginal(C | current homes),
// subject to the per-hub concentration cap, GREEDILY: after each placement the chosen hub's
// position joins the homed set and its per-hub count increments, so the next hauler sees it (the
// facility-location self-limiting that spreads the fleet across demand clusters).
//
// A hauler is placed only at a hub with strictly-positive marginal (never homed where it would add
// no coverage value — this is what stops the whole fleet clumping on one already-covered hub). A
// hub at the concentration cap is excluded outright (the hard-ceiling guardrail). If no eligible,
// positive-marginal hub remains, the hauler is left unhomed this tick (a spare with no marginal
// contribution simply waits — a deferral, never a strand).
func (h *RunContractHubCoordinatorHandler) planPlacements(
	cfg contractHubRunConfig,
	scan HubScan,
	weights map[string]float64,
	homedPositions []hubPosition,
	hubCounts map[string]int,
	toPlace []HaulerHome,
) []hubPlacement {
	// Working copies so the caller's inputs are not mutated across the greedy loop.
	positions := append([]hubPosition(nil), homedPositions...)
	counts := make(map[string]int, len(hubCounts))
	for k, v := range hubCounts {
		counts[k] = v
	}

	var out []hubPlacement
	for _, hauler := range toPlace {
		coverage := buildCoverage(scan.Sources, positions, cfg.BaselineCoverage)

		bestIdx := -1
		bestMarginal := 0.0 // strictly-positive required: 0 is "adds nothing", not a placement
		for i, cand := range scan.Candidates {
			if counts[cand.Waypoint] >= cfg.MaxHaulersPerHub {
				continue // concentration cap: hub is full
			}
			m := hubMarginal(cand, weights, scan.Sources, coverage)
			if m > bestMarginal || (bestIdx >= 0 && m == bestMarginal && cand.Waypoint < scan.Candidates[bestIdx].Waypoint) {
				bestMarginal = m
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			continue // no eligible, positive-marginal hub — leave this hauler unhomed this tick
		}

		chosen := scan.Candidates[bestIdx]
		out = append(out, hubPlacement{ShipSymbol: hauler.ShipSymbol, HubWaypoint: chosen.Waypoint})
		positions = append(positions, hubPosition{X: chosen.X, Y: chosen.Y})
		counts[chosen.Waypoint]++
	}

	// Deterministic order for logging/metrics (placement decisions are independent of this order).
	sort.SliceStable(out, func(i, j int) bool { return out[i].ShipSymbol < out[j].ShipSymbol })
	return out
}

// euclid is the in-system Euclidean distance between two positions (mirrors shared.Waypoint's
// DistanceTo; the engine works in raw coordinates so it needs no Waypoint value objects).
func euclid(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}
