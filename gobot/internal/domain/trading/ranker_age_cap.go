package trading

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Activity-conditioned ranker freshness caps. The economy analyst fit
// these on era3/4 telemetry (n=3,236 trades): a stale listing's price barely drifts,
// and the drift COST is activity-dependent — a WEAK market is nearly static for
// hours, a STRONG one moves fast — so a single uniform freshness cap (the retired
// flat 75-minute maxListingAge) is the wrong SHAPE. Each cap is fitted so the p90
// sell-price drift over its window stays ≤ ~5%:
//
//   - WEAK       → 480 min (8h)  — nearly static, safe to rank off an 8h-old quote
//   - RESTRICTED → 180 min (3h)  — the conservative middle (also the unknown default)
//   - GROWING    →  60 min       — competition churns prices within the hour
//   - STRONG     →  30 min       — fastest-moving; a half-hour-old quote is unreliable
//
// These are the ARMED defaults (RULINGS #5): they are the single numeric source the
// config layer's absent-knob fallback and the RankerAgeCaps zero-value defense both
// reference, so the table is DEFINED ONCE and a captain retunes it purely from
// config.yaml ([trading] ranker_age_cap_minutes.{weak,restricted,growing,strong}).
const (
	DefaultRankerAgeCapWeak       = 480 * time.Minute
	DefaultRankerAgeCapRestricted = 180 * time.Minute
	DefaultRankerAgeCapGrowing    = 60 * time.Minute
	DefaultRankerAgeCapStrong     = 30 * time.Minute
)

// RankerAgeCaps is the activity-conditioned freshness table the UNDIRECTED lane
// ranker (partitionListingsByAge) and the tour snapshot builder (BuildTourSnapshot)
// drop stale listings against — each listing measured against ITS OWN activity's cap
// instead of one flat threshold. It is the one table both ranker sites reference
// (defined once, sp-t5sh5), resolved from config so the caps are tune knobs, not a
// hardcoded 4-way constant.
//
// It changes only the ranker's VISIBILITY cap (which cached rows are eligible to be
// RANKED). The execution money guards — staleAskAborts, the per-visit margin
// re-check, min-margin/price/reserve — are untouched (RULINGS #4): a directed --dest
// lane keeps its stale rows and is re-verified LIVE at execution, so a
// loosened visibility cap can never spend into a moved price; the execution tail
// still aborts.
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
