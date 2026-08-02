package contract

import (
	"time"
)

// IdleArbConfig parametrizes the dispatcher (RULINGS #5: operational values
// are config, not constants — these flow from the coordinator's persisted
// launch config, with the defaults below when unset).
type IdleArbConfig struct {
	// ReserveHulls is the number of idle dedicated hulls the dispatcher must
	// always leave unclaimed for instant contract claims. The serial pipeline
	// needs at most one hull at a time, so 1 preserves the zero-latency bound.
	ReserveHulls int
	// HubRadius is the maximum in-system distance (distance units) from the
	// hull's current waypoint to the leg's sell market. Bounds both leg
	// duration and how far a hull can drift from its hub. This is the OUTER
	// hub-local filter; LeashRadius (below) is the tighter money-guard leash.
	HubRadius float64
	// LeashRadius is the formal money-guard leash: the maximum distance
	// (distance units) from the home hub a leg's sell market may sit. Legs
	// naturally max ~52u, so 80 formalizes that boundary with headroom;
	// tighter than HubRadius, it is the binding radius in practice. A candidate
	// beyond it is skipped (leash counter), never dispatched.
	LeashRadius float64
	// MaxLegDuration caps a leg's projected one-way flight time to the sell
	// market (CRUISE estimate from the hull's engine speed). It bites where
	// LeashRadius does not: a slow hull whose in-radius leg still projects
	// longer than this is skipped (leash counter).
	MaxLegDuration time.Duration
	// MaxSpendPerLeg caps each leg's buy (the arb run's --max-spend guard).
	MaxSpendPerLeg int
	// MinMarginPerUnit is the absolute per-unit floor handed to the arb run's
	// margin gate (which re-reads live prices and fails closed).
	MinMarginPerUnit int
	// MarginVerifyFraction is the RELATIVE per-unit floor: a leg's effective
	// MinMargin is raised to ceil(MarginVerifyFraction × quoted margin), so the
	// arb run's existing live-verify gate aborts fail-closed unless the live
	// margin holds ≥ this fraction of the cached quote. 0.80 = tolerate at most
	// a 20% margin slip between quote and live.
	MarginVerifyFraction float64
	// Blacklist is the config-driven excluded-goods list checked at dispatch: a
	// leg is never dispatched on a listed good. Nil → the package default
	// (ELECTRONICS); an explicit empty list disables the blacklist. The
	// captain flips a good back by editing config and restarting (no code
	// redeploy). RULINGS #5.
	Blacklist []string
	// StandbyStations are the operator's standby waypoint symbols — the SAME
	// --standby-stations set the contract coordinator's contract-handoff
	// homing uses. The post-leg re-home (rehomeDriftedHulls) sends any idle
	// dedicated hull NOT sitting at one of these back to its balanced standby
	// station, so an arb leg that ends off-station doesn't dead-idle at the
	// sell waypoint. Empty (or a nil homer) disables re-homing entirely,
	// mirroring HomeShipCommand's own "empty stations = no relocation" contract.
	// RULINGS #5: the tunable is the station set, already an operator flag.
	StandbyStations []string
	// Interval is the dispatch tick.
	Interval time.Duration
	// RecoveryHold is the lane mutex's post-termination hold: after a leg on a
	// (good, sink) lane terminates, the dispatcher keeps that lane closed for
	// this long before another hull may work it, so back-to-back passes never
	// re-dump a sink the last leg just depressed. In-flight legs block their
	// lane regardless of this value; it only spaces SEQUENTIAL legs on one
	// sink. See laneMutex for why a flat hold (not the routing service's
	// recovery model) is deliberate, and how it cites the model's half-lives.
	RecoveryHold time.Duration

	// Per-trip live-profitability floor: the dispatcher launches one arb leg
	// (one buy->sell round trip) per lane per pass, RE-PRICED every pass from
	// the freshly-read ask/bid (never a cached spread). A lane clears only
	// when, at current live prices,
	//   net_per_u = (sink bid − hub ask) − FuelCostPerUnit
	// meets the BINDING floor: max(MinNetProfitPerUnit, ceil(NetProfitFraction ×
	// hub ask)). Re-pricing matters because the fleet's OWN repeated buys walk a
	// thin EXPORT price up (and its dumps walk the IMPORT price down), so a good
	// that quoted a healthy spread can degrade trip-over-trip; re-pricing every
	// pass catches it and the lane AUTO-RE-ENTERS when the price recovers.
	// GENERIC — no per-good knowledge; the floors are tunable config (RULINGS
	// #5), never a hardcoded good list.

	// MinNetProfitPerUnit is the ABSOLUTE after-fuel net floor a lane must clear.
	MinNetProfitPerUnit int
	// NetProfitFraction is the RELATIVE floor: a lane's net must also be at least
	// this fraction of the hub ask. It is what stops a HIGH-PRICED good with a thin
	// absolute spread (e.g. buy 5000/u, +265 net) from clearing on the flat floor
	// alone — the flat floor governs cheap goods, this one governs expensive ones,
	// and the binding (larger) of the two applies.
	NetProfitFraction float64
	// FuelCostPerUnit is the per-cargo-unit fuel estimate subtracted from the gross
	// spread to get net. It is a flat hub-local estimate (the leash bounds every leg
	// to a short, similar hop, so a flat figure is honest here); ~35/u on central
	// lanes, a captain whose lanes differ retunes it. The within-trip price ladder is
	// guarded downstream by the arb run's live per-tranche buy ceiling / sell floor —
	// this floor is the CROSS-trip decision: should this lane be flown AT ALL this
	// pass, at current live prices.
	FuelCostPerUnit int
}

