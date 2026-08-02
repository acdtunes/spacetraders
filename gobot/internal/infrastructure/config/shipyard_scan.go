package config

// shipyard_scan.go carries the fleet's ONE shipyard-read budget: the
// total request rate every shipyard reader in the daemon shares, and how much more
// attention the yards the fleet is buying from may earn than the ones it is not.
//
// It is the sibling of market_scan.go beside it and exists for the same reason: the
// scanner it governs is built once in the composition root and shared by every
// coordinator container, so a per-container knob would multiply by the container
// count and stop being a budget. An absent section resolves to the armed defaults,
// so the budget is enforced whether or not anyone writes a config stanza.

// ShipyardScanConfig holds the shipyard-read budget's two knobs.
type ShipyardScanConfig struct {
	// BudgetReqPerSec is the TOTAL shipyard-read rate shared by every reader in
	// the daemon: the parked yard rotation, the scout tour's piggybacked scans,
	// route-executor arrivals, warp charting, the hull search on the construction
	// drain, and every pre-buy price verification.
	//
	// It is deliberately a constant with respect to the charted map. Each yard's
	// read interval is derived as budget ÷ yards known, so charting more systems
	// lengthens intervals instead of raising traffic — which is what makes the
	// server's unraisable 2.00 req/s ceiling survivable permanently rather than
	// binding harder every era.
	//
	// It replaces quartermaster_cadence_secs as the thing that sets the rate. That
	// knob was raised 3600 -> 86400 during an incident to mask shipyard reads
	// measured at 0.844 req/s (44.7% of the whole ceiling), and a cadence floor
	// cannot prioritise: it starved 80 of the 84 yards known to sell
	// SHIP_HEAVY_FREIGHTER while the fleet bought 24 heavies against prices it
	// could no longer see. With this budget in force the cadence returns to being
	// what its own documentation claims — a floor the budget may not scan past.
	//
	// Raise it when Forced overdrafts are persistently high (the fleet's
	// unavoidable pre-buy verification exceeds the allowance) — never by weakening
	// the guards. 0 → the armed default.
	BudgetReqPerSec float64 `mapstructure:"budget_req_per_sec"`

	// ValueClampR is the ceiling on how much more read attention the most valuable
	// yard may earn than a yard selling nothing the fleet wants. It mirrors the
	// market budget's clamp so both allowances express priority on one scale.
	//
	// It is also the anti-starvation constant: no known yard goes unread for
	// longer than ValueClampR × yardsKnown ÷ budget, because that is the coldest
	// admissible yard's interval on the least favourable weighting. Raising it
	// sharpens the preference for yards we are buying from AND lengthens the worst
	// case in the same proportion. 1 flattens the weighting entirely; 0 → the
	// armed default.
	ValueClampR int `mapstructure:"value_clamp_r"`
}

// Shipyard-read budget defaults (config package locals: they size a request
// allowance, not a domain quantity).
//
// 0.12 req/s is sized from the measurement the budget was written for — shipyard
// reads at 0.844 req/s of a 2.00 req/s ceiling — and takes them to roughly 6% of
// the ceiling instead of 44.7%. It is chosen so the incident case clears fast
// rather than merely slower: a backlog of 84 demanded-but-unpriced yards, all at
// the clamp, sweeps in about twelve minutes. 8 matches the market budget's clamp.
const (
	defaultShipyardScanBudgetReqPerSec = 0.12
	defaultShipyardScanValueClampR     = 8
)

// ResolvedBudgetReqPerSec returns the configured budget or the armed default. Note
// there is no value that disables pacing: a non-positive setting resolves to the
// default rather than to "unlimited", because an unpaced shipyard reader is the
// defect this budget exists to fix.
func (c ShipyardScanConfig) ResolvedBudgetReqPerSec() float64 {
	if c.BudgetReqPerSec > 0 {
		return c.BudgetReqPerSec
	}
	return defaultShipyardScanBudgetReqPerSec
}

// ResolvedValueClampR returns the configured clamp or the armed default. 1 is a
// legitimate setting (flat weighting) and is honoured; only a non-positive value
// falls back.
func (c ShipyardScanConfig) ResolvedValueClampR() int {
	if c.ValueClampR > 0 {
		return c.ValueClampR
	}
	return defaultShipyardScanValueClampR
}
