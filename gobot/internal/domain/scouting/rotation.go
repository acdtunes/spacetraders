package scouting

import (
	"math"
	"sort"
	"time"
)

// rotation.go holds the API-pressure shedding policy: under limiter contention
// the fleet rotates SCANNING off, never probes — a parked probe that skips a
// cycle costs zero API, while a repositioned one costs navigate+fuel. Pure
// math, no clocks, no randomness.

// ActiveShare maps the smoothed limiter wait to the fraction of in-scope
// systems that scan this cycle, and whether discovery may spend headroom.
// Discovery sheds FIRST because it is speculative; steady scanning of
// known-good systems degrades only after exploration has stopped, and never
// below half the fleet — degradation is bounded, not open-ended.
//
//	wait <= waitLow            → share 1.0, discovery true
//	waitLow < wait < waitHigh  → share 1.0, discovery false
//	wait >= waitHigh           → share scales linearly 1.0→0.5 over
//	                             [waitHigh, 4×waitHigh], floor 0.5, discovery false
func ActiveShare(wait, waitLow, waitHigh time.Duration) (share float64, discovery bool) {
	if wait <= waitLow {
		return 1.0, true
	}
	if wait < waitHigh {
		return 1.0, false
	}
	rampSpan := 3 * waitHigh
	if rampSpan <= 0 {
		// Degenerate config (waitHigh <= 0): treat any wait past waitLow as
		// full pressure rather than dividing by zero.
		return 0.5, false
	}
	share = 1.0 - 0.5*float64(wait-waitHigh)/float64(rampSpan)
	if share < 0.5 {
		share = 0.5
	}
	return share, false
}

// RotateDormant returns the systems that do NOT scan this cycle: the in-scope
// list (sorted ascending for determinism) is rotated round-robin by cursor; the
// first ceil(len×share) from the cursor stay active, the rest are dormant. The
// cursor advances by the active count each call (the caller persists it in
// memory only — a restart resets rotation harmlessly). Round-robin, never
// value-ranked: ranking would starve exactly the crushed markets the fleet most
// needs to watch recover. Under constant share every system is active at least
// once every ceil(1/share)+1 calls.
func RotateDormant(inScope []string, share float64, cursor int) (dormant map[string]bool, nextCursor int) {
	dormant = make(map[string]bool)
	total := len(inScope)
	if total == 0 {
		return dormant, cursor
	}

	ring := append([]string(nil), inScope...)
	sort.Strings(ring)

	if share < 0 {
		share = 0
	}
	if share > 1 {
		share = 1
	}
	active := int(math.Ceil(float64(total) * share))
	if active > total {
		active = total
	}

	cursor = ((cursor % total) + total) % total
	activeSet := make(map[string]bool, active)
	for i := 0; i < active; i++ {
		activeSet[ring[(cursor+i)%total]] = true
	}
	for _, system := range ring {
		if !activeSet[system] {
			dormant[system] = true
		}
	}
	return dormant, (cursor + active) % total
}
