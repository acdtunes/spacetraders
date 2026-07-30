package ledger

import (
	"sort"
	"time"
)

// position_buckets.go — time bucketing for position-matched accounting, and the naive
// leg-summing it replaces (kept side by side ON PURPOSE, see BucketNaiveLegs).
//
// Bucketing is where the sp-76r2c defect became visible, so it is where the fix has to be
// demonstrable. Closed fragments bucket by ClosedAt — the instant the position was closed —
// so an hourly figure answers "what did positions closed in this hour earn". Open inventory
// never enters a realised bucket at all.

// RealizedBucket is realised margin over one time bucket, from CLOSED positions only. Open
// inventory and uncosted revenue are deliberately absent: they are separate figures, and
// folding either one in is the contamination this package exists to prevent.
type RealizedBucket struct {
	// Start is the bucket's inclusive lower bound, truncated to the bucket width in UTC.
	Start time.Time
	// Width is the bucket duration, carried so a rate ($/hr) can be derived without the
	// consumer having to remember which width it asked for.
	Width time.Duration
	// Closes is the number of matched fragments attributed to this bucket. Zero-fragment
	// buckets are NOT emitted — an absent bucket means "nothing closed", which is
	// different from "closed for zero", and inventing a 0 row would re-create the false
	// "net collapsed to nothing" reading that started sp-76r2c.
	Closes int
	// Units is the total cargo units closed in this bucket.
	Units int
	// Cost is the total purchase basis of what closed here — allocated from purchases that
	// may well have happened in an EARLIER bucket. That is the point.
	Cost int64
	// Revenue is the total sale proceeds of what closed here.
	Revenue int64
}

// RealizedMargin is Revenue − Cost: the realised profit of positions closed in this bucket.
func (b RealizedBucket) RealizedMargin() int64 { return b.Revenue - b.Cost }

// CreditsPerHour is the bucket's realised margin as an hourly rate. Zero when Width is
// non-positive rather than dividing by zero.
func (b RealizedBucket) CreditsPerHour() float64 {
	hours := b.Width.Hours()
	if hours <= 0 {
		return 0
	}
	return float64(b.RealizedMargin()) / hours
}

// MarginRatio is realised margin as a fraction of cost (0.25 == a 25% markup). Returns
// (0, false) when Cost is zero — a margin ratio on a zero basis is undefined, and a
// fabricated 0 would read as a break-even trade. Fail closed, RULINGS #4.
func (b RealizedBucket) MarginRatio() (float64, bool) {
	if b.Cost <= 0 {
		return 0, false
	}
	return float64(b.RealizedMargin()) / float64(b.Cost), true
}

// BucketRealized groups closed positions into time buckets of the given width by ClosedAt,
// ascending. Pure; the input is not mutated.
//
// A non-positive width yields nil rather than panicking or degenerating to one bucket per
// fragment. Buckets with no closes are omitted (see RealizedBucket.Closes).
func BucketRealized(closed []ClosedPosition, width time.Duration) []RealizedBucket {
	if width <= 0 {
		return nil
	}

	byStart := make(map[time.Time]*RealizedBucket)
	for _, c := range closed {
		start := c.ClosedAt.UTC().Truncate(width)
		bucket, ok := byStart[start]
		if !ok {
			bucket = &RealizedBucket{Start: start, Width: width}
			byStart[start] = bucket
		}
		bucket.Closes++
		bucket.Units += c.Units
		bucket.Cost += c.Cost
		bucket.Revenue += c.Revenue
	}

	out := make([]RealizedBucket, 0, len(byStart))
	for _, bucket := range byStart {
		out = append(out, *bucket)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// NaiveBucket is the DEFECTIVE reading: a plain signed sum of raw legs per time bucket, the
// SQL equivalent of
//
//	SELECT date_trunc('hour', created_at), SUM(amount) ... GROUP BY 1
//
// It is implemented here deliberately, not by accident and not for use in reporting. It
// exists so a consumer can print the two figures SIDE BY SIDE and see the distortion for
// itself, and so the regression suite can assert the divergence rather than merely assert
// the fix looks reasonable. sp-76r2c was filed because this reading is PLAUSIBLE — it never
// looks broken, it just inverts. The only durable defence is to keep it visible next to the
// truth.
//
// Do not build a KPI, guard, panel or autosizer input on NetCredits.
type NaiveBucket struct {
	Start time.Time
	Width time.Duration
	// Legs is the number of raw ledger rows summed.
	Legs int
	// Buys and Sells count the legs by direction. A bucket where these are lopsided is
	// exactly where NetCredits lies hardest.
	Buys  int
	Sells int
	// NetCredits is SUM(amount) over the raw legs — purchases negative, sales positive,
	// unmatched. THIS IS THE DEFECTIVE FIGURE.
	NetCredits int64
}

// BucketNaiveLegs reproduces the defective time-bucketed leg sum, for comparison against
// BucketRealized only. See NaiveBucket.
func BucketNaiveLegs(legs []CargoLeg, width time.Duration) []NaiveBucket {
	if width <= 0 {
		return nil
	}

	byStart := make(map[time.Time]*NaiveBucket)
	for _, leg := range legs {
		start := leg.At.UTC().Truncate(width)
		bucket, ok := byStart[start]
		if !ok {
			bucket = &NaiveBucket{Start: start, Width: width}
			byStart[start] = bucket
		}
		bucket.Legs++
		if leg.IsBuy {
			bucket.Buys++
			bucket.NetCredits -= magnitude(leg.Amount)
			continue
		}
		bucket.Sells++
		bucket.NetCredits += magnitude(leg.Amount)
	}

	out := make([]NaiveBucket, 0, len(byStart))
	for _, bucket := range byStart {
		out = append(out, *bucket)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// FilterClosedByWindow keeps the fragments attributed to [start, end) by ClosedAt.
//
// This is the correct way to scope a report: match over the FULL history, then filter the
// OUTPUT. Filtering the INPUT legs instead truncates the purchase stream, and every position
// opened before start degrades into an UncostedSale — the same artefact class as bucketing by
// hour, just relocated to the window edge. Live p99 holding time is 25.6h (max 90h), so a
// six-hour input filter would corrupt a six-hour report.
//
// A zero start or end is treated as unbounded on that side.
func FilterClosedByWindow(closed []ClosedPosition, start, end time.Time) []ClosedPosition {
	out := make([]ClosedPosition, 0, len(closed))
	for _, c := range closed {
		if !start.IsZero() && c.ClosedAt.Before(start) {
			continue
		}
		if !end.IsZero() && !c.ClosedAt.Before(end) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// FilterOpenAsOf keeps the positions that were still open at asOf — purchased at or before
// it, by OpenedAt. Pass the zero time for "every open position".
//
// NOTE this is a filter over positions the MATCH already determined to be open at the end of
// the matched history; it does not reconstruct the hold as it stood at an arbitrary past
// instant (a position open at 06:00 and closed at 08:00 is a ClosedPosition, and is
// correctly absent here). Use it to scope inventory reporting, not to replay history.
func FilterOpenAsOf(open []OpenPosition, asOf time.Time) []OpenPosition {
	out := make([]OpenPosition, 0, len(open))
	for _, o := range open {
		if !asOf.IsZero() && o.OpenedAt.After(asOf) {
			continue
		}
		out = append(out, o)
	}
	return out
}
