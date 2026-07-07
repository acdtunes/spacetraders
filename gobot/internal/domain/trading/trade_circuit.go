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
)

// MarginAlive reports whether a destination bid still clears the bid-floor over
// basis, where basis is the per-unit acquisition cost (the source ask we paid).
// The circuit loops while this holds and stops the moment it fails.
func MarginAlive(bid, basis int) bool {
	return bid >= basis+MinBidMargin
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
