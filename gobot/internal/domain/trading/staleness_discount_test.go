package trading

import (
	"math"
	"testing"
	"time"
)

const allActivities = 4

func activities() []string { return []string{"STRONG", "GROWING", "RESTRICTED", "WEAK"} }

// A quote observed now competes at FACE VALUE. This is half of what the change exists to
// fix: the retired gate charged nothing up to its cap and then everything, and a discount
// that nicked a fresh quote would just move the same unfairness to the other end.
func TestStalenessDiscount_AFreshQuoteIsUndiscounted(t *testing.T) {
	d := DefaultStalenessDiscount()
	for _, activity := range activities() {
		if got := d.DriftFraction(activity, 0); got != 0 {
			t.Fatalf("%s at age 0: drift %v, want 0", activity, got)
		}
		if got := d.AdjustedAsk(1000, activity, 0); got != 1000 {
			t.Fatalf("%s at age 0: ask %d, want 1000", activity, got)
		}
		if got := d.AdjustedBid(1000, activity, 0); got != 1000 {
			t.Fatalf("%s at age 0: bid %d, want 1000", activity, got)
		}
	}
	// A negative age (a clock skew, a quote stamped in the future) is not a bonus.
	if got := d.DriftFraction("STRONG", -time.Hour); got != 0 {
		t.Fatalf("negative age: drift %v, want 0", got)
	}
}

// Integer prices meet a continuous curve, so "undiscounted" has to survive rounding: a
// quote a second old must come back at exactly its quoted price, not a credit worse.
func TestStalenessDiscount_ASecondOldQuoteRoundsToItsOwnPrice(t *testing.T) {
	d := DefaultStalenessDiscount()
	for _, activity := range activities() {
		if got := d.AdjustedAsk(1000, activity, time.Second); got != 1000 {
			t.Fatalf("%s at 1s: ask %d, want 1000", activity, got)
		}
		if got := d.AdjustedBid(1000, activity, time.Second); got != 1000 {
			t.Fatalf("%s at 1s: bid %d, want 1000", activity, got)
		}
	}
}

// The discount RISES with age, strictly, over the whole range the fleet operates in — and
// the ordering across activity classes holds at every age.
func TestStalenessDiscount_GrowsWithAgeAndWithActivity(t *testing.T) {
	d := DefaultStalenessDiscount()
	ages := []time.Duration{time.Minute, 15 * time.Minute, time.Hour, 2 * time.Hour,
		4 * time.Hour, 8 * time.Hour, StalenessDiscountHorizon}

	for _, activity := range activities() {
		prev := -1.0
		for _, age := range ages {
			got := d.DriftFraction(activity, age)
			if got <= prev {
				t.Fatalf("%s: drift at %v is %v, not above %v at the previous age", activity, age, got, prev)
			}
			prev = got
		}
	}
	for _, age := range ages {
		strong := d.DriftFraction("STRONG", age)
		growing := d.DriftFraction("GROWING", age)
		restricted := d.DriftFraction("RESTRICTED", age)
		weak := d.DriftFraction("WEAK", age)
		if !(strong > growing && growing > restricted && restricted > weak) {
			t.Fatalf("at %v the activity ordering broke: strong=%v growing=%v restricted=%v weak=%v",
				age, strong, growing, restricted, weak)
		}
	}
}

// BOUNDED. Past the fitted horizon the curve is flat: a week-old quote is charged exactly
// what a twelve-hour-old one is, and never more than the fitted maximum. An unbounded
// haircut would eventually invert into a veto dressed as a score.
func TestStalenessDiscount_IsBoundedByTheFittedMaximum(t *testing.T) {
	d := DefaultStalenessDiscount()
	max := map[string]float64{
		"STRONG": DefaultStalenessDriftBpsStrong, "GROWING": DefaultStalenessDriftBpsGrowing,
		"RESTRICTED": DefaultStalenessDriftBpsRestricted, "WEAK": DefaultStalenessDriftBpsWeak,
	}
	for _, activity := range activities() {
		ceiling := max[activity] / 10000
		atHorizon := d.DriftFraction(activity, StalenessDiscountHorizon)
		if math.Abs(atHorizon-ceiling) > 1e-9 {
			t.Fatalf("%s at the horizon: drift %v, want the fitted max %v", activity, atHorizon, ceiling)
		}
		for _, age := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 365 * 24 * time.Hour} {
			if got := d.DriftFraction(activity, age); math.Abs(got-ceiling) > 1e-9 {
				t.Fatalf("%s at %v: drift %v, want it clamped at %v", activity, age, got, ceiling)
			}
		}
	}
}