// Idle-arb defaults. HubRadius 250 is the loose outer hub-local filter;
// LeashRadius 80 is the tight money-guard leash (legs naturally max ~52u, so
// 80 formalizes that boundary with headroom) and the 8-minute cap catches
// slow hulls the radius alone would not; spend 100k/leg × ≤5 concurrent legs
// bounds exposure at ~500k against a multi-million treasury, ahead of the arb
// run's own non-tunable working-capital floor. MinMargin 1 is the ABSOLUTE
// floor; the capital protection is the RELATIVE MarginVerifyFraction (0.80) —
// a flat floor of 1 lets the live-verify gate pass a leg whose quoted margin
// has nearly collapsed, so the 80%-of-quote floor gives the gate teeth to
// abort those pre-buy.
const (
	DefaultIdleArbReserveHulls         = 1
	DefaultIdleArbHubRadius            = 250.0
	DefaultIdleArbLeashRadius          = 80.0
	DefaultIdleArbMaxLegDuration       = 8 * time.Minute
	DefaultIdleArbMaxSpend             = 100_000
	DefaultIdleArbMinMargin            = 1
	DefaultIdleArbMarginVerifyFraction = 0.80
	DefaultIdleArbInterval             = 90 * time.Second
	// DefaultIdleArbRecoveryHold is the lane mutex's post-termination hold,
	// deliberately shorter than any modelled recovery half-life: it does not
	// claim full recovery, only that a sink is not re-dumped back-to-back. The
	// in-flight lane block and the per-tranche sell floor carry the rest of the
	// defense; a captain wanting the fuller modelled hold raises the config
	// knob with no code change.
	DefaultIdleArbRecoveryHold = 20 * time.Minute
	// Per-trip profitability floor defaults: 100/u absolute after fuel (fuel
	// runs ~35/u on central lanes) and 20% of the buy price. The relative
	// floor stops a high-priced good with a thin absolute spread from sneaking
	// through on the flat floor alone.
	DefaultIdleArbMinNetProfit      = 100
	DefaultIdleArbNetProfitFraction = 0.20
	DefaultIdleArbFuelCostPerUnit   = 35
)

// DefaultIdleArbBlacklist is the initial excluded-goods list — ELECTRONICS
// proved too volatile for the live-verify margin floor to safely price. A nil
// IdleArbConfig.Blacklist takes this; an explicit empty list disables the
// blacklist entirely.
var DefaultIdleArbBlacklist = []string{"ELECTRONICS"}

// WithDefaults fills zero-valued fields with the package defaults.
func (c IdleArbConfig) WithDefaults() IdleArbConfig {
	if c.ReserveHulls <= 0 {
		c.ReserveHulls = DefaultIdleArbReserveHulls
	}
	if c.HubRadius <= 0 {
		c.HubRadius = DefaultIdleArbHubRadius
	}
	if c.LeashRadius <= 0 {
		c.LeashRadius = DefaultIdleArbLeashRadius
	}
	if c.MaxLegDuration <= 0 {
		c.MaxLegDuration = DefaultIdleArbMaxLegDuration
	}
	if c.MaxSpendPerLeg <= 0 {
		c.MaxSpendPerLeg = DefaultIdleArbMaxSpend
	}
	if c.MinMarginPerUnit <= 0 {
		c.MinMarginPerUnit = DefaultIdleArbMinMargin
	}
	if c.MarginVerifyFraction <= 0 {
		c.MarginVerifyFraction = DefaultIdleArbMarginVerifyFraction
	}
	// nil → default blacklist; an explicit empty (non-nil) list is preserved so
	// a config whitelist-flip genuinely disables the blacklist without code.
	if c.Blacklist == nil {
		c.Blacklist = DefaultIdleArbBlacklist
	}
	if c.Interval <= 0 {
		c.Interval = DefaultIdleArbInterval
	}
	if c.RecoveryHold <= 0 {
		c.RecoveryHold = DefaultIdleArbRecoveryHold
	}
	// The per-trip profitability floor is DEFAULT-ON — a config that omits it
	// must not silently disable a money guard (RULINGS #4, matching the
	// sibling MarginVerifyFraction/Blacklist defaults). A captain retune sets
	// a non-zero value.
	if c.MinNetProfitPerUnit <= 0 {
		c.MinNetProfitPerUnit = DefaultIdleArbMinNetProfit
	}
	if c.NetProfitFraction <= 0 {
		c.NetProfitFraction = DefaultIdleArbNetProfitFraction
	}
	if c.FuelCostPerUnit <= 0 {
		c.FuelCostPerUnit = DefaultIdleArbFuelCostPerUnit
	}
	return c
}

// IdleArbSpec is one hub-local leg the dispatcher wants flown: the arb-run
// launch parameters plus the claim identity (Operation) the container must use
// so the atomic ClaimShip dedication check passes for the contract fleet's own
// hulls — and keeps rejecting everyone else's.
type IdleArbSpec struct {
	ShipSymbol string
	Good       string
	BuyAt      string // the hull's CURRENT waypoint (arb location guard enforces this)
	SellAt     string
	MaxSpend   int
	MinMargin  int
	PlayerID   int
	Operation  string // claim identity, e.g. "contract"
	// SellFloorFraction arms the arb run's per-tranche sell floor: each sell
	// tranche aborts the remainder when the LIVE bid falls below this fraction
	// of the quoted bid. It reuses the SAME 80% knob the buy-side live-verify uses
	// (cfg.MarginVerifyFraction), so a captain retune moves both floors together.
	// 0 → the arb run's own default (defaultArbSellFloorFraction).
	SellFloorFraction float64
}
