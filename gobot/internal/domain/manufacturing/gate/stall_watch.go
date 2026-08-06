package gate

import (
	"fmt"
	"time"
)

// StallThreshold is how long an unmet gate material may receive ZERO delivered units before the
// silence is escalated (sp-63r4f).
//
// SIXTY MINUTES, THE SAME DERIVATION AS sp-c9wuu's StuckPauseTimeout AND FOR THE SAME REASON: two
// constructionSupplyTaskDefaultTimeout lifetimes (2x30m). One lifetime is how long a single
// claim->source->deliver->record leg may legitimately take, so anything shorter would fire on a
// pipeline that is simply mid-leg. The second covers the gap between legs — a buy floor can idle
// for tens of minutes while a market regenerates, which is healthy waiting and must not be reported
// as a stall or this becomes the noise it exists to cut through.
//
// Reusing the number is deliberate rather than lazy: both bound "how long may this pipeline
// legitimately show no progress", and two constants answering one question would drift. If the
// supply-task lifetime moves, both should move with it.
//
// Against the incidents it would have fired at 60 minutes into stalls that ran 90 minutes, 7.5
// hours and 8 hours — every one of which was instead found by a human asking why the gate was stuck.
const StallThreshold = 60 * time.Minute

// MaterialProgress is what was OBSERVED about one gate material, gathered by the caller from
// persisted rows. Every field is a measurement; none is an inference.
type MaterialProgress struct {
	Good      string
	Remaining int
	// UnitsDelivered is how many units landed inside the window. Zero is the stall condition.
	UnitsDelivered int
	// LastDeliveryAt is when units last landed, from the persisted task history. The zero value
	// means "nothing has ever been delivered for this material", which is a legitimate state for a
	// young pipeline and is why SinceMeasuredFrom exists.
	LastDeliveryAt time.Time
	// SourceSupply is the current supply level at the material's source, carried purely so the
	// escalation can report it. It is NOT part of the decision.
	SourceSupply string
}

// StallVerdict is one material's progress ruling, materialized so the escalation can report what
// was observed rather than what someone guessed.
//
// IT DELIBERATELY CARRIES NO CAUSE. Three different defects produced identical silence this week —
// a head-of-line block, an affordability wall and a hysteresis deadlock — and each logged something
// true and reassuring while the gate sat stopped. A watchdog that guessed among them would have
// been wrong two times in three, and a confident wrong cause is worse than none: it sends the
// reader somewhere specific. The diagnosis is the human's; this makes the silence loud.
type StallVerdict struct {
	Good           string
	Remaining      int
	UnitsDelivered int
	StalledFor     time.Duration
	SourceSupply   string
	Stalled        bool
}

// LogLine renders the escalation. Everything is in the MESSAGE because the container log renderer
// drops metadata maps, and a watchdog that reported only in metadata would be exactly as silent as
// the stalls it exists to break.
func (v StallVerdict) LogLine(site string) string {
	supply := v.SourceSupply
	if supply == "" {
		supply = "unreadable"
	}
	return fmt.Sprintf(
		"GATE STALLED: %s at %s has received %d units in the last %s and still needs %d. Source supply reads %s. This is a PROGRESS check, not a diagnosis — it reports only that nothing has arrived, and the cause could be sourcing, affordability, a paused buy floor, or something not yet seen",
		v.Good, site, v.UnitsDelivered, v.StalledFor.Round(time.Minute), v.Remaining, supply)
}

// DetectStalls rules on every material observed, returning only the stalled ones.
//
// measuredFrom is the earliest instant the caller can speak for — the start of the observation
// window. A material whose LastDeliveryAt is zero (nothing ever delivered) is judged from there, so
// a pipeline that has only just started is not accused of stalling before it has had a chance to
// deliver anything.
//
// A MET MATERIAL IS NEVER STALLED, however long it has been quiet: silence on a finished bill is
// the correct outcome, and reporting it would train the reader to ignore this line. That is
// criterion 1, and it is the difference between a watchdog and an alarm nobody reads.
func DetectStalls(now time.Time, measuredFrom time.Time, materials []MaterialProgress, threshold time.Duration) []StallVerdict {
	var stalled []StallVerdict
	for _, m := range materials {
		if m.Remaining <= 0 {
			continue // the bill is met; silence here is success
		}
		if m.UnitsDelivered > 0 {
			continue // real progress, however small — criterion 5's trickle
		}
		since := m.LastDeliveryAt
		if since.IsZero() {
			// NOTHING EVER DELIVERED. There is no timestamp to measure from, so the window start is
			// the earliest instant the caller can stand behind. This keeps a young pipeline from
			// being accused of a stall measured since the epoch.
			since = measuredFrom
		}
		// A KNOWN DELIVERY BEFORE THE WINDOW IS NOT CLAMPED, and this is criterion 6 (sp-20eyn).
		//
		// The obvious-looking `|| since.Before(measuredFrom)` belongs here and is WRONG: it makes
		// the reported stall depend on how long the observer has been watching. A process that
		// restarted ten seconds ago would compute ten seconds of silence for a material that has
		// been quiet for seven and a half hours, so a restart LOOP would hide the stall completely —
		// exactly the 34,279-restart shape. LastDeliveryAt comes from persisted task rows, which
		// outlive the process, so an old timestamp is real knowledge rather than a gap and must be
		// used as-is.
		quiet := now.Sub(since)
		if quiet < threshold {
			continue
		}
		stalled = append(stalled, StallVerdict{
			Good:           m.Good,
			Remaining:      m.Remaining,
			UnitsDelivered: m.UnitsDelivered,
			StalledFor:     quiet,
			SourceSupply:   m.SourceSupply,
			Stalled:        true,
		})
	}
	return stalled
}
