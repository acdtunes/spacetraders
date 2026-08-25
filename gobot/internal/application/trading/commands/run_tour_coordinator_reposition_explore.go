package commands

// First-visit exploration for the reposition pre-flight.
//
// Every relocation trigger picks which grounds to price from ONE ranking: cached in-system
// capped spread, decayed per gate hop. That key is a pure function of cached prices, so from a
// given origin it names the same grounds on every attempt — a ground just outside the bounded
// window is never priced, so the fleet never learns it was worth flying to, so it stays
// outside. The tie sweep covers only the regime where the window's edge falls inside a run of
// EQUAL scores; with real prices it never fires.
//
// So one extra planner call goes to the ground the coordinator has gone longest without
// pricing, appended AFTER the window the score decided — nothing is removed, nothing is
// re-ordered, and nothing is scored off a ground that was not actually priced. An admitted
// ground still clears the same pre-flight, relocation floor and rate ranking as every other
// candidate (RULINGS #4): the slot buys a LOOK, never a verdict. The evidence bars keep the
// call off a ground that cannot support a tour at all, and both fail toward today's behaviour.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The exploration slot's two evidence bars, live-tunable via `spacetraders tune --operation
// tour <key> N`.
const (
	TuneKeyExploreMinFreshListings = "explore_min_fresh_listings"
	TuneKeyExploreMinTradeVolume   = "explore_min_trade_volume"
)

const (
	// defaultExploreMinFreshListings is the documented default of
	// TuneKeyExploreMinFreshListings. A tour needs a source, a sink and something to carry, so
	// a ground quoting fewer rows than one market's board holds cannot build one however wide
	// its headline spread. Deliberately low: admitting a mediocre ground costs one pre-flight
	// the solver declines, while excluding a good one leaves it invisible indefinitely.
	defaultExploreMinFreshListings = 6

	// defaultExploreMinTradeVolume is the documented default of TuneKeyExploreMinTradeVolume.
	// A market that absorbs only a token tranche cannot pay for a crossing whatever the spread,
	// so depth — not price — is what makes an unproven ground worth the look.
	defaultExploreMinTradeVolume = 20
)

// exploreMinFreshListings resolves the live listing-breadth bar, falling back to the
// documented default whenever the tune surface has nothing positive to say.
func (h *RunTourCoordinatorHandler) exploreMinFreshListings(ctx context.Context, playerID int) int {
	if tuned, ok := h.freshness.TunedInt(ctx, playerID, TuneKeyExploreMinFreshListings); ok {
		return tuned
	}
	return defaultExploreMinFreshListings
}

// exploreMinTradeVolume resolves the live absorption bar, falling back to the documented
// default whenever the tune surface has nothing positive to say.
func (h *RunTourCoordinatorHandler) exploreMinTradeVolume(ctx context.Context, playerID int) int {
	if tuned, ok := h.freshness.TunedInt(ctx, playerID, TuneKeyExploreMinTradeVolume); ok {
		return tuned
	}
	return defaultExploreMinTradeVolume
}

// notePricedGround records that the solver has just priced a candidate ground, advancing the
// sweep so the next exploration slot goes to a colder one. It is called from planAtCandidate —
// the ONE place a candidate ground is actually priced — so all three relocation triggers feed
// one ledger and none has to remember to. A restart begins the sweep again, which re-explores
// rather than skips.
func (h *RunTourCoordinatorHandler) notePricedGround(system string) {
	if system == "" {
		return
	}
	h.exploreMu.Lock()
	defer h.exploreMu.Unlock()
	if h.explorePriced == nil {
		h.explorePriced = make(map[string]uint64)
	}
	h.exploreSeq++
	h.explorePriced[system] = h.exploreSeq
}

// explorePricedAt reports where a ground sits in the sweep, and whether it has been priced at
// all — an unpriced ground being the coldest thing there is.
func (h *RunTourCoordinatorHandler) explorePricedAt(system string) (uint64, bool) {
	h.exploreMu.Lock()
	defer h.exploreMu.Unlock()
	at, priced := h.explorePriced[system]
	return at, priced
}

// exploreEligible reports whether a candidate's OBSERVABLE data is evidence enough to spend
// the slot on it. Both bars must hold, so an unstamped candidate from a discovery path that
// carries no observables clears neither and stays where the pre-rank put it.
func exploreEligible(candidate repositionCandidate, minListings, minVolume int) bool {
	return candidate.freshLanes >= minListings && candidate.tradeVolume >= minVolume
}

// admitExplorationCandidate splices the coldest eligible candidate from OUTSIDE the top-K
// window in at the window's edge and widens k by one, returning the pre-flight slice and the
// bound its caller should honour. Inserting AFTER the window rather than into it is what
// preserves every preference the score expressed; everything else keeps its rank order behind.
// Nothing outside the window, or nothing out there worth the call, returns the input untouched.
func (h *RunTourCoordinatorHandler) admitExplorationCandidate(ctx context.Context, cmd *RunTourCoordinatorCommand, candidates []repositionCandidate, k int) ([]repositionCandidate, int) {
	if k <= 0 || k >= len(candidates) {
		return candidates, k
	}
	minListings := h.exploreMinFreshListings(ctx, cmd.PlayerID)
	minVolume := h.exploreMinTradeVolume(ctx, cmd.PlayerID)

	pick := -1
	var pickAt uint64
	pickUnpriced := false
	for i := k; i < len(candidates); i++ {
		if !exploreEligible(candidates[i], minListings, minVolume) {
			continue
		}
		at, priced := h.explorePricedAt(candidates[i].system)
		switch {
		case pick < 0:
		case pickUnpriced:
			continue // nothing beats a ground never priced; the first such in rank order wins
		case priced && at >= pickAt:
			continue
		}
		pick, pickAt, pickUnpriced = i, at, !priced
	}
	if pick < 0 {
		return candidates, k
	}

	admitted := candidates[pick]
	widened := make([]repositionCandidate, 0, len(candidates))
	widened = append(widened, candidates[:k]...)
	widened = append(widened, admitted)
	for i := k; i < len(candidates); i++ {
		if i != pick {
			widened = append(widened, candidates[i])
		}
	}
	logExplorationAdmitted(ctx, cmd, admitted, k, len(candidates), pickUnpriced)
	return widened, k + 1
}

// logExplorationAdmitted names the ground the slot bought a look at, in the MESSAGE TEXT
// (which `container logs` keeps even though it drops the structured metadata map), so a fleet
// reaching new ground is greppable rather than only visible in a distinct-markets count weeks on.
func logExplorationAdmitted(ctx context.Context, cmd *RunTourCoordinatorCommand, admitted repositionCandidate, k, total int, unpriced bool) {
	standing := "coldest priced"
	if unpriced {
		standing = "never priced"
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Reposition exploration: the top-%d cut is decided by cached spread alone and re-asks the same grounds, so %s (%s, %d hop(s), prerank %d, %d fresh row(s), depth %d) gets one pre-flight of its own out of %d candidate(s)", k, admitted.system, standing, repositionChargedHops(admitted), admitted.score, admitted.freshLanes, admitted.tradeVolume, total), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "candidate": admitted.system, "gate_hops": repositionChargedHops(admitted),
		"prerank": admitted.score, "fresh_lanes": admitted.freshLanes, "trade_volume": admitted.tradeVolume,
		"candidates": total, "never_priced": unpriced, "trigger": "reposition_exploration",
	})
}
