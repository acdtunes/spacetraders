package common

// ReserveFloorGate is the fail-closed working-capital reserve bound SHARED by every engine
// that dispatches spend against a live treasury: idle-arb's per-pass reserve gate (sp-zq635
// §4a) and the long-haul money envelope's cushion fence (sp-mepj §3). Built once from a
// single live-treasury read, it reports when committing one more nextLegSpend-credit spend,
// on top of what has already been committed, would drop treasury below Floor.
//
// ONE copy of the treasury-floor comparison, parametrized by Floor, so both callers share
// it instead of each carrying their own: idle-arb builds it with Floor = ImmutableReserveFloor
// (the flat 50k), long-haul with Floor = ContractScalerCushion (the 200k contract-cushion
// fence — long-haul capital never dips into contract working capital). RULINGS #4: the
// mechanism fails CLOSED and is never weakened; only the Floor value differs per caller.
type ReserveFloorGate struct {
	Active     bool  // a treasury reader is wired (inert — never holds — when false)
	Unreadable bool  // the live read failed → fail closed (Holds everything)
	Treasury   int64 // live balance at the read
	Floor      int64 // the reserve the spend must leave intact
}

// Holds reports whether committing one more nextLegSpend-credit spend, on top of committed,
// would drop treasury below Floor — i.e. whether the spend must be HELD. Inert (never holds)
// when no reader is wired; ALWAYS holds when the treasury was unreadable (fail-closed — a
// guard whose job is protecting the reserve must never spend blind, RULINGS #4).
func (g ReserveFloorGate) Holds(committed, nextLegSpend int64) bool {
	if !g.Active {
		return false
	}
	if g.Unreadable {
		return true
	}
	return g.Treasury-committed-nextLegSpend < g.Floor
}
