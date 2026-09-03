package cargo

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// DefaultGuardReuseHeadroomPct and DefaultGuardReuseMaxAge arm the per-tranche
// guard-read reuse the Admiral ruled on 2026-09-03: one visit's live read may
// serve the NEXT tranche of the same good at the same market when the previous
// tranche's realised price still clears the guard by this margin, and only this
// recently. The guards spent ~800 live Get Market calls an hour re-reading a
// price their own previous tranche had just proven.
const (
	DefaultGuardReuseHeadroomPct = 10
	DefaultGuardReuseMaxAge      = 120 * time.Second
)

// Reuse labels on the existing scan-dedup saved-calls counter, and the log's
// side field, so the two savings stay separable in Prometheus.
const (
	scanDedupTrancheReuseBuy  = "tranche_reuse_buy"
	scanDedupTrancheReuseSell = "tranche_reuse_sell"
	guardSideBuy              = "buy"
	guardSideSell             = "sell"
)

// ResolveGuardReuse turns the [cargo_guards] knobs into the pair SetGuardReuse
// takes. A nil (ABSENT) knob is the armed default, so the reuse ships ON
// (RULINGS #22); an explicitly configured headroom or max age at or below zero
// is the operator's kill switch, and both disarm by the same route — headroom 0,
// the one value the predicate reads as "never reuse".
func ResolveGuardReuse(headroomPct, maxAgeSecs *int) (int, time.Duration) {
	pct, maxAge := DefaultGuardReuseHeadroomPct, DefaultGuardReuseMaxAge
	if headroomPct != nil {
		pct = *headroomPct
	}
	if maxAgeSecs != nil {
		maxAge = time.Duration(*maxAgeSecs) * time.Second
	}
	if pct <= 0 || maxAge <= 0 {
		return 0, DefaultGuardReuseMaxAge
	}
	return pct, maxAge
}

// guardReadReusable is the reuse predicate: may tranche N+1 dispatch on the read
// tranche N already paid for? Buy: the ask may walk UP by the headroom and still
// clear the ceiling. Sell: the bid may fall by it and still clear the floor.
// Everything else — reuse disarmed, no realised price, an incomplete previous
// tranche, a read past maxAge — takes the live read, exactly as today.
func guardReadReusable(prevRealisedPerUnit, guardPerUnit int, isBuy bool, headroomPct int, elapsed, maxAge time.Duration, prevTrancheComplete bool) bool {
	if headroomPct <= 0 || prevRealisedPerUnit <= 0 || !prevTrancheComplete || elapsed > maxAge {
		return false
	}
	headroom := prevRealisedPerUnit * headroomPct / 100
	if isBuy {
		return prevRealisedPerUnit+headroom <= guardPerUnit
	}
	return prevRealisedPerUnit-headroom >= guardPerUnit
}

// guardReuse is ONE command's tranche-loop memory: when its guard last actually
// read the market, and what the tranche before this one realised per unit.
type guardReuse struct {
	headroomPct  int
	maxAge       time.Duration
	lastRead     time.Time
	prevRealised int
	prevComplete bool
	reusedOn     int // the realised price THIS tranche dispatched on
	tranche      int
	dispatched   bool // this tranche skipped its live read
}

// verified stamps the moment the guard last actually read the market — a live
// scan, or the arrival scan today's dedup proves is this visit's own.
func (g *guardReuse) verified(now time.Time) {
	g.lastRead = now
}

// record files what a tranche realised per unit and whether it moved everything
// it asked for; a clamp, a reconcile or any retry is NOT complete. A part-credit
// ask rounds UP so the rounding can never favour the fleet (RULINGS #4).
func (g *guardReuse) record(totalAmount, units int, complete, isBuy bool) {
	g.prevRealised = 0
	if units > 0 {
		g.prevRealised = totalAmount / units
		if isBuy && totalAmount%units != 0 {
			g.prevRealised++
		}
	}
	g.prevComplete = complete
}

