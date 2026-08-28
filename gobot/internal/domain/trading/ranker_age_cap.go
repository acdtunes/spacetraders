package trading

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Ranker age caps — the BACKSTOP horizon, no longer a freshness cliff.
//
// These caps used to BE the freshness model: a quote inside its activity's cap ranked
// at full face value, a quote a minute past it was refused outright. Both halves were
// wrong, and the cap was unachievable besides — the scan rotation over the probe-held
// map cannot refresh every waypoint inside a 30-minute STRONG cap at any affordable
// request budget, so a tight setting mass-refused good data and a loose one traded
// hours-old quotes at par. StalenessDiscount now prices age continuously, and the caps
// have one job left: stop ranking where the fitted curve stops saying anything.
//
// The horizon is therefore StalenessDiscountHorizon, uniformly. The activity
// conditioning moved to the discount, where it is measured; past saturation every
// class's curve is flat, so ranking beyond it would score a three-day-old quote exactly
// like a twelve-hour-old one — the very error the discount exists to remove. A
// per-activity backstop was considered and refused: the observed saturation ages are
// neither cleanly ordered nor separated by more than the noise (GROWING ~11h, STRONG
// and WEAK ~13.5h, RESTRICTED still creeping at 21h), so splitting them would fit noise
// into a money-adjacent threshold.
//
// These are the ARMED defaults (RULINGS #5): the single numeric source the config
// layer's absent-knob fallback and the RankerAgeCaps zero-value defense both reference,
// so the table is DEFINED ONCE and a captain retunes it purely from config.yaml
// ([trading] ranker_age_cap_minutes.{weak,restricted,growing,strong}). The four keys
// survive so one class can still be tightened alone.
const (
	DefaultRankerAgeCapWeak       = StalenessDiscountHorizon
	DefaultRankerAgeCapRestricted = StalenessDiscountHorizon
	DefaultRankerAgeCapGrowing    = StalenessDiscountHorizon
	DefaultRankerAgeCapStrong     = StalenessDiscountHorizon
)

// RankerAgeCaps is the backstop table the UNDIRECTED lane ranker
// (partitionListingsByAge) and the tour snapshot builder (BuildTourSnapshot) drop
// listings against, each measured against ITS OWN activity's cap. It is the one table
// both ranker sites reference (defined once, sp-t5sh5), resolved from config so the
// caps are tune knobs, not a hardcoded 4-way constant.
//
// It is a VISIBILITY bound (which cached rows are eligible to be RANKED at all), and
// it is now the outer bound only: within it, StalenessDiscount does the work of pricing
// how much an old quote is worth. The execution money guards — staleAskAborts, the
// per-visit margin re-check, min-margin/price/reserve — are untouched (RULINGS #4): a
// directed --dest lane keeps its stale rows and is re-verified LIVE at execution, so a
// loosened visibility cap can never spend into a moved price; the execution tail still
// aborts.
type RankerAgeCaps struct {
	Weak       time.Duration
	Restricted time.Duration
	Growing    time.Duration
	Strong     time.Duration
}

// DefaultRankerAgeCaps returns the fitted activity-cap table — the ARMED defaults an
// absent [trading] config section (and any caller that wants the table explicitly)
// resolves to.
func DefaultRankerAgeCaps() RankerAgeCaps {
	return RankerAgeCaps{
		Weak:       DefaultRankerAgeCapWeak,
		Restricted: DefaultRankerAgeCapRestricted,
		Growing:    DefaultRankerAgeCapGrowing,
		Strong:     DefaultRankerAgeCapStrong,
	}
}

// For returns the freshness cap for a listing's activity level. An unknown or
// missing activity ("" or any unrecognized value) falls to the RESTRICTED cap — the
// conservative middle: we neither over-trust an unlabeled market as static (WEAK) nor
// over-drop it as fast-moving (STRONG).
//
// Any zero/unset field falls back to that activity's fitted default, so the ZERO
// VALUE RankerAgeCaps{} (a handler whose SetRankerAgeCaps was never called — every
// unit test, any daemon wiring gap) still yields the armed defaults rather than a
// cap of 0 that would drop every dated listing. This is the same numeric source the
// config resolver uses, so "defined once" holds across both.
func (c RankerAgeCaps) For(activity string) time.Duration {
	switch shared.ActivityLevel(activity) {
	case shared.ActivityLevelWeak:
		return capOrDefault(c.Weak, DefaultRankerAgeCapWeak)
	case shared.ActivityLevelGrowing:
		return capOrDefault(c.Growing, DefaultRankerAgeCapGrowing)
	case shared.ActivityLevelStrong:
		return capOrDefault(c.Strong, DefaultRankerAgeCapStrong)
	case shared.ActivityLevelRestricted:
		return capOrDefault(c.Restricted, DefaultRankerAgeCapRestricted)
	default:
		return capOrDefault(c.Restricted, DefaultRankerAgeCapRestricted)
	}
}

// Fresh reports whether a listing is still rankable at now, measured against ITS OWN
// activity's cap. A zero ObservedAt is FRESH: an unknown age is not evidence of staleness.
//
// It is the one statement of that rule, shared by the undirected lane ranker
// (partitionListingsByAge) and the profitable-lane census, so a stale row cannot be
// invisible to the executor and visible to the count that sizes the fleet to fly it.
func (c RankerAgeCaps) Fresh(l GoodListing, now time.Time) bool {
	return l.ObservedAt.IsZero() || now.Sub(l.ObservedAt) <= c.For(l.Activity)
}

// Widest returns the loosest cap in the table — the age past which NO activity is still
// rankable. It is the honest BACKSTOP for a consumer downstream of a per-activity filter
// (the tour solver, which re-checks the snapshot it is handed): anything tighter would
// re-drop rows the per-activity pass deliberately kept, silently defeating the fitted
// model, while anything looser would admit rows no activity considers fresh. Derived from
// the same table For reads, so a retune moves it with no second definition.
func (c RankerAgeCaps) Widest() time.Duration {
	widest := time.Duration(0)
	for _, activity := range []shared.ActivityLevel{
		shared.ActivityLevelWeak,
		shared.ActivityLevelRestricted,
		shared.ActivityLevelGrowing,
		shared.ActivityLevelStrong,
	} {
		if capped := c.For(string(activity)); capped > widest {
			widest = capped
		}
	}
	return widest
}

// capOrDefault returns d when it is a positive duration, else the fitted default —
// the per-field "0 → armed default" fallback For relies on.
func capOrDefault(d, fitted time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fitted
}
