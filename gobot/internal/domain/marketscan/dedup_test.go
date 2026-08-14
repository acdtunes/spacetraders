package marketscan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// DedupEligible is the pure decision at the heart of sp-7q61f: may an
// Earning-class guard reuse the market row its OWN visit's arrival travel just
// wrote, instead of spending another live GetMarket call? It is deliberately
// conservative — every condition must hold, and any doubt (a missing bracket, a
// row that predates this visit's travel, too much elapsed time, a malformed or
// backward-running clock) returns false, the caller's cue to run its
// unconditional fresh live scan exactly as before (RULINGS #4: fail CLOSED).
func TestDedupEligible(t *testing.T) {
	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	const maxElapsed = 5 * time.Second

	cases := []struct {
		name           string
		beforeTravel   time.Time
		afterArrival   time.Time
		rowLastUpdated time.Time
		now            time.Time
		want           bool
	}{
		{
			name:           "eligible: row written mid-travel, negligible elapsed time since confirmed arrival",
			beforeTravel:   base,
			afterArrival:   base.Add(2 * time.Second),
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(3 * time.Second),
			want:           true,
		},
		{
			name:           "not eligible: no bracket armed (ship not on the live allowlist)",
			beforeTravel:   time.Time{},
			afterArrival:   base.Add(2 * time.Second),
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(3 * time.Second),
			want:           false,
		},
		{
			name:           "not eligible: travel/dock never confirmed complete (afterArrival zero)",
			beforeTravel:   base,
			afterArrival:   time.Time{},
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(3 * time.Second),
			want:           false,
		},
		{
			name:           "not eligible: market never scanned (rowLastUpdated zero)",
			beforeTravel:   base,
			afterArrival:   base.Add(2 * time.Second),
			rowLastUpdated: time.Time{},
			now:            base.Add(3 * time.Second),
			want:           false,
		},
		{
			name:           "not eligible: row predates this visit's own travel attempt (stale pre-existing cache, not a fresh arrival scan)",
			beforeTravel:   base.Add(5 * time.Second),
			afterArrival:   base.Add(7 * time.Second),
			rowLastUpdated: base,
			now:            base.Add(8 * time.Second),
			want:           false,
		},
		{
			name:           "not eligible: real time separated the checks beyond the negligible-elapsed bound",
			beforeTravel:   base,
			afterArrival:   base.Add(2 * time.Second),
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(2*time.Second + maxElapsed + time.Second),
			want:           false,
		},
		{
			name:           "not eligible: malformed bracket (afterArrival before beforeTravel is never trusted)",
			beforeTravel:   base.Add(5 * time.Second),
			afterArrival:   base,
			rowLastUpdated: base,
			now:            base.Add(1 * time.Second),
			want:           false,
		},
		{
			name:           "not eligible: clock ran backward (now before confirmed arrival)",
			beforeTravel:   base,
			afterArrival:   base.Add(5 * time.Second),
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(4 * time.Second),
			want:           false,
		},
		{
			name:           "eligible: elapsed exactly at the bound (inclusive)",
			beforeTravel:   base,
			afterArrival:   base,
			rowLastUpdated: base,
			now:            base.Add(maxElapsed),
			want:           true,
		},
		{
			name:           "eligible: row written at the exact instant travel started (boundary - not before beforeTravel)",
			beforeTravel:   base,
			afterArrival:   base.Add(2 * time.Second),
			rowLastUpdated: base,
			now:            base.Add(2 * time.Second),
			want:           true,
		},
		{
			// sp-7q61f finding #2: beforeTravel..afterArrival can span the FULL
			// transit time (minutes, not seconds) on a real lane. A row written
			// shortly after beforeTravel — early in that window, e.g. a DIFFERENT
			// hull's own scan, or simply no scan at all since our own travel
			// started — must NOT read as "our own arrival scan" just because it
			// clears the old beforeTravel-only lower bound.
			name:           "not eligible: row written early in a long transit, well before OUR confirmed arrival (stale relative to afterArrival despite clearing the beforeTravel floor)",
			beforeTravel:   base,
			afterArrival:   base.Add(10 * time.Minute),
			rowLastUpdated: base.Add(1 * time.Second),
			now:            base.Add(10 * time.Minute),
			want:           false,
		},
		{
			name:           "eligible: row genuinely written at OUR OWN confirmed arrival, even after a long transit (true positive survives the tightened bound)",
			beforeTravel:   base,
			afterArrival:   base.Add(10 * time.Minute),
			rowLastUpdated: base.Add(10 * time.Minute),
			now:            base.Add(10 * time.Minute),
			want:           true,
		},
		{
			name:           "eligible: row exactly maxElapsed older than confirmed arrival (upper-bound boundary, inclusive)",
			beforeTravel:   base,
			afterArrival:   base.Add(10 * time.Minute),
			rowLastUpdated: base.Add(10*time.Minute - maxElapsed),
			now:            base.Add(10 * time.Minute),
			want:           true,
		},
		{
			name:           "not eligible: row one tick older than the upper-bound tolerance relative to confirmed arrival",
			beforeTravel:   base,
			afterArrival:   base.Add(10 * time.Minute),
			rowLastUpdated: base.Add(10*time.Minute - maxElapsed - time.Nanosecond),
			now:            base.Add(10 * time.Minute),
			want:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupEligible(tc.now, tc.beforeTravel, tc.afterArrival, tc.rowLastUpdated, maxElapsed)
			assert.Equal(t, tc.want, got)
		})
	}
}
