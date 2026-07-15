package commands

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// This file holds the sp-k645 slice-B OFF-GATE DEMAND SIGNAL: the frontier coordinator's
// hook that raises "explorer demand" — a flag, a count, and a selected warp target — when
// the gate-reachable frontier can no longer serve the fleet's need to expand. It is a
// SIGNAL ONLY: slice B never warps, buys, or dispatches. Slice C reads OffGateDemand(playerID)
// and acts (buys the explorer hull, dispatches the warp). Two triggers raise the signal:
//
//   (a) the gate-reachable virgin set is EXHAUSTED — the expansion queue has been empty (no
//       new gate ring opened) for N consecutive cycles (debounced so a one-cycle dip never
//       fires); OR
//   (b) the fleet has a heavy-capacity shortfall it cannot buy — heavyShortfall > 0 AND no
//       heavy yard is known — AND the gate-reachable shipyards are scan-exhausted, so a
//       missing heavy yard is CONCLUSIVE (not merely undiscovered). It never fires while
//       shipyard coverage is still sparse (a heavy yard might yet be found on-gate).
//
// Like the depth slice (frontier_depth_policy.go) this is a self-contained add-on to
// ReconcileOnce reached from ONE new line, with its collaborators wired by optional
// injection. Its ports are faked at the boundary in tests; the real adapters live in
// internal/adapters/expansion.

// OffGateTarget is the selected warp-exploration target the demand signal carries for slice
// C: an off-gate system, the frontier system a warp would launch FROM (the nearest
// gate-connected system — the frontier edge), the warp fuel that leg costs (slice A's
// CRUISE fuel model), and the exploration value that ranked it.
type OffGateTarget struct {
	SystemSymbol string
	X            float64
	Y            float64
	FromSystem   string
	WarpFuelCost int
	Value        int
}

// OffGateSelectionParams are the tunable ranking inputs for target selection: the warp-range
// bound (a leg costing more fuel than this is out of range and excluded), and the value and
// fuel weights that trade exploration value off against warp distance in the score.
type OffGateSelectionParams struct {
	WarpRangeFuel int
	ValueWeight   int
	FuelWeight    int
}

// OffGateTargetSelector ranks off-gate systems (universe systems NOT on our gate network)
// by warp-fuel distance from the frontier edge and exploration value, and picks the
// nearest-highest-value one within warp range. Driven port; the adapter joins the universe
// roster against the gate graph. found=false means no reachable off-gate target exists.
type OffGateTargetSelector interface {
	SelectTarget(ctx context.Context, playerID int, params OffGateSelectionParams) (target OffGateTarget, found bool, err error)
}

// ShipyardCoverageReader reports whether the gate-reachable shipyards have been scanned
// thoroughly enough that a missing heavy yard is CONCLUSIVE — the (b) trigger's guard.
// While coverage is still sparse it returns exhausted=false so the signal does not fire
// prematurely. readable=false ⇒ the signal is unreadable; the caller treats coverage as
// sparse (fail-safe: do not fire).
type ShipyardCoverageReader interface {
	GateShipyardsScanExhausted(ctx context.Context, playerID int) (exhausted bool, readable bool, err error)
}

// OffGateDemandSignal is the off-gate explorer demand the frontier coordinator raises for
// slice C to consume. Demanded is the flag; ExplorerCount the count of explorers wanted;
// Reason the human trigger ("queue exhausted N cycles" / "heavy-yard hunt off-gate"); and
// Target the selected warp target, present (HasTarget) iff a reachable off-gate system
// exists within warp range. A zero value (Demanded=false) means no demand this cycle.
type OffGateDemandSignal struct {
	Demanded      bool
	ExplorerCount int
	Reason        string
	HasTarget     bool
	Target        OffGateTarget
}

// offGateDemandTracker holds the coordinator's ONLY cross-tick state for this slice: the
// per-player streak of consecutive cycles the gate-reachable expansion queue has been empty
// (the trigger-(a) debounce), and the latest computed signal (exposed to slice C). Every
// other coordinator decision derives fresh from repositories each pass; this streak is the
// analogue of Handle's errMon, kept here (keyed by player) because the queue it debounces is
// built inside ReconcileOnce, not visible to Handle. Guarded by a mutex — the handler is a
// registered singleton serving every player's ticks.
type offGateDemandTracker struct {
	mu          sync.Mutex
	emptyStreak map[int]int
	latest      map[int]OffGateDemandSignal
}

func newOffGateDemandTracker() *offGateDemandTracker {
	return &offGateDemandTracker{
		emptyStreak: make(map[int]int),
		latest:      make(map[int]OffGateDemandSignal),
	}
}

// advanceQueueStreak increments the empty-queue streak when the queue is empty this cycle,
// resets it to 0 when the queue has entries (a new ring opened), and returns the result.
func (t *offGateDemandTracker) advanceQueueStreak(playerID int, queueEmpty bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !queueEmpty {
		t.emptyStreak[playerID] = 0
		return 0
	}
	t.emptyStreak[playerID]++
	return t.emptyStreak[playerID]
}

func (t *offGateDemandTracker) setLatest(playerID int, signal OffGateDemandSignal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.latest[playerID] = signal
}

func (t *offGateDemandTracker) get(playerID int) (OffGateDemandSignal, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	signal, ok := t.latest[playerID]
	return signal, ok
}

// SetOffGateTargetSelector wires the off-gate warp-target selector and arms the demand hook
// (sp-k645). Leaving it unset makes evaluateOffGateDemand a no-op — the coordinator behaves
// byte-identically to pre-slice-B.
func (h *RunFrontierExpansionCoordinatorHandler) SetOffGateTargetSelector(s OffGateTargetSelector) {
	h.offGateSelector = s
	if h.offGate == nil {
		h.offGate = newOffGateDemandTracker()
	}
}

