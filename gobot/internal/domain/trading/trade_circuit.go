package trading

// Trade-analyst arbitrage discipline (cited on sp-s7c2). Two caps keep a
// hand-flown circuit from walking its own edge to zero:
//
//   - TrancheCap bounds how many units to move per market visit. Small tranches
//     keep each fill near the top of the book; a big fill walks the price down.
//   - MinBidMargin is the bid-floor: the lane's edge is alive only while the
//     destination bid stays at least this far above basis (the per-unit
//     acquisition cost). Once bid < basis + MinBidMargin, the margin is dead —
//     stop trading rather than grind the spread to nothing.
const (
	TrancheCap   = 18
	MinBidMargin = 1000

	// StaleAskMovePercent bounds how far the source ask may have drifted, as a
	// percentage of the basis the lane was RANKED on, before the circuit refuses to
	// buy. Lanes are ranked from a market cache that can be many minutes stale; if a
	// live re-read shows the ask has run away from that basis, the ranked spread is
	// fiction and buying on it can realise a large loss (a -196k manual precedent).
	// 30% tolerates ordinary tick drift while catching a basis that has moved (e.g. a
	// 3.6x ask jump). The move is measured in EITHER direction: a large swing either
	// way means the ranking premise is stale and the run should re-scan, not execute.
	StaleAskMovePercent = 30
)

// MarginAlive reports whether a destination bid still clears the bid-floor over
// basis, where basis is the per-unit acquisition cost (the source ask we paid).
// The circuit loops while this holds and stops the moment it fails.
func MarginAlive(bid, basis int) bool {
	return bid >= basis+MinBidMargin
}

// AskMovedBeyondTolerance reports whether a freshly-read source ask has drifted
// more than StaleAskMovePercent from the basis the lane was ranked on, in either
// direction. A degenerate basis (<= 0) never trips the guard — there is nothing
// meaningful to compare against. Integer math avoids float rounding at the boundary:
// the move trips strictly ABOVE the tolerance (exactly StaleAskMovePercent is allowed).
func AskMovedBeyondTolerance(liveAsk, rankedBasis int) bool {
	if rankedBasis <= 0 {
		return false
	}
	diff := liveAsk - rankedBasis
	if diff < 0 {
		diff = -diff
	}
	return diff*100 > rankedBasis*StaleAskMovePercent
}

// VisitTranche returns how many units to move in a single market visit, bounded
// by the per-visit tranche cap, the market's tradable volume this tick, and the
// ship's remaining usable capacity (cargo space when buying, units held when
// selling). Returns 0 when any bound is 0 or negative, so a spent circuit halts.
func VisitTranche(marketVolume, capacity int) int {
	tranche := TrancheCap
	if marketVolume < tranche {
		tranche = marketVolume
	}
	if capacity < tranche {
		tranche = capacity
	}
	if tranche < 0 {
		return 0
	}
	return tranche
}
