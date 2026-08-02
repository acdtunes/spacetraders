package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateUnchartedMarketSystems is the retroactive-backlog enumeration: the systems that
// are MARKET-KNOWN (a key in marketAges — the market repository swept at least one of
// its markets) but GATE-UNCHARTED (NOT a key with real edges in charted — the Adjacency
// key set is exactly the systems whose jump gate is charted). This is the market-swept-but-
// gate-empty set the strict pathfinder fails closed on, that chart-on-arrival alone cannot
// reach (such a system is never revisited once swept — the chicken-and-egg). A system with
// an empty edge slice is treated as uncharted (a "connects nowhere" set is not a charted
// gate). Sorted for deterministic, stable dispatch order. Pure — no store, no API.
func gateUnchartedMarketSystems(marketAges map[string]float64, charted map[string][]system.GateEdge) []string {
	uncharted := make([]string, 0, len(marketAges))
	for systemSymbol := range marketAges {
		if len(charted[systemSymbol]) > 0 {
			continue // its jump gate is already charted — not part of the backlog
		}
		uncharted = append(uncharted, systemSymbol)
	}
	sort.Strings(uncharted)
	return uncharted
}

// gateChartSweepTargets GENERALIZES the market-only enumeration to a widened target set:
// the UNION of the market-known-but-gate-uncharted backlog (gateUnchartedMarketSystems) and the
// traffic-markered uncharted TRANSIT systems (markeredGates — the era-scoped backoff markers a
// stale route-through 400 left behind), each minus the already-charted set. Deduped (a system
// that is BOTH market-known and markered is one target, drawing one probe) and sorted for a
// deterministic, stable dispatch order. markeredGates nil (an unwired provider or the
// disable-escape) collapses this to exactly gateUnchartedMarketSystems, the market-only
// behavior. Pure — no store, no API.
func gateChartSweepTargets(marketAges map[string]float64, markeredGates map[string]string, charted map[string][]system.GateEdge) []string {
	seen := make(map[string]struct{}, len(marketAges)+len(markeredGates))
	for _, systemSymbol := range gateUnchartedMarketSystems(marketAges, charted) {
		seen[systemSymbol] = struct{}{}
	}
	for systemSymbol := range markeredGates {
		if len(charted[systemSymbol]) > 0 {
			continue // its jump gate is already charted — a lingering marker is not a target
		}
		seen[systemSymbol] = struct{}{}
	}
	targets := make([]string, 0, len(seen))
	for systemSymbol := range seen {
		targets = append(targets, systemSymbol)
	}
	sort.Strings(targets)
	return targets
}

