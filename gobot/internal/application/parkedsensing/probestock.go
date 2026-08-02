package parkedsensing

import (
	"context"
	"fmt"
	"time"
)

// probeListingMemoTTL is how long a PERSISTED shipyard listing set is trusted
// before the yard is asked again.
//
// THE WASTE IT REMOVES. ListProbeYards falls back to shipyard-TRAIT waypoints
// whenever a system has no stored probe listing, and that fallback is the normal
// path — only a handful of true probe yards are known fleet-wide. Those waypoints
// are real shipyards that simply do not sell probes, so every one of them that a
// hull happens to stand at costs one live quote per drain tick, forever, and the
// answer is discarded each time. The per-tick refusalMemo already stops the
// REPEATS within a tick; nothing carried the fact ACROSS ticks.
//
// WHY SIX HOURS. At ~57 drain cycles an hour a dead yard costs ~57 calls/hour
// today; trusting a stored reading for six hours costs one call per six hours per
// yard, a ~99.7% reduction, while still re-checking each yard ~20 times inside a
// ~120-hour era. A shorter interval does not scale with the thing that creates
// these yards: charting toward the 300-system target keeps adding shipyard-trait
// waypoints, and at one call/hour each a few dozen of them would re-create the
// very cost this removes. A longer one buys little more — the second six hours
// saves a further 0.08 calls/hour per yard — while widening the window in which a
// restocked yard is wrongly written off.
//
// BEING WRONG IS CHEAP AND SELF-HEALING, which is what makes six hours defensible
// rather than merely convenient: a yard that starts selling probes is simply not
// bought from until the interval elapses. It blocks nothing else — seed staging
// tests hull PRESENCE (staffedAt), not probe stock, so expansion is unaffected —
// and any other yard in the system still serves the placement.
//
// A plain constant, deliberately not a knob, in the manner of maxDrainAttempts:
// it paces how long one local fact is trusted, and nothing downstream benefits
// from tuning it.
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
// ONE place.
//
// A STALE probe-less reading degrades to UNREAD, so a restocked counter is reconsidered rather than
// written off for the era — the same rule skipKnownProbeless applies, and it is written here so the
// buy queue and seed staging cannot drift apart on it. A nil memo answers UNREAD, which is exactly
// the behaviour both callers had before the port existed.
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
// It is the exported face of readProbeStock — the FOURTH consumer of that one
// rule, not a fifth notion. It derives nothing of its own: the three-way
// classification, the staleness degrade and the nil-memo default all stay in
// readProbeStock, and this only names the projection a yard list needs.
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
// EVIDENCE vs GUESS is deliberately NOT returned. The only caller — the adapter
// behind ListProbeYards — already gets that ranking from the order it unions its
// two sources in, so returning it here would be a second, unreachable way to say
// the same thing.
//
// Without this the adapter would have to re-derive "does this yard sell probes"
// in SQL, which is precisely the drift probeStock.acceptsStaging and
// skipKnownProbeless were written to prevent — three engines answering one
// question three ways.
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
