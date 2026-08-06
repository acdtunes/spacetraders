package trading

import (
	"math"
	"sync"
	"time"
)

// LaneKey identifies one arbitrage lane for the shared cooldown ledger: the ordered
// (buy-market, sell-market, good) triple a circuit trades. It is a comparable value so
// it keys the ledger map directly.
type LaneKey struct {
	Source string // buy here (ArbitrageLane.SourceWaypoint)
	Dest   string // sell here (ArbitrageLane.DestWaypoint)
	Good   string
}

// ActivityStrong is the market activity tier whose price recovery was measured fast enough to
// warrant its own decay rate.
const ActivityStrong = "STRONG"

// strongToRestrictedHalfLifeRatio scales tau for a STRONG market. Recovery half-lives were fitted
// per activity tier and differ by roughly this much; tau already matches the slower tier, so a
// STRONG market's constant is that one scaled down. A ratio rather than a second absolute constant
// so refitting tau carries to both tiers together.
const strongToRestrictedHalfLifeRatio = 1.7 / 9.0

// LaneCooldownLedger is the shared, decaying, per-lane compression ledger. When a
// hull trades U units on a lane it ADDS compression debt (buyImpact+sellImpact)·(U/tv),
// timestamped; the debt DECAYS as exp(-dt/tau). The ranker SUBTRACTS the live decayed
// debt from a lane's expected spread, so once the fleet hammers a lane it stays
// down-weighted for HOURS (tau≈750min) and hulls ROTATE to fresh lanes instead of
// re-discovering the compression the expensive way (each wasted leg burns API budget
// + travel).
//
// It is SHARED across the fleet: every hull's trade Accrues, every rank Debt-reads,
// keyed by lane. The state is an in-memory decaying map — a daemon restart simply
// forgets recent compression, which self-heals (the debt would have decayed away over
// the next tau anyway). Safe for concurrent hulls: a mutex guards the map.
//
// Debt is stored as a compression FRACTION (dimensionless, in price-fraction units the
// same way the impact coefficients are), not credits — the ranker converts it to
// per-unit spread credits at read time against the lane's live mid-price, keeping this
// domain type price-agnostic.
type LaneCooldownLedger struct {
	mu         sync.Mutex
	buyImpact  float64
	sellImpact float64
	tau        time.Duration
	entries    map[LaneKey]cooldownEntry
	// activity resolves a market's observed activity tier, for the decay rate. Nil leaves every
	// key on tau, which is what every caller that does not wire it sees.
	activity func(waypoint, good string) (string, bool)
}

// cooldownEntry is one lane's accumulated compression debt as of a timestamp. Storing a
// single decayed scalar per lane (rather than a list of timestamped trades) keeps the
// ledger O(1) per lane: on each Accrue the prior debt is first decayed forward to now,
// then the new trade's debt added — mathematically identical to summing independently
// decayed per-trade entries because exp(-Δ/τ) composes multiplicatively.
type cooldownEntry struct {
	debt float64
	at   time.Time
}

// NewLaneCooldownLedger builds the shared ledger with the era-3 impact coefficients and
// decay constant. A non-positive argument resolves to its documented era-3 default, so a
// caller can pass zero for any knob it hasn't refit.
func NewLaneCooldownLedger(buyImpact, sellImpact float64, tau time.Duration) *LaneCooldownLedger {
	if buyImpact <= 0 {
		buyImpact = DefaultBuyImpactCoefficient
	}
	if sellImpact <= 0 {
		sellImpact = DefaultSellImpactCoefficient
	}
	if tau <= 0 {
		tau = DefaultCooldownTau
	}
	return &LaneCooldownLedger{
		buyImpact:  buyImpact,
		sellImpact: sellImpact,
		tau:        tau,
		entries:    make(map[LaneKey]cooldownEntry),
	}
}

// Accrue records that a hull traded `units` against a market of `tradeVolume` on the
// lane, adding (buyImpact+sellImpact)·(units/tradeVolume) compression debt as of `now`.
// A non-positive units or tradeVolume is a no-op (fail-safe: nothing to compress, and
// never a divide-by-zero on a zero-volume market).
func (l *LaneCooldownLedger) Accrue(key LaneKey, units, tradeVolume int, now time.Time) {
	if units <= 0 || tradeVolume <= 0 {
		return
	}
	x := float64(units) / float64(tradeVolume)
	addend := (l.buyImpact + l.sellImpact) * x

	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[key] = cooldownEntry{
		debt: l.decayLocked(key, now) + addend,
		at:   now,
	}
}