// reusable answers the predicate for this tranche. A read that never happened
// (zero lastRead) is never reusable.
func (g *guardReuse) reusable(now time.Time, guardPerUnit int, isBuy bool) bool {
	if g.lastRead.IsZero() {
		return false
	}
	return guardReadReusable(g.prevRealised, guardPerUnit, isBuy, g.headroomPct, now.Sub(g.lastRead), g.maxAge, g.prevComplete)
}

// dispatchOnReusedRead decides whether this tranche may skip its live read, and
// records the saved call when it does.
func (h *CargoTransactionHandler) dispatchOnReusedRead(ctx context.Context, cmd *CargoTransactionCommand, waypoint string, guardPerUnit int, isBuy bool, reuse *guardReuse) bool {
	now := h.clock.Now()
	if !reuse.reusable(now, guardPerUnit, isBuy) {
		return false
	}
	side, label := guardSideSell, scanDedupTrancheReuseSell
	if isBuy {
		side, label = guardSideBuy, scanDedupTrancheReuseBuy
	}
	reuse.dispatched = true
	reuse.reusedOn = reuse.prevRealised
	metrics.RecordScanDedupSaved(cmd.PlayerID.Value(), cmd.ShipSymbol, label)
	logging.LoggerFromContext(ctx).Log("DEBUG", fmt.Sprintf(
		"Guard reuse: tranche %d of %s at %s dispatches on the previous tranche's realised %d/unit (guard %d, headroom %d%%, read age %s) instead of a live read",
		reuse.tranche, cmd.GoodSymbol, waypoint, reuse.prevRealised, guardPerUnit, reuse.headroomPct, now.Sub(reuse.lastRead)), map[string]interface{}{
		"action": "guard_read_reused", "good": cmd.GoodSymbol, "waypoint": waypoint,
		"ship_symbol": cmd.ShipSymbol, "tranche": reuse.tranche,
		"prev_realised": reuse.prevRealised, "guard": guardPerUnit, "side": side,
	})
	return true
}

// guardReuseBreached is the fail-closed half: a tranche that dispatched on a
// reused read must justify it after the fact, and its OWN realised price beyond
// the guard aborts the remainder exactly as a tripped guard does. The exposure
// is bounded to that one tranche, which the ruling accepts.
//
// It reports an ABORT only when there is a remainder to withhold. A breach on
// the last tranche is logged and nothing more: the abort flags mean "units were
// held back", and the tour spends its one refusal per good on reading them, so
// raising one that withheld nothing would disarm the next sell of that good.
func (h *CargoTransactionHandler) guardReuseBreached(ctx context.Context, cmd *CargoTransactionCommand, waypoint string, isBuy bool, reuse *guardReuse, unitsRemaining int, shortfall bool) (bool, int) {
	if !reuse.dispatched {
		return false, 0
	}
	guardPerUnit, side, breached := cmd.MinBidPerUnit, guardSideSell, reuse.prevRealised < cmd.MinBidPerUnit
	if isBuy {
		guardPerUnit, side, breached = cmd.MaxAskPerUnit, guardSideBuy, reuse.prevRealised > cmd.MaxAskPerUnit
	}
	if !breached {
		return false, 0
	}
	tail := fmt.Sprintf("aborting the remaining %d units", unitsRemaining)
	switch {
	case shortfall:
		tail = "on the tranche a server-cargo reconcile already closed the transaction with"
	case unitsRemaining <= 0:
		tail = "on the last tranche, so nothing remains to abort"
	}
	logging.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Guard reuse breach: tranche %d of %s at %s dispatched on a reused read of %d/unit but realised %d/unit against guard %d - %s",
		reuse.tranche, cmd.GoodSymbol, waypoint, reuse.reusedOn, reuse.prevRealised, guardPerUnit, tail), map[string]interface{}{
		"action": "guard_reuse_breach", "good": cmd.GoodSymbol, "waypoint": waypoint,
		"ship_symbol": cmd.ShipSymbol, "tranche": reuse.tranche,
		"prev_realised": reuse.reusedOn, "guard": guardPerUnit, "side": side,
		"realised": reuse.prevRealised, "units_remaining": unitsRemaining, "shortfall": shortfall,
	})
	return unitsRemaining > 0 && !shortfall, reuse.prevRealised
}
