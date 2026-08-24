package commands

// The same-market rebuy guard. A hull that sells into a market moves that market's price
// against itself: the sale drops the bid and, at the same waypoint, leaves an ask the next
// plan reads as the cheapest source for the good just dumped. Nothing downstream notices —
// the margin gate compares the ask against a sink, not against what this hull was paid
// here minutes ago — so the hull buys its own dump back and pays the spread again, and
// again, without ever leaving the system.
//
// The guard removes the BUY side of any (market, good) the SAME hull sold at inside the
// window, so no plan can re-source there while the dump is still in the price. The sink is
// left standing: a hold discharged in tranches must still be able to finish selling.
//
// It is a PLANNING filter, not a money guard. It fails OPEN — no record, no filtering —
// because a hull whose history is unknown must still be able to trade.

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// TuneKeySameMarketRebuyWindowMinutes is how long after a sale the selling hull is barred
// from re-sourcing that good at that market: `spacetraders tune --operation tour
// same_market_rebuy_window_minutes N`.
const TuneKeySameMarketRebuyWindowMinutes = "same_market_rebuy_window_minutes"

// defaultSameMarketRebuyWindowMinutes is the documented default of
// TuneKeySameMarketRebuyWindowMinutes, and what applies when nothing is tuned. It is sized
// against market recovery rather than tempo: the window only needs to outlast the price
// impact of the hull's own sale, since past that the ask is a real quote again and not an
// echo of the dump. Too short re-opens the cycle; far too long would bar a hull from a
// market that has genuinely re-priced, which costs real lanes.
const defaultSameMarketRebuyWindowMinutes = 30

// sameMarketRebuyReason labels the drop counter, so the guard's bite is a dashboard read
// rather than something only a log grep can find.
const sameMarketRebuyReason = "same_market_rebuy"

// marketGood is one (market, good) buy source — the granularity the guard blocks at, so a
// hull that sold here can still source the good elsewhere and other goods here.
type marketGood struct {
	waypoint string
	good     string
}

// noteRecentSell records that ship just sold good at waypoint.
//
// The record is IN-MEMORY on this daemon-lifetime handler and deliberately not persisted:
// what it protects against is a hull re-buying into the price impact of its OWN sale, and
// that impact decays on the same clock the record does. A restart therefore forgets a
// window that is already expiring, the guard re-arms on the hull's next sale, and the cost
// of the miss is bounded by one plan. Keyed by SHIP symbol because the impact belongs to
// the hull that caused it, not to the container that happened to be running it.
func (h *RunTourCoordinatorHandler) noteRecentSell(ship, waypoint, good string) {
	h.recordSellAt(ship, waypoint, good, h.clock.Now())
}

// recordSellAt is noteRecentSell with the instant supplied, so the window's boundaries are
// testable without a wall clock.
func (h *RunTourCoordinatorHandler) recordSellAt(ship, waypoint, good string, at time.Time) {
	if ship == "" || waypoint == "" || good == "" {
		return
	}
	h.recentSellsMu.Lock()
	defer h.recentSellsMu.Unlock()
	if h.recentSells == nil {
		h.recentSells = make(map[string]map[marketGood]time.Time)
	}
	sold := h.recentSells[ship]
	if sold == nil {
		sold = make(map[marketGood]time.Time)
		h.recentSells[ship] = sold
	}
	sold[marketGood{waypoint: waypoint, good: good}] = at
}

// recentRebuySources returns the buy sources ship is currently barred from, and prunes the
// entries that have aged out so a long-lived hull's record cannot grow without bound.
// Guarded by recentSellsMu because the handler is a SHARED singleton dispatched
// concurrently for every touring hull.
func (h *RunTourCoordinatorHandler) recentRebuySources(ship string, now time.Time, window time.Duration) map[marketGood]bool {
	h.recentSellsMu.Lock()
	defer h.recentSellsMu.Unlock()

	sold := h.recentSells[ship]
	if len(sold) == 0 {
		return nil
	}
	blocked := make(map[marketGood]bool, len(sold))
	for key, at := range sold {
		if now.Sub(at) > window {
			delete(sold, key)
			continue
		}
		blocked[key] = true
	}
	if len(sold) == 0 {
		delete(h.recentSells, ship)
	}
	return blocked
}

// rebuyWindow resolves the live window, falling back to the documented default whenever
// the tune surface has nothing positive to say.
func (h *RunTourCoordinatorHandler) rebuyWindow(ctx context.Context, playerID int) time.Duration {
	minutes := defaultSameMarketRebuyWindowMinutes
	if tuned, ok := h.freshness.TunedInt(ctx, playerID, TuneKeySameMarketRebuyWindowMinutes); ok {
		minutes = tuned
	}
	return time.Duration(minutes) * time.Minute
}

// dropRecentRebuySources zeroes the Ask on every blocked (market, good) row and reports how
// many it silenced. Zeroing rather than dropping the row is what keeps the sink: the solver
// pairs a buy leg only against a positive ask, and reads the bid independently.
//
// An empty block set returns the SAME slice (zero copy), so a hull with no record plans
// over exactly the universe the snapshot builder produced. The caller's rows are never
// mutated — the guard is per-hull and the snapshot is not.
func dropRecentRebuySources(snapshot []routing.TourGoodSnapshot, blocked map[marketGood]bool) ([]routing.TourGoodSnapshot, int) {
	if len(blocked) == 0 {
		return snapshot, 0
	}
	kept := make([]routing.TourGoodSnapshot, len(snapshot))
	copy(kept, snapshot)
	dropped := 0
	for i := range kept {
		if kept[i].Ask <= 0 || !blocked[marketGood{waypoint: kept[i].Waypoint, good: kept[i].Good}] {
			continue
		}
		kept[i].Ask = 0
		dropped++
	}
	return kept, dropped
}

// filterSameMarketRebuys applies the guard to one plan's good universe.
func (h *RunTourCoordinatorHandler) filterSameMarketRebuys(
	ctx context.Context, cmd *RunTourCoordinatorCommand, snapshot []routing.TourGoodSnapshot,
) []routing.TourGoodSnapshot {
	blocked := h.recentRebuySources(cmd.ShipSymbol, h.clock.Now(), h.rebuyWindow(ctx, cmd.PlayerID))
	filtered, dropped := dropRecentRebuySources(snapshot, blocked)
	if dropped > 0 {
		metrics.RecordTourCandidateDropped(cmd.PlayerID, sameMarketRebuyReason, dropped)
	}
	return filtered
}
