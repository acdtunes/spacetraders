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
	return entry.debt * math.Exp(-float64(dt)/float64(l.tau))
}
