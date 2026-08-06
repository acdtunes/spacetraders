package common

// Wave is the regime one tick is in: HEAVY buys trade capacity, PROBE buys sensing coverage.
// It is DERIVED on every tick by every consumer and never stored, so a restart re-derives it
// from durable facts, no cursor can go stale, and there is no re-fire hazard.
type Wave string

const (
	// WaveHeavy pauses probe buying so the treasury can reach a heavy hull's ask.
	WaveHeavy Wave = "heavy"
	// WaveProbe runs probe buying at full speed. It AUTHORISES nothing: the drain's own floor,
	// its probe cap and the immutable reserve all still bind.
	WaveProbe Wave = "probe"
)

// WaveProbeReason names which clause forced PROBE. It exists because PROBE otherwise has one
// name and several meanings, and an operator watching probe buying continue cannot tell "this
// fleet cannot plausibly reach a hull" from "the lane surface is down" without it.
type WaveProbeReason string

const (
	WaveProbeReasonNone               WaveProbeReason = ""
	WaveProbeReasonGrowthDisabled     WaveProbeReason = "growth_disabled"
	WaveProbeReasonLanesUnreadable    WaveProbeReason = "lanes_unreadable"
	WaveProbeReasonLanesServed        WaveProbeReason = "lanes_served"
	WaveProbeReasonCapacityUnreadable WaveProbeReason = "capacity_unreadable"
	WaveProbeReasonUnreachable        WaveProbeReason = "unreachable"
)

// Reachable answers whether a fleet can PLAUSIBLY reach an ask, judged on the highest balance it
// has held across a full trade cycle rather than on the balance it happens to hold right now.
//
// WHY THE PEAK AND NOT THE LIVE BALANCE. A trading fleet's treasury oscillates by more than a
// hull's price within a single cycle as hulls buy and sell cargo, so an instantaneous balance
// reports WHERE IN THE CYCLE THE TICK LANDED, not what the fleet can afford. A regime chosen from
// that flips with every tour — and worse, flips the wrong way, since treasury peaks just after a
// sale and troughs once the money is committed to cargo. The peak across a cycle is the same
// number wherever in the cycle it is sampled, which is the whole property.
//
// IT IS HoldAt, GIVEN A DIFFERENT BALANCE — never a copy of its arithmetic. HoldAt already means
// "surplus above the immutable floor, less the entry share of the ask", so the entry computation,
// its overflow-safe two-term form and every rung of HeavyReserve above it are reused rather than
// restated. The two calls ask genuinely different questions and both are correct:
//
//	HoldAt(liveTreasury)      — how much may I withhold AT THIS INSTANT? A question about the
//	                            moment, and the right one for a withholding decision.
//	Reachable(t, highWater)   — is this ask reachable BY THIS FLEET? A question about capacity,
//	                            and the right one for choosing a regime.
//
// It authorises no spend. Every purchase remains gated by the full fail-closed guard stack
// against the LIVE balance, so an over-stated capacity can only ever pause probe buying — never
// approve a hull the fleet cannot pay for.
func Reachable(t HeavyReserveTarget, highWaterTreasury int64) bool {
	return t.HoldAt(highWaterTreasury) > 0
}

// WaveInputs are the facts the predicate judges. Every one is read fresh by the caller on the
// tick that asks; nothing here is remembered between ticks.
type WaveInputs struct {
	// GrowthEnabled is the heavy buyer's master switch as read this tick. Off means no heavy
	// buyer exists, which is why it is the first clause: pausing probe buying to save for a
	// purchase nothing can make is a deadlock no spender is able to clear.
	GrowthEnabled bool

	// UnservedLanes is the profitable, feasible lane count beyond the current trade pool — the
	// capacity-short signal. UnservedLanesReadable=false means the surface could not be read,
	// which yields PROBE rather than a guess.
	UnservedLanes         int
	UnservedLanesReadable bool

	// Target is what the fleet would be saving toward, from HeavyReserve — the ONE definition.
	// The cap clause, the priced-target clause and the capability clause are all expressed
	// through it rather than beside it, so they cannot drift from the reservation's own rungs.
	Target HeavyReserveTarget

	// HighWaterTreasury is the highest balance held across a full trade cycle — the fleet's
	// demonstrated capacity, and the balance the reachability clause judges.
	//
	// HighWaterReadable=false means the window could not be read or held no rows. EMPTY IS NOT
	// ZERO: a zero high-water is the strong claim "this fleet has never held money", which is a
	// genuine unreachable, while an unreadable window is "we could not see". Collapsing them
	// would report a blind read as a verdict.
	HighWaterTreasury int64
	HighWaterReadable bool

	// LiveTreasuryForReporting is carried for the decision log and the gauges ONLY. It is
	// deliberately NOT an input to any clause: a live balance here is what made the regime a
	// function of trade-cycle phase, and the name is what stops it being wired back in.
	LiveTreasuryForReporting int64
}

// DeriveWave is the ONE definition of the regime. The growth coordinator's heavy buyer and the
// sensing probe drain both call THIS function; a second derivation in either would let one spend
// while the other withholds, which is the split-brain the whole design exists to prevent.
//
// EVERY "CANNOT" ANSWER IS PROBE. That is the release direction, matching HeavyReserve's own
// documented treatment of a blind signal: PROBE withholds nothing and authorises nothing, so a
// wrong PROBE costs a deferred purchase, while a wrong HEAVY pauses coverage growth against a
// target nobody can justify.
//
// Pure: no clock, no I/O, no stored state — inputs to a regime.
func DeriveWave(in WaveInputs) (Wave, WaveProbeReason) {
	if !in.GrowthEnabled {
		return WaveProbe, WaveProbeReasonGrowthDisabled
	}
	if !in.UnservedLanesReadable {
		return WaveProbe, WaveProbeReasonLanesUnreadable
	}
	if in.UnservedLanes <= 0 {
		return WaveProbe, WaveProbeReasonLanesServed
	}
	if !in.HighWaterReadable {
		return WaveProbe, WaveProbeReasonCapacityUnreadable
	}
	if !Reachable(in.Target, in.HighWaterTreasury) {
		return WaveProbe, WaveProbeReasonUnreachable
	}
	return WaveHeavy, WaveProbeReasonNone
}