// SetShipyardCoverageReader wires the gate-shipyard scan-exhaustion guard for trigger (b).
// Leaving it unset (or an unreadable reader) suppresses trigger (b) — fail-safe.
func (h *RunFrontierExpansionCoordinatorHandler) SetShipyardCoverageReader(r ShipyardCoverageReader) {
	h.shipyardCoverage = r
}

// OffGateDemand returns the latest off-gate explorer demand signal for a player — the seam
// slice C reads to decide whether to buy an explorer and warp it to Target. ok=false when the
// hook is unwired or has not evaluated for this player yet.
func (h *RunFrontierExpansionCoordinatorHandler) OffGateDemand(playerID int) (OffGateDemandSignal, bool) {
	if h.offGate == nil {
		return OffGateDemandSignal{}, false
	}
	return h.offGate.get(playerID)
}

// evaluateOffGateDemand is the ReconcileOnce hook (ONE call). It advances the empty-queue
// streak, evaluates the two triggers, selects a warp target when either fires, and records the
// signal for slice C. Signal-only: it NEVER warps, buys, or dispatches. A nil selector short-
// circuits so the unwired coordinator is unchanged.
func (h *RunFrontierExpansionCoordinatorHandler) evaluateOffGateDemand(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig, queueLen int) {
	if h.offGateSelector == nil || h.offGate == nil {
		return
	}
	playerID := cmd.PlayerID.Value()
	streak := h.offGate.advanceQueueStreak(playerID, queueLen == 0)

	reason, triggered := h.offGateTrigger(ctx, cmd, cfg, streak)
	if !triggered {
		h.offGate.setLatest(playerID, OffGateDemandSignal{})
		h.emitOffGateDemand(playerID, OffGateDemandSignal{}) // bridge the "no demand" out to the buy side too
		return
	}

	signal := OffGateDemandSignal{Demanded: true, ExplorerCount: 1, Reason: reason}
	target, found, err := h.offGateSelector.SelectTarget(ctx, playerID, OffGateSelectionParams{
		WarpRangeFuel: cfg.OffGateWarpRangeFuel,
		ValueWeight:   cfg.OffGateValueWeight,
		FuelWeight:    cfg.OffGateFuelWeight,
	})
	switch {
	case err != nil:
		// Demand is still raised (the trigger fired); the target is just unavailable this cycle.
		// Log loudly rather than swallowing — a persistently unreadable selector is a wiring bug.
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Off-gate target selection failed (demand raised, no target this cycle): %v", err), map[string]interface{}{
			"action":       "off_gate_target_select_failed",
			"container_id": cmd.ContainerID,
		})
	case found:
		signal.HasTarget = true
		signal.Target = target
	}
	h.offGate.setLatest(playerID, signal)
	h.emitOffGateDemand(playerID, signal) // bridge the raised signal out to the fleet autosizer's buy side
	h.logOffGateDemand(ctx, cmd, signal)
}

// offGateTrigger evaluates the two demand triggers and returns the human reason plus whether
// EITHER fired: (a) the gate-reachable virgin set is exhausted (empty-queue streak has reached
// N), or (b) the fleet needs a heavy yard it cannot buy AND gate shipyard coverage is
// exhausted. (a) is checked first (cheaper — no I/O).
func (h *RunFrontierExpansionCoordinatorHandler) offGateTrigger(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, cfg frontierConfig, streak int) (string, bool) {
	if streak >= cfg.OffGateQueueExhaustionCycles {
		return fmt.Sprintf("gate-reachable virgin set exhausted (empty queue %d cycles)", streak), true
	}
	if h.heavyYardHuntExhausted(ctx, cmd) {
		return "heavy-yard shortfall with gate shipyards scan-exhausted — hunt off-gate", true
	}
	return "", false
}

// heavyYardHuntExhausted is trigger (b): the fleet has a heavy-capacity shortfall it cannot
// buy (heavyShortfall > 0 AND no heavy yard known — reusing the sp-rjgr DepthObjectiveReader)
// AND the gate-reachable shipyards are scan-exhausted, so a missing heavy yard is conclusive.
// Every input fails SAFE to "do not fire": a nil/unreadable objective, a nil/unreadable/sparse
// coverage reader, or a met objective all return false.
func (h *RunFrontierExpansionCoordinatorHandler) heavyYardHuntExhausted(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) bool {
	if h.objective == nil || h.shipyardCoverage == nil {
		return false
	}
	shortfall, yardKnown, readable, err := h.objective.HeavyYardObjective(ctx, cmd.PlayerID.Value())
	if err != nil || !readable {
		return false
	}
	if shortfall <= 0 || yardKnown {
		return false
	}
	exhausted, coverageReadable, err := h.shipyardCoverage.GateShipyardsScanExhausted(ctx, cmd.PlayerID.Value())
	if err != nil || !coverageReadable {
		return false
	}
	return exhausted
}

// logOffGateDemand emits one concise line per cycle the signal is raised — the operator's
// window on a signal slice C has not yet wired to an action.
func (h *RunFrontierExpansionCoordinatorHandler) logOffGateDemand(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand, signal OffGateDemandSignal) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Off-gate explorer demand raised: %s (target=%s, has_target=%v) — signal for slice C", signal.Reason, signal.Target.SystemSymbol, signal.HasTarget), map[string]interface{}{
		"action":        "off_gate_demand_raised",
		"container_id":  cmd.ContainerID,
		"reason":        signal.Reason,
		"target_system": signal.Target.SystemSymbol,
		"from_system":   signal.Target.FromSystem,
		"warp_fuel":     signal.Target.WarpFuelCost,
		"has_target":    signal.HasTarget,
	})
}
