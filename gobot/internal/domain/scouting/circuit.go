package scouting

import (
	"math"
	"time"
)

// circuit.go holds the deterministic scout-post circuit math (sp-k7q5): the
// Admiral's freshness model, freshness-per-market ≈ (markets / hulls) × avgHop,
// where avgHop is the average per-market cost of a tour hop (navigation + scan
// dwell). It is pure math with no I/O so BOTH the undersized-post warning in the
// scout-post coordinator (layer 1, daemon process) and the auto-post proposal
// detector in the watchkeeper (layer 3, a separate process) share one definition
// of "how many probes does this many markets need" rather than each rederiving it.
//
// avgHop and the freshness target are CONFIG at the call sites (RULINGS #5), never
// constants here — this file only turns those inputs into a hull count.

// RequiredHulls is the minimum probe count needed to keep `markets` markets scanned
// within `freshness`, at an average per-market hop cost of avgHop. From the circuit
// model (markets / hulls) × avgHop ≤ freshness, so hulls ≥ markets × avgHop /
// freshness; the result is the ceiling of that ratio, never below 1 when there is at
// least one market. Degenerate inputs (no markets, or a non-positive freshness/avgHop)
// return 0 — a signal the caller reads as "cannot assess", NOT "undersized".
func RequiredHulls(markets int, avgHop, freshness time.Duration) int {
	if markets <= 0 || avgHop <= 0 || freshness <= 0 {
		return 0
	}
	need := float64(markets) * float64(avgHop) / float64(freshness)
	r := int(math.Ceil(need))
	if r < 1 {
		r = 1
	}
	return r
}

// CircuitDuration is the modeled time for ONE hull to complete its share of a post's
// tour: (markets / hulls) × avgHop. It is the observability figure the undersized
// warning reports (the modeled age the post's freshest lane will reach), not a
// decision input. hulls < 1 is treated as 1 — a post always has at least its primary
// slot. Zero markets or a non-positive avgHop yields 0.
func CircuitDuration(markets, hulls int, avgHop time.Duration) time.Duration {
	if markets <= 0 || avgHop <= 0 {
		return 0
	}
	if hulls < 1 {
		hulls = 1
	}
	return time.Duration(float64(markets) / float64(hulls) * float64(avgHop))
}

// IsUndersized reports whether a post touring `markets` markets with `hulls` probes
// cannot keep them within `freshness` under the avgHop circuit model — its modeled
// circuit exceeds the freshness target, so every lane ages past the contract and (past
// the tour planner's cap) goes silently invisible. Equivalent to hulls <
// RequiredHulls(...). A post with no markets or an unusable freshness/avgHop is never
// undersized (there is nothing to assess).
func IsUndersized(markets, hulls int, avgHop, freshness time.Duration) bool {
	req := RequiredHulls(markets, avgHop, freshness)
	if req == 0 {
		return false
	}
	if hulls < 1 {
		hulls = 1
	}
	return hulls < req
}