// SourceDrainKey is the key for compression measured against a SOURCE MARKET alone, with no
// destination. A buyer pacing itself against a market it is draining cares only about how much it
// has taken OUT of that market: the same source commonly feeds several destinations (one exporter
// to four or five importers is ordinary), so a per-destination key splits one market's drain into a
// bucket per destination, each reading under any bound while the market is drained several times as
// fast.
//
// It shares the ledger with the full-lane keys rather than living in a second one, and cannot
// collide with them: every real lane names a destination, and this names none.
func SourceDrainKey(source, good string) LaneKey {
	return LaneKey{Source: source, Good: good}
}

// TrancheDebt is the debt ONE full trade-volume purchase adds — the model's own unit of
// compression, in the same dimensionless fraction Debt returns.
//
// It exists so a consumer pacing its purchases has a bound to compare against without
// inventing a number: "this source still carries more than a single tranche's worth of
// impact that has not decayed" is a statement in the model's own terms, and it moves with
// the era coefficients rather than drifting apart from them.
func (l *LaneCooldownLedger) TrancheDebt() float64 {
	return l.buyImpact + l.sellImpact
}

// Debt returns the lane's live compression debt decayed forward to `now`: the stored
// debt times exp(-dt/tau). An untracked lane (never traded, or fully decayed and
// pruned) returns 0. Read-only — it does not mutate the entry.
func (l *LaneCooldownLedger) Debt(key LaneKey, now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.decayLocked(key, now)
}

// decayLocked computes the lane's debt decayed to `now`. Caller holds l.mu. A
// non-positive dt (same instant, or clock skew) returns the stored debt undecayed —
// decay only ever shrinks debt, never grows it.
func (l *LaneCooldownLedger) decayLocked(key LaneKey, now time.Time) float64 {
	entry, ok := l.entries[key]
	if !ok {
		return 0
	}
	dt := now.Sub(entry.at)
	if dt <= 0 {
		return entry.debt
	}
	return entry.debt * math.Exp(-float64(dt)/float64(l.tauFor(key)))
}

// tauFor picks a key's decay constant from the observed activity of the market it names.
//
// A market under STRONG activity sheds a price move several times faster than one under
// RESTRICTED, so a single constant is either far too slow for one or far too fast for the other.
// Too slow only withholds buys, which is why the single constant was safe to ship and why the
// faster rate is the only direction this may move.
//
// ONLY A KEY THAT NAMES ONE MARKET. A source-drain key names a single market, so its activity is
// unambiguous. A full lane names two, and the recovery rates were fitted per market — there is no
// measurement of what a two-ended lane decays at, so those keep tau and every lane ranking that
// reads them is unchanged.
//
// UNKNOWN IS THE SLOW RATE, never the fast one: no resolver, an unreadable market, or any tier
// other than the one measured fast all decay at tau. An activity this cannot read must not be
// treated as one that has already recovered.
func (l *LaneCooldownLedger) tauFor(key LaneKey) time.Duration {
	if l.activity == nil || key.Dest != "" {
		return l.tau
	}
	activity, ok := l.activity(key.Source, key.Good)
	if !ok || activity != ActivityStrong {
		return l.tau
	}
	return l.strongTau()
}

// strongTau is the decay constant for a STRONG market, scaled from tau by the ratio of the two
// measured half-lives so a refit of tau carries to both tiers together.
func (l *LaneCooldownLedger) strongTau() time.Duration {
	return time.Duration(float64(l.tau) * strongToRestrictedHalfLifeRatio)
}

// SetActivityResolver wires the observed-activity lookup the decay rate reads. Activity is an
// OBSERVABLE, not a knob: it does not track our own buying, so this only ever reads it to choose a
// rate and never assumes a trade can induce a tier. Nil (the default) leaves every key on tau.
func (l *LaneCooldownLedger) SetActivityResolver(resolve func(waypoint, good string) (string, bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.activity = resolve
}
