package trading

import (
	"context"
	"math"
	"sort"
	"time"
)

// jump_toll_model.go — the MEASURED per-gate-hop toll: what one jump actually costs the hull
// that flies it, in wall-clock seconds, learned from the fleet's own hops.
//
// WHAT THE QUANTITY IS. The tour solver prices a crossing affinely — base + per_hop x hops
// (relocation_travel_model.go) — where the per-hop term is the MARGINAL cost of one gate hop:
// the jump plus the cooldown that must elapse before the hull can act again. That term has
// always been a constant fitted once and then frozen, and freezing it is the defect. The
// number is not a property of the map: it is the cooldown the API hands back plus whatever
// scheduling and rate-limit delay the fleet is running under, and both move. The fitted 650
// came off a cap-4-era window; the level the fleet later ran the solver at (1030) came off a
// separate one-time reading of 2,325 hops. Neither can follow the fleet.
//
// WHY WALL-CLOCK AND NOT THE REPORTED COOLDOWN. The cooldown is what the game charges; the
// wait is what the hull PAYS. Between them sit the jump call itself, the shared rate limiter's
// queue, and the wait budget's own jitter margin. The solver's objective divides value by
// TIME, so the honest denominator is the interval over which the hull earned nothing — from
// dispatching the jump to being able to act again. That is also how the 1030 was measured, so
// the estimator and the constant it replaces denominate the same thing.
//
// WHY THE COOLDOWN IS RECORDED ANYWAY. The API's cooldown scales with the hop's distance, so
// it is the distance signal, available for free and more directly than any coordinate lookup
// (the daemon has no reliable galaxy coordinates: system_coords is written lazily by the
// visualizer and is era-scoped). It is carried on every sample so a later banded or per-pair
// refit has its data, but nothing prices off it today — the solver's crossing model has ONE
// fleet-wide marginal term and no distance channel on the wire, so a banded estimate would
// have nowhere to land.

// JumpTollSample is one measured gate hop.
//
// WaitSeconds is the economic cost: wall-clock from dispatching the jump to the hull being
// action-ready again. CooldownSeconds is what the API reported for that same hop — recorded
// as the distance signal, never priced off (see the file header).
type JumpTollSample struct {
	WaitSeconds     int
	CooldownSeconds int
	RecordedAt      time.Time
}

// JumpTollParams are the estimator's shape. They have ONE definition
// (DefaultJumpTollParams) and are injected rather than read from a knob, exactly as the
// fitted crossing coefficients above them are: these are properties of the ESTIMATOR, chosen
// against the measured distribution, not levers an operator would move from something seen on
// a live fleet (RULINGS #5 as bounded 2026-07-25). A zero value estimates nothing, so an
// unwired caller withholds the override instead of inventing a window.
type JumpTollParams struct {
	// Window is how far back a sample still counts as evidence about now.
	Window time.Duration
	// Bucket is the width of one median. Medians are taken WITHIN a bucket and decayed
	// ACROSS buckets, which is what makes the estimator both tail-robust and recency-weighted.
	Bucket time.Duration
	// HalfLife is how long it takes a bucket's influence to halve.
	HalfLife time.Duration
	// MinSamples is the floor below which no override is emitted at all.
	MinSamples int
	// MinSampleSeconds and MaxSampleSeconds bound what counts as one hop's cost.
	MinSampleSeconds int
	MaxSampleSeconds int
}

// DefaultJumpTollParams is the armed shape (RULINGS #22: this ships ON — the estimator feeds
// the solver as soon as it has samples).
//
// EVERY VALUE IS SET AGAINST THE MEASURED DISTRIBUTION the constant it replaces was read from
// (2,325 hops: median 1028, mean 1122, p75 1546, max ~3600):
//
//   - Window 24h, Bucket 1h. At the fleet's observed jump rate a day is ~2k hops over 24
//     buckets, so a bucket holds enough readings for its median to mean something while
//     still resolving a regime change within the hour.
//   - HalfLife 6h. The tail in that measurement was partly contention from bottlenecks that
//     have since been fixed, so the estimator must be able to walk the level DOWN as
//     conditions smooth. Six hours retires a stale regime inside a shift (a day-old bucket is
//     worth 1/16 of a fresh one) without letting one quiet hour reprice the fleet.
//   - MinSamples 30. Below this the median of the newest bucket is noise, and the fitted
//     default is the better estimate — so the estimator stays silent and the solver keeps it.
//   - The band [60, 3600] admits a hop and rejects what is not one. Under 60s is not a
//     cooldown at all (a mis-bracketed no-op); over an hour is a re-adopted hull riding out a
//     cooldown persisted across a restart, which is a resume artifact rather than the price
//     of a hop. 3600 is exactly the max of the measured population.
func DefaultJumpTollParams() JumpTollParams {
	return JumpTollParams{
		Window:           24 * time.Hour,
		Bucket:           time.Hour,
		HalfLife:         6 * time.Hour,
		MinSamples:       30,
		MinSampleSeconds: 60,
		MaxSampleSeconds: 3600,
	}
}

