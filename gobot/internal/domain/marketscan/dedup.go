package marketscan

import "time"

// DedupMaxElapsedSinceArrival bounds how long after a visit's confirmed arrival
// an Earning-class guard may still treat the arrival scan as current. Shared by
// every DedupEligible caller so the guards can never disagree about what
// "negligible" means. It also bounds how much OLDER than afterArrival the
// cached row's own timestamp may be — see DedupEligible.
const DedupMaxElapsedSinceArrival = 3 * time.Second

// DedupEligible reports whether an Earning-class guard scan may reuse a market
// row already on file instead of spending a fresh live scan. Every condition
// must hold; any failure returns false, the caller's cue to scan live as
// before (money guards fail closed and are never weakened).
//
//   - beforeTravel: clock reading before this visit's own travel started.
//     Zero means no bracket armed — dedup is never considered.
//   - afterArrival: clock reading once travel+dock both confirmed complete —
//     the reference point the elapsed budget is measured from, so transit time
//     itself never counts against "negligible elapsed".
//   - rowLastUpdated: the cached row's own scan timestamp. Must be no older
//     than beforeTravel (proving it postdates this visit's own travel
//     attempt, not a stale pre-existing leftover) AND no more than maxElapsed
//     older than afterArrival (proving it is fresh relative to OUR OWN
//     confirmed arrival, not merely some write that landed anywhere across
//     the full beforeTravel..afterArrival window — transit can run minutes,
//     long enough for a DIFFERENT hull's write early in that window to
//     otherwise look identical to our own arrival scan).
//   - maxElapsed bounds both now-afterArrival and afterArrival-rowLastUpdated.
//     A clock that runs backward is never trusted either.
//
// This does not detect a concurrent hull trading the same waypoint inside the
// narrow maxElapsed window around afterArrival; a row rewritten in that
// window is still live data, so leniency there does not weaken the guard.
func DedupEligible(now, beforeTravel, afterArrival, rowLastUpdated time.Time, maxElapsed time.Duration) bool {
	if beforeTravel.IsZero() || afterArrival.IsZero() || rowLastUpdated.IsZero() {
		return false
	}
	if afterArrival.Before(beforeTravel) {
		return false
	}
	elapsed := now.Sub(afterArrival)
	if elapsed < 0 || elapsed > maxElapsed {
		return false
	}
	if rowLastUpdated.Before(beforeTravel) {
		return false
	}
	return afterArrival.Sub(rowLastUpdated) <= maxElapsed
}