// reconcileGateChartSweep is the RETROACTIVE gate-reconcile pass (Part 2): it dispatches
// up to a BOUNDED number of LEFTOVER idle probes to UNCHARTED frontier gates so each
// probe lands on that system's jump gate and Part 1's chart-on-arrival
// (chartArrivedGate -> ChartPresentGate) fills its gate_edges. The target set is the UNION of
// two sources (gateChartSweepTargets):
//   - the market-known-but-gate-uncharted backlog (a market-swept system with empty
//     gate_edges the strict pathfinder strands hulls on);
//   - the traffic-markered MARKETLESS transit gates: uncharted systems a stale backoff
//     marker proves fleet traffic jumps THROUGH (MarkUnreadable is written on a real GetJumpGate
//     400). The market-scoped enumeration structurally can NEVER reach these — a system's
//     market status is unknown until its gate is charted. A marketless dead-end no route
//     crosses is never markered, so the scope stays bounded to traffic-touched gates (NOT
//     all reachable uncharted gates — that over-exploration is rejected).
//
// A market target is aimed at any market waypoint (Part 1 charts the GATE on the pre-market
// arrival hop); a marketless target is aimed at the gate WAYPOINT the marker recorded.
//
// SAFETY (HARD API-budget constraint):
//   - DEFAULT OFF (self-guards on GateReconcileEnabled): deploy-inert until armed. The marketless
//     widening is additionally reversible live via GateReconcileMarketlessDisabled (default ON).
//   - HARD-CAPPED at resolveGateReconcileMaxDispatch relays per tick — never a burst.
//   - Runs on the LEFTOVER idle pool AFTER manning (idleSats already drained by Pass 2),
//     so it never starves a post of a probe; a dispatched probe is spliced out of the pool.
//   - Idempotent: ChartPresentGate's store-first guard (Part 1) makes an arrival on an
//     already-charted system cost ZERO API, so a redundant relay is cheap.
//   - Fail-closed at every gap (no gate graph / no freshness provider / read error / no
//     routable satellite / no destination), and the per-target dispatch backoff prevents churn.
func (h *RunScoutPostCoordinatorHandler) reconcileGateChartSweep(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, idleSats *[]*navigation.Ship) {
	if !cmd.GateReconcileEnabled || h.gateGraph == nil || h.marketFreshnessProvider == nil {
		return
	}
	if len(*idleSats) == 0 {
		return // no leftover probe to spend on the backlog this tick
	}
	logger := common.LoggerFromContext(ctx)

	marketAges, err := h.marketFreshnessProvider.MaxAgeSecondsBySystem(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("gate-reconcile: market enumeration failed — skipping sweep this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_enumeration_failed",
		})
		return
	}
	charted, err := h.gateGraph.Adjacency(ctx)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("gate-reconcile: gate adjacency read failed — skipping sweep this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_adjacency_failed",
		})
		return
	}

	markeredGates := h.markeredUnchartedGates(ctx, cmd)

	targets := gateChartSweepTargets(marketAges, markeredGates, charted)
	if len(targets) == 0 {
		return // frontier fully charted — nothing to reconcile
	}

	maxDispatch := resolveGateReconcileMaxDispatch(cmd)
	maxJumps := resolveMaxRepositionJumps(cmd)
	dispatched := 0
	for _, target := range targets {
		if dispatched >= maxDispatch {
			break // rate-budget cap reached — the rest waits for the next tick
		}
		if len(*idleSats) == 0 {
			break // idle pool exhausted
		}
		key := gateReconcileBackoffKey(cmd.PlayerID.Value(), target)
		if h.repositionBackedOff(key) {
			continue // a relay for this target is already in flight / recently dispatched
		}
		idx, hops, ok := h.selectNearestSatelliteByHops(ctx, *idleSats, target, maxJumps)
		if !ok {
			continue // no idle probe can jump-route to this frontier system this tick
		}
		destWaypoint, ok := h.resolveGateChartDestination(ctx, target, markeredGates)
		if !ok {
			continue // no market waypoint AND no recorded gate to aim at right now — fail closed
		}

		sat := (*idleSats)[idx]
		*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)
		shipSymbol := sat.ShipSymbol()

		// A 0-hop dispatch (the probe is ALREADY in the target system) must chart the gate
		// itself — travelWithJumpBound's same-system branch would otherwise navigate it to
		// a market and return before charting, so the backlog never drains. A multi-hop
		// dispatch (hops>0) already charts on the jump-arrival hop, so it stays the plain relay.
		relayID, err := h.spawnReposition(ctx, cmd, shipSymbol, destWaypoint, maxJumps, hops == 0)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("gate-reconcile: failed to dispatch %s to chart %s: %v", shipSymbol, target, err), map[string]interface{}{
				"action":        "gate_reconcile_dispatch_failed",
				"system_symbol": target,
				"ship_symbol":   shipSymbol,
			})
			continue
		}
		h.noteRepositionDispatch(key)
		dispatched++
		logger.Log("INFO", fmt.Sprintf("gate-reconcile: repositioning %s → %s (%d jump(s), ≤%d bound, relay %s) to chart its jump gate on arrival", shipSymbol, target, hops, maxJumps, relayID), map[string]interface{}{
			"action":        "gate_reconcile_dispatch",
			"system_symbol": target,
			"ship_symbol":   shipSymbol,
			"jumps":         hops,
			"max_jumps":     maxJumps,
			"destination":   destWaypoint,
			"relay":         relayID,
		})
	}
}

// markeredUnchartedGates reads the traffic-marker set (system -> recorded gate waypoint)
// the widened sweep charts alongside the market backlog. It fails SAFE to a market-only
// sweep at every gap: an unwired provider (nil) or the GateReconcileMarketlessDisabled
// escape returns nil (no marketless targets), and a provider read error is logged and swallowed
// (market-only this tick, never an aborted sweep) — mirroring the marketFreshnessProvider's
// fail-open contract. nil is a valid, empty markered set for gateChartSweepTargets.
func (h *RunScoutPostCoordinatorHandler) markeredUnchartedGates(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) map[string]string {
	if h.unreadableGateProvider == nil || cmd.GateReconcileMarketlessDisabled {
		return nil // widening unwired or pinned off — market-only
	}
	gates, err := h.unreadableGateProvider.UnreadableGates(ctx)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("gate-reconcile: marker enumeration failed — charting market-only this tick: %v", err), map[string]interface{}{
			"action": "gate_reconcile_marker_enumeration_failed",
		})
		return nil
	}
	return gates
}

// resolveGateChartDestination picks the waypoint to aim a charting probe at: any market in
// the target (Part 1 charts the GATE on the pre-market arrival hop) when one exists, else
// the gate WAYPOINT the backoff marker recorded (the marketless-transit path). Trying
// markets FIRST keeps a market target's destination stable. ok=false when neither is
// available — no market waypoint yet AND no recorded gate (or an empty-string marker) —
// so the caller fails closed for this target without consuming a probe.
func (h *RunScoutPostCoordinatorHandler) resolveGateChartDestination(ctx context.Context, target string, markeredGates map[string]string) (string, bool) {
	markets, err := h.discoverMarkets(ctx, target)
	if err == nil && len(markets) > 0 {
		return pickRepositionDestination(markets), true
	}
	if gateWaypoint := markeredGates[target]; gateWaypoint != "" {
		return gateWaypoint, true
	}
	return "", false
}

// gateReconcileBackoffKey is the per-target dispatch-backoff key for the gate-chart sweep.
// It is DISTINCT from a post slot's backoffKey (a "gatereconcile|" prefix) so a gate-reconcile
// relay and a post reposition to the same system never share a backoff window — the two are
// independent dispatch decisions that happen to reuse the shared repositionBackoffUntil map.
func gateReconcileBackoffKey(playerID int, systemSymbol string) string {
	return fmt.Sprintf("gatereconcile|%d|%s", playerID, systemSymbol)
}