// EstimatePerHopTollSeconds is the estimator: a decay-weighted average of per-bucket MEDIANS.
//
// THE TWO STATISTICS DO DIFFERENT JOBS, and using one for both is what makes this defensible
// at its size. The median is taken WITHIN a bucket because the hop distribution is
// right-skewed (mean 1122 against median 1028, p75 1546) and a mean would let a handful of
// contention-inflated readings set the fleet's price. The DECAY is applied ACROSS buckets
// because the skew is not the only thing moving — the whole level shifts with traffic, and an
// unweighted window would keep pricing yesterday's congestion into today's plans.
//
// Bucket weight is count x 2^(-age/half-life): the exponential is the recency term, and the
// count keeps a bucket holding three hops from outvoting one holding three hundred.
//
// ok=false is the FAIL-OPEN surface and it is load-bearing: fewer than MinSamples admissible
// readings means the caller sends nothing, the solver never sees a request value, and the
// fitted default prices the tour exactly as it does today. Every degenerate input lands here —
// no samples, a zero-value params, a window of nothing but out-of-band readings.
//
// The returned value is clamped to the SAME [CrossingTermMinSeconds, CrossingTermMaxSeconds]
// bounds the solver clamps its own term to, so an estimate can never price a crossing at zero
// (making every distant region look free) or at an absurd level, and the value that crosses
// the wire is one the far side will not silently alter.
func EstimatePerHopTollSeconds(samples []JumpTollSample, now time.Time, p JumpTollParams) (int, bool) {
	if p.Window <= 0 || p.Bucket <= 0 || p.HalfLife <= 0 || p.MinSamples <= 0 {
		return 0, false
	}
	if p.MinSampleSeconds <= 0 || p.MaxSampleSeconds <= p.MinSampleSeconds {
		return 0, false
	}

	// Bucket index 0 is the newest Bucket-wide slice of the window. A sample stamped slightly
	// in the future (clock skew between the recording daemon and this read) lands in bucket 0
	// rather than being discarded — it is the freshest evidence there is.
	buckets := make(map[int][]int)
	admitted := 0
	for _, s := range samples {
		if s.WaitSeconds < p.MinSampleSeconds || s.WaitSeconds > p.MaxSampleSeconds {
			continue
		}
		age := now.Sub(s.RecordedAt)
		if age > p.Window {
			continue
		}
		idx := 0
		if age > 0 {
			idx = int(age / p.Bucket)
		}
		buckets[idx] = append(buckets[idx], s.WaitSeconds)
		admitted++
	}
	if admitted < p.MinSamples {
		return 0, false
	}

	var weighted, weight float64
	for idx, waits := range buckets {
		// Age the bucket from its MIDPOINT, not its edge: the edge would price every reading
		// in the newest bucket as if it had just happened, and the oldest as if it were a full
		// bucket younger than it is.
		ageHours := (float64(idx) + 0.5) * p.Bucket.Hours()
		w := float64(len(waits)) * math.Exp2(-ageHours/p.HalfLife.Hours())
		weighted += w * medianOf(waits)
		weight += w
	}
	if weight <= 0 {
		return 0, false
	}

	seconds := weighted / weight
	if seconds < CrossingTermMinSeconds {
		seconds = CrossingTermMinSeconds
	}
	if seconds > CrossingTermMaxSeconds {
		seconds = CrossingTermMaxSeconds
	}
	return int(math.Round(seconds)), true
}

// medianOf sorts a copy and returns the middle (the mean of the two middles on an even count).
// Callers only reach it with a non-empty slice — an empty bucket is never created.
func medianOf(values []int) float64 {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return float64(sorted[mid])
	}
	return float64(sorted[mid-1]+sorted[mid]) / 2
}

// JumpTollRepository persists measured hops and reads back the recent window the estimator
// runs over. Implemented by the persistence layer.
//
// RECOMPUTE-FROM-DURABLE, NOT PERSISTED STATE (RULINGS #2). The estimate itself is never
// stored: every sample is a durable row, and the estimator is a pure function of the rows in
// the window. A daemon restart therefore recovers the live toll on its first solve with no
// reload path to get wrong, and a restart mid-window loses nothing but the hop it was flying.
// The same discipline the per-departure-gate fee table follows.
type JumpTollRepository interface {
	// RecordJumpToll persists one measured hop.
	RecordJumpToll(ctx context.Context, playerID int, shipSymbol, fromSystem, toSystem string, sample JumpTollSample) error
	// RecentJumpTolls returns playerID's samples recorded at or after since, newest first.
	RecentJumpTolls(ctx context.Context, playerID int, since time.Time, limit int) ([]JumpTollSample, error)
}
