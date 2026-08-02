package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// capitalCeiling resolves the pre-positioning capital ceiling: ceilingPct (default 10)
// percent of treasury, held JUNIOR to the working-capital reserve. Returns known=false
// when the balance is UNREADABLE — the pick then stocks nothing (fail closed, RULINGS #4).
// Mirrors the tour's depositCapitalCeiling verbatim, including its treasury read: both go
// through the shared ledger-backed reader rather than each calling Get Agent.
func (h *RunStockerCoordinatorHandler) capitalCeiling(ctx context.Context, playerID int, reserve int64) (int64, bool) {
	if h.apiClient == nil && h.treasury == nil {
		return 0, false
	}
	credits, err := readTreasuryCredits(ctx, h.treasury, h.apiClient, playerID)
	if err != nil {
		return 0, false
	}
	pct := int64(h.ceilingPct)
	if pct <= 0 {
		pct = defaultDepositCeilingPct
	}
	ceiling := credits * pct / 100
	if avail := credits - reserve; avail < ceiling {
		ceiling = avail // junior to the working-capital reserve
	}
	if ceiling < 0 {
		ceiling = 0
	}
	return ceiling, true
}

// foreignMarketFresh reports whether the miner's cheapest-foreign market for good is a
// trustworthy pick: the cached market row must still sell the good and be no older than
// maxAge (the 75-min discipline). A zero timestamp is "unknown age" and treated as fresh
// (matches tour_snapshot). An unreadable/gone market fails CLOSED (not fresh) so the
// stocker never hauls to a market it cannot confirm.
func (h *RunStockerCoordinatorHandler) foreignMarketFresh(ctx context.Context, waypoint, good string, playerID int, now time.Time, maxAge time.Duration) bool {
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil || mkt == nil {
		return false
	}
	if mkt.FindGood(good) == nil {
		return false
	}
	observed := mkt.LastUpdated()
	if observed.IsZero() {
		return true
	}
	return now.Sub(observed) <= maxAge
}

// foreignMarketReachable reports whether waypoint's system has a jump-gate route from
// the hull's CURRENT system, within the SAME bound buy()'s travel() itself enforces
// (sp-yuq9). Without this filter the need-rank picks the cheapest foreign
// market across EVERY scouted market_data row with no reachability check at all and
// hands it to travel() unchecked — TORWIND-38 repeatedly selected X1-PB12 as cheapest
// from X1-KA42, but PB12 has no jump-gate route within 5 jumps, so every relaunch
// crash-looped identically ("travel to market X1-PB12-C55F failed: no jump-gate route
// from X1-KA42 to X1-PB12 within 5 jumps") because scout posts kept PB12's ask fresh
// forever. This consults h.legs.gateGraphResolver() — the IDENTICAL cached GateGraph
// instance (gategraph.Service, bounded to MaxJumpPath) that travel()'s own jumpPath()
// uses — so there is exactly ONE notion of reachability in the codebase, never a second
// one invented here. The check is DB-only (Routable -> Path -> the cached fetch-through
// adjacency, sp-ikx1): no per-candidate live API call.
//
//   - Same system → trivially reachable, no consult needed (also keeps every
//     single-system pick() test passing unmodified with no gate graph wired).
//   - No gate graph wired (h.legs.gateGraphResolver() == nil) → fails OPEN, mirroring
//     jumpPath's own legacy single-hop fallback, so every caller that never wires one
//     (nearly all existing tests) is byte-for-byte unaffected.
//   - A wired graph that cannot resolve routability (a store/fetch error, NOT a
//     definitive unroutable verdict) fails CLOSED — an unverifiable route is no more
//     trustworthy than the unreadable market foreignMarketFresh already refuses.
//   - Otherwise, returns the graph's own Routable verdict directly.
func (h *RunStockerCoordinatorHandler) foreignMarketReachable(ctx context.Context, currentSystem, waypoint string, playerID int) bool {
	destSystem := shared.ExtractSystemSymbol(waypoint)
	if currentSystem == destSystem {
		return true
	}
	gateGraph := h.legs.gateGraphResolver()
	if gateGraph == nil {
		return true
	}
	routable, err := gateGraph.Routable(ctx, currentSystem, destSystem, playerID)
	if err != nil {
		return false
	}
	return routable
}

// recordNoReachableSource emits the sp-yuq9 "no reachable source" verdict for a hull at
// most ONCE per distinct (unreachable/total) signature: a hull whose need-rank keeps
// landing on unreachable-only candidates parks QUIETLY across hundreds of re-plans,
// logging one line, not one per pass (the same discipline that stopped the ikx1/13tl
// spam). A genuine state change — the unreachable count moves, or the hull later finds a
// reachable pick and clearNoReachableSource forgets the signature — re-emits.
// Concurrency-safe: the stocker handler is a shared singleton dispatched for every
// stocking hull at once.
func (h *RunStockerCoordinatorHandler) recordNoReachableSource(ctx context.Context, shipSymbol string, unreachable, totalRows int) {
	sig := fmt.Sprintf("%d/%d", unreachable, totalRows)
	h.noReachableSourceMu.Lock()
	if h.noReachableSource[shipSymbol] == sig {
		h.noReachableSourceMu.Unlock()
		return
	}
	h.noReachableSource[shipSymbol] = sig
	h.noReachableSourceMu.Unlock()
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Stocker parked: no reachable source (%d/%d ranked candidate(s) gate-unreachable from the hull's current system) - parking quietly rather than repeating an unreachable pick",
		unreachable, totalRows), map[string]interface{}{
		"ship_symbol": shipSymbol, "reason": "no_reachable_source", "unreachable_candidates": unreachable, "total_candidates": totalRows,
	})
}

// clearNoReachableSource forgets a hull's last no-reachable-source verdict so that,
// should the need-rank fall back into an unreachable-only state later, it logs afresh —
// the "or on state change" half of the once-per-hull discipline.
func (h *RunStockerCoordinatorHandler) clearNoReachableSource(shipSymbol string) {
	h.noReachableSourceMu.Lock()
	delete(h.noReachableSource, shipSymbol)
	h.noReachableSourceMu.Unlock()
}

// strandedReason reports whether the hull is ending laden with undeposited cargo — an
// honest-completion veto (the stocker's one job is to deposit, so ending with a load is a
// failure, whatever its provenance). It reads the PHYSICAL hull cargo (not a bought-this-run
// tally) so a hull that restarts laden and cannot deposit — warehouse full/gone — reports
// FAILED and the next run retries deposit-first. The message names each good, its units,
// and the hull's current location so the strand is greppable and hand-recoverable. A load
// that cannot be read does NOT false-veto (fail open on the read).
func (h *RunStockerCoordinatorHandler) strandedReason(ctx context.Context, cmd *RunStockerCoordinatorCommand) (string, bool) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return "", false
	}
	c := ship.Cargo()
	if c == nil {
		return "", false
	}
	var parts []string
	for _, item := range c.Inventory {
		if item.Units > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.Units, item.Symbol))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	return fmt.Sprintf("stranded cargo: %s still aboard at %s (undeposited) - reporting failure", strings.Join(parts, ", "), ship.CurrentLocation().Symbol), true
}
