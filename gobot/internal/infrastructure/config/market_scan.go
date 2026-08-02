package config

// market_scan.go carries the fleet's ONE market-scan budget: the total
// request rate every market reader in the daemon shares, and how much more
// attention the most valuable market may earn than the least.
//
// It is config rather than a container tunable because the scanner it governs is
// built once in the composition root and shared by every coordinator container —
// a per-container knob would multiply by the container count and stop being a
// budget. Absent section resolves to the armed defaults, so the budget is
// enforced whether or not anyone writes a config.yaml stanza.

// MarketScanConfig holds the market-scan budget's two knobs.
type MarketScanConfig struct {
	// BudgetReqPerSec is the TOTAL market-read rate shared by every reader in the
	// daemon: the parked sensing rotation, tour and trade arrival scans, the
	// scout tour, manufacturing and contract market lookups, charting, and the
	// pre-commit money guards.
	//
	// It is deliberately a constant with respect to the charted map. Each market's
	// scan interval is derived as budget ÷ markets known, so charting more systems
	// lengthens intervals instead of raising traffic — which is what makes the
	// server's unraisable 2.00 req/s ceiling survivable permanently rather than
	// binding earlier every era.
	//
	// Raise it when Forced overdrafts are persistently high (the fleet's
	// unavoidable pre-commit verification exceeds the allowance) — never by
	// weakening the guards. 0 → the armed default.
	BudgetReqPerSec float64 `mapstructure:"budget_req_per_sec"`

	// ValueClampR is the ceiling on how much more scan attention the hottest
	// market may earn than the coldest, by observed relative bid-ask spread. It
	// mirrors the parked rotation's own value_clamp_r so both allocate attention
	// on one scale.
	//
	// It is also the anti-starvation constant: no known market goes unscanned for
	// longer than ValueClampR × marketsKnown ÷ budget, because that is the
	// coldest admissible market's interval on the least favourable weighting.
	// Raising it sharpens the preference for hot markets AND lengthens the
	// worst-case staleness in the same proportion. 1 flattens the weighting
	// entirely; 0 → the armed default.
	ValueClampR int `mapstructure:"value_clamp_r"`
}

// Market-scan budget defaults (config package locals: they size a request
// allowance, not a domain quantity).
//
// 0.35 req/s is sized from the era-5 measurement the budget was written for —
// total market reads of 0.644 req/s against a 2.00 req/s ceiling — and cuts the
// measured draw by roughly 46% while leaving sensing a meaningful share. 8 matches
// the parked rotation's clamp.
const (
	defaultMarketScanBudgetReqPerSec = 0.35
	defaultMarketScanValueClampR     = 8
)

// ResolvedBudgetReqPerSec returns the configured budget or the armed default.
// Note there is no value that disables pacing: a non-positive setting resolves to
// the default rather than to "unlimited", because an unpaced scanner is the defect
// this budget exists to fix.
func (c MarketScanConfig) ResolvedBudgetReqPerSec() float64 {
	if c.BudgetReqPerSec > 0 {
		return c.BudgetReqPerSec
	}
	return defaultMarketScanBudgetReqPerSec
}

// ResolvedValueClampR returns the configured clamp or the armed default. 1 is a
// legitimate setting (flat weighting) and is honoured; only a non-positive value
// falls back.
func (c MarketScanConfig) ResolvedValueClampR() int {
	if c.ValueClampR > 0 {
		return c.ValueClampR
	}
	return defaultMarketScanValueClampR
}
