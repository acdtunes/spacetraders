package trading

import (
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// StalenessDiscountHorizon is the age at which the fitted drift curve saturates: the
// point past which an older quote carries no further measurable penalty. It is also
// what RankerAgeCaps is now sized to (the backstop), so the discount is continuous
// right up to the drop and nothing is ever ranked on an extrapolated curve.
const StalenessDiscountHorizon = 720 * time.Minute

// Fitted maximum expected adverse drift per quote side, in basis points of the quoted
// price, reached at StalenessDiscountHorizon. Activity is the conditioning variable:
// a STRONG board moves an order of magnitude more than a WEAK one over the same hours.
const (
	DefaultStalenessDriftBpsWeak       = 153
	DefaultStalenessDriftBpsRestricted = 238
	DefaultStalenessDriftBpsGrowing    = 602
	DefaultStalenessDriftBpsStrong     = 824
)

// DefaultStalenessDiscountScalePct charges the fitted curve at face value — the ARMED
// default, and what a zero/absent knob resolves to. maxStalenessDiscountScalePct bounds
// how far an operator can dial it up, so a mistyped knob cannot turn the haircut into a
// veto. Turning the discount OFF is Disabled, not a scale of zero: the "0 → fitted
// default" rule has to hold for the knob to be revertible.
const (
	DefaultStalenessDiscountScalePct = 100
	maxStalenessDiscountScalePct     = 500
)

// StalenessDiscount prices what a market quote's AGE costs: the expected adverse move
// in that price between the observation the ranker holds and the moment a hull would
// act on it.
//
// It replaces a binary freshness gate. The gate was wrong on both sides — a quote one
// minute past its cap was refused outright, a quote one minute inside it was ranked at
// full face value — and it was unachievable besides: the scan rotation over the
// probe-held map cannot refresh every waypoint inside the fitted STRONG cap at any
// affordable request budget, so the tight setting mass-refused good data and the loose
// setting traded hours-old quotes at par.
//
// ADVERSE, NOT MEAN. The mean SIGNED drift is ~0 — prices move both ways — so an
// unbiased discount would be no discount at all. That is the wrong statistic here,
// because the ranker does not act on a random lane: it acts on the lane that looks
// best, and a quote whose error flatters the spread is exactly the quote most likely to
// win selection. So each side is charged E[max(0, adverse move)] — the half of the
// error distribution that hurts, with no credit for the half that helps. That quantity
// is proportional to the error's dispersion, which is what makes an old quote's
// selection advantage disappear; a high quantile of the same distribution is not usable
// as a cross-activity basis because the distribution has a point mass at zero whose
// size varies by class (17% of WEAK windows move adversely against 50% of STRONG ones),
// which puts p75 below the mean for WEAK and above it for STRONG.
//
// LINEAR IN LOG-AGE, SATURATING. The measured curve rises steeply through the first
// hour then creeps, flat within noise past ~11h — the same shape, and the same reason,
// as the own-trade recency haircut. The form is therefore bounded by construction:
// zero at age zero, monotone, and clamped at the fitted maximum from
// StalenessDiscountHorizon on.
//
// RANKING ONLY — WHICH BINDS CALLERS AND IS NOT A PROPERTY OF THIS TYPE. A marked-UP ask
// or marked-DOWN bid LOOSENS any bound built on it, so a caller computing a money guard
// must band it around the quote it passed IN, never the one returned here (RULINGS #4).
type StalenessDiscount struct {
	// ScalePct scales the whole fitted table. 100 charges the fit as measured; a
	// zero/absent value resolves to that default, so the ZERO VALUE
	// StalenessDiscount{} — every unit test, any wiring gap — still charges the fit
	// rather than silently reverting to face-value ranking.
	ScalePct int
	// Disabled is the kill switch, and the only way to reach a discount of zero.
	Disabled bool
}

// DefaultStalenessDiscount returns the armed default: the fitted curve at face value.
func DefaultStalenessDiscount() StalenessDiscount {
	return StalenessDiscount{ScalePct: DefaultStalenessDiscountScalePct}
}

// scale resolves the configured percentage into a multiplier, clamped to
// [0, maxStalenessDiscountScalePct/100].
func (d StalenessDiscount) scale() float64 {
	if d.Disabled {
		return 0
	}
	pct := d.ScalePct
	switch {
	case pct <= 0:
		pct = DefaultStalenessDiscountScalePct
	case pct > maxStalenessDiscountScalePct:
		pct = maxStalenessDiscountScalePct
	}
	return float64(pct) / 100
}

// driftBpsFor returns the fitted saturation drift for an activity. An unknown or
// missing activity falls to RESTRICTED, the same conservative middle RankerAgeCaps.For
// uses — measured drift on unlabelled rows is about half RESTRICTED's, so this
// over-charges an unknown board rather than under-charging it.
func driftBpsFor(activity string) float64 {
	switch shared.ActivityLevel(activity) {
	case shared.ActivityLevelWeak:
		return DefaultStalenessDriftBpsWeak
	case shared.ActivityLevelGrowing:
		return DefaultStalenessDriftBpsGrowing
	case shared.ActivityLevelStrong:
		return DefaultStalenessDriftBpsStrong
	default:
		return DefaultStalenessDriftBpsRestricted
	}
}

// DriftFraction is the expected adverse move in one quoted price at this age and
// activity, as a fraction of the price.
//
// A non-positive age is undiscounted — a fresh quote competes at face value, and a
// zero/unknown age is not evidence of staleness (the same fail-open rule
// RankerAgeCaps.Fresh applies). The result is confined to [0, driftBps × scale] for
// every input, so no caller can be handed a discount that exceeds the price.
func (d StalenessDiscount) DriftFraction(activity string, age time.Duration) float64 {
	if age <= 0 {
		return 0
	}
	minutes := age.Minutes()
	share := math.Log1p(minutes) / math.Log1p(StalenessDiscountHorizon.Minutes())
	if share > 1 {
		share = 1
	}
	return driftBpsFor(activity) / 10000 * share * d.scale()
}

// AdjustedAsk is what a source's quoted ask is worth ranking at once its age is
// charged: the price we would pay, marked UP by the expected adverse rise.
//
// Rounded to NEAREST, not away from the trade. Prices are integers, so ceiling would
// charge a whole credit against a quote observed a microsecond ago and a fresh quote
// would not compete at face value — which is half of what this change exists to fix.
func (d StalenessDiscount) AdjustedAsk(ask int, activity string, age time.Duration) int {
	if ask <= 0 {
		return ask
	}
	return int(math.Round(float64(ask) * (1 + d.DriftFraction(activity, age))))
}

// AdjustedBid is what a destination's quoted bid is worth ranking at once its age is
// charged: the price we would receive, marked DOWN by the expected adverse fall. Floored
// at 0 — the fitted drift cannot reach 100%, but a bid must never go negative and turn a
// sink into a phantom source.
func (d StalenessDiscount) AdjustedBid(bid int, activity string, age time.Duration) int {
	if bid <= 0 {
		return bid
	}
	adjusted := int(math.Round(float64(bid) * (1 - d.DriftFraction(activity, age))))
	if adjusted < 0 {
		return 0
	}
	return adjusted
}

// SpreadHaircutPerUnit is the credits per unit a lane's quoted spread is discounted by:
// the source ask's expected adverse rise plus the destination bid's expected adverse
// fall, each charged at ITS OWN side's age and activity. Two sides, because a lane is
// priced from two independently-aged observations and either can have moved.
func (d StalenessDiscount) SpreadHaircutPerUnit(l ArbitrageLane, now time.Time) float64 {
	haircut := 0.0
	if !l.SourceObservedAt.IsZero() {
		haircut += float64(l.SourceAsk) * d.DriftFraction(l.SourceActivity, now.Sub(l.SourceObservedAt))
	}
	if !l.DestObservedAt.IsZero() {
		haircut += float64(l.DestBid) * d.DriftFraction(l.DestActivity, now.Sub(l.DestObservedAt))
	}
	return haircut
}

// DiscountedSpreadPerUnit is the lane's quoted per-unit spread less its staleness
// haircut, floored at zero.
//
// The floor is what keeps this a discount and not a veto: a lane the haircut would take
// negative ranks at the bottom of the order rather than inverting into a score that
// sorts as if the lane were profitable in the other direction. Nothing is removed from
// the candidate set here — that is the backstop cap's job, and it is applied as its own
// step before ranking.
func (d StalenessDiscount) DiscountedSpreadPerUnit(l ArbitrageLane, now time.Time) float64 {
	discounted := float64(l.SpreadPerUnit) - d.SpreadHaircutPerUnit(l, now)
	if discounted < 0 {
		return 0
	}
	return discounted
}
