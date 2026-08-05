package parkedsensing

import (
	"context"
	"fmt"
	"time"
)

// probeListingMemoTTL is how long a PERSISTED shipyard listing set is trusted
// before the yard is asked again.
//
// WHAT IT SAVES. ListProbeYards falls back to shipyard-TRAIT waypoints whenever a
// system has no stored probe listing, and that fallback is the normal path. Those
// waypoints are real shipyards that simply do not sell probes, so without a
// persisted memo every one of them a hull stands at costs one live quote per drain
// tick, forever, for an answer that is discarded each time. The per-tick
// refusalMemo stops the REPEATS within a tick; this carries the fact ACROSS ticks.
//
// The interval trades call volume against the window in which a restocked yard is
// wrongly written off, and being wrong is cheap and self-healing: such a yard is
// simply not bought from until the interval elapses, it blocks nothing else (seed
// staging tests hull PRESENCE via staffedAt, not probe stock), and any other yard
// in the system still serves the placement.
const probeListingMemoTTL = 6 * time.Hour

// probeStock is what a yard's STORED listings say about whether it sells a probe. It is the memo's
// three answers named, so the two engines that consult them cannot drift into different rules.
type probeStock int

const (
	// probeStockUnread — no listing has ever been persisted for this yard. It is how a yard ENTERS
	// the memo, so it must never be read as a negative: doing so would freeze the fleet's knowledge
	// and permanently write off every counter nothing had happened to look at yet.
	probeStockUnread probeStock = iota
	// probeStockSells — a stored, priced SHIP_PROBE listing. Evidence, not a trait guess.
	probeStockSells
	// probeStockNone — read recently, and it sells no probe. The standing fact worth acting on.
	probeStockNone
)

// readProbeStock classifies one yard from the stored listings, applying the memo's staleness rule in
// ONE place, so the buy queue and seed staging cannot drift apart on it.
//
// A STALE probe-less reading degrades to UNREAD, so a restocked counter is reconsidered rather than
// written off for the era. A nil memo answers UNREAD.
func readProbeStock(ctx context.Context, memo ProbeListingMemo, playerID int, yard string, now time.Time) (probeStock, time.Time, error) {
	if memo == nil {
		return probeStockUnread, time.Time{}, nil
	}
	sellsProbe, scannedAt, known, err := memo.LastListingScan(ctx, playerID, yard)
	if err != nil {
		return probeStockUnread, time.Time{}, fmt.Errorf("failed to read the stored listings of %q: %w", yard, err)
	}
	switch {
	case !known:
		return probeStockUnread, scannedAt, nil
	case sellsProbe:
		return probeStockSells, scannedAt, nil
	case now.Sub(scannedAt) >= probeListingMemoTTL:
		return probeStockUnread, scannedAt, nil // gone stale; ask again
	default:
		return probeStockNone, scannedAt, nil
	}
}

// ProbeYardIsCandidate answers the one question a probe-yard CANDIDATE LIST has
// to ask of each waypoint: may this yard appear at all?
//
// It is the exported face of readProbeStock and derives nothing of its own — the
// three-way classification, the staleness degrade and the nil-memo default all
// stay there, so the adapter behind ListProbeYards never re-derives "does this
// yard sell probes" in SQL. This only names the projection a yard list needs:
//
//   - SELLS  → candidate. Priced evidence.
//   - UNREAD → candidate. Never priced, so it is a guess — but it is also how the
//     fleet LEARNS where probes are sold, so ranking it last must never mean
//     dropping it.
//   - NONE   → NOT a candidate. Priced, and it sells no probe: the standing fact
//     the buy queue already refuses on. (A STALE reading is not this case —
//     readProbeStock degrades it to UNREAD, so a restocked counter is
//     reconsidered rather than written off for the era.)
//
// EVIDENCE vs GUESS is deliberately NOT returned: the only caller already gets
// that ranking from the order it unions its two sources in.
func ProbeYardIsCandidate(
	ctx context.Context,
	memo ProbeListingMemo,
	playerID int,
	yard string,
	now time.Time,
) (bool, error) {
	stock, _, err := readProbeStock(ctx, memo, playerID, yard, now)
	if err != nil {
		return false, err
	}
	return stock != probeStockNone, nil
}