// The scale knob cannot turn the haircut into an inversion: it is clamped, so an operator
// who types an extra zero gets a bounded charge rather than a ranking that prefers the
// stalest board on the map.
func TestStalenessDiscount_ScaleIsClampedAndZeroMeansTheFittedDefault(t *testing.T) {
	fitted := DefaultStalenessDiscount().DriftFraction("STRONG", time.Hour)

	if got := (StalenessDiscount{}).DriftFraction("STRONG", time.Hour); got != fitted {
		t.Fatalf("the zero value must charge the fit (%v), got %v", fitted, got)
	}
	if got := (StalenessDiscount{ScalePct: 200}).DriftFraction("STRONG", time.Hour); math.Abs(got-2*fitted) > 1e-9 {
		t.Fatalf("scale 200 should double the fit to %v, got %v", 2*fitted, got)
	}
	huge := StalenessDiscount{ScalePct: 100000}.DriftFraction("STRONG", StalenessDiscountHorizon)
	if huge > float64(maxStalenessDiscountScalePct)/100*DefaultStalenessDriftBpsStrong/10000+1e-9 {
		t.Fatalf("an absurd scale must clamp, got %v", huge)
	}
	if got := (StalenessDiscount{Disabled: true}).DriftFraction("STRONG", 8*time.Hour); got != 0 {
		t.Fatalf("the kill switch must charge nothing, got %v", got)
	}
}

// A lane is priced from TWO independently-aged quotes, and each is charged on its own age
// and activity. A fresh source beside a stale sink must not be charged as if both were
// fresh, nor as if both were stale.
func TestStalenessDiscount_ChargesEachEndOfALaneOnItsOwnAge(t *testing.T) {
	now := time.Now()
	d := DefaultStalenessDiscount()
	lane := ArbitrageLane{
		SourceAsk: 1000, DestBid: 3000, SpreadPerUnit: 2000,
		SourceActivity: "WEAK", SourceObservedAt: now,
		DestActivity: "STRONG", DestObservedAt: now.Add(-6 * time.Hour),
	}

	want := 3000 * d.DriftFraction("STRONG", 6*time.Hour)
	if got := d.SpreadHaircutPerUnit(lane, now); math.Abs(got-want) > 1e-6 {
		t.Fatalf("haircut %v, want only the stale DEST side charged (%v)", got, want)
	}

	// Both ends unstamped: an unknown age is not evidence of staleness.
	blind := ArbitrageLane{SourceAsk: 1000, DestBid: 3000, SpreadPerUnit: 2000,
		SourceActivity: "STRONG", DestActivity: "STRONG"}
	if got := d.SpreadHaircutPerUnit(blind, now); got != 0 {
		t.Fatalf("an unstamped lane must not be charged, got %v", got)
	}
}

// A haircut deeper than the quoted spread FLOORS at zero. Ranking a lane at a negative
// value would sort it as though it earned in the other direction; removing it is the
// backstop's job, not the discount's.
func TestStalenessDiscount_DiscountedSpreadNeverGoesNegative(t *testing.T) {
	now := time.Now()
	thin := ArbitrageLane{
		SourceAsk: 100000, DestBid: 100010, SpreadPerUnit: 10,
		SourceActivity: "STRONG", SourceObservedAt: now.Add(-8 * time.Hour),
		DestActivity: "STRONG", DestObservedAt: now.Add(-8 * time.Hour),
	}
	got := DefaultStalenessDiscount().DiscountedSpreadPerUnit(thin, now)
	if got != 0 {
		t.Fatalf("a spread the haircut exceeds must floor at 0, got %v", got)
	}
}

// THE BACKSTOP IS STILL A BACKSTOP. Pricing age continuously does not mean ranking a quote
// of any age: past the horizon the curve is flat, so the cap is what stops an ancient row
// scoring identically to a merely stale one.
func TestRankerAgeCaps_StillDropQuotesPastTheSaturationHorizon(t *testing.T) {
	now := time.Now()
	caps := DefaultRankerAgeCaps()
	for _, activity := range activities() {
		inside := GoodListing{Activity: activity, ObservedAt: now.Add(-caps.For(activity) + time.Minute)}
		if !caps.Fresh(inside, now) {
			t.Fatalf("%s just inside its backstop must stay rankable", activity)
		}
		for _, age := range []time.Duration{25 * time.Hour, 7 * 24 * time.Hour} {
			ancient := GoodListing{Activity: activity, ObservedAt: now.Add(-age)}
			if caps.Fresh(ancient, now) {
				t.Fatalf("%s aged %v must be dropped by the backstop, not ranked", activity, age)
			}
		}
	}
	if caps.Widest() != StalenessDiscountHorizon {
		t.Fatalf("the backstop must be the fit's saturation horizon, got %v", caps.Widest())
	}
	if len(activities()) != allActivities {
		t.Fatalf("the backstop table lost an activity")
	}
}
