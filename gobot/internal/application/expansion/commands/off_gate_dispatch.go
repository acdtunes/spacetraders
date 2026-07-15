package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// This file holds the sp-a3yn slice-C explorer BUY seam (the bridge sink) and DISPATCH loop — the
// two hooks the frontier coordinator drives after it computes the off-gate demand signal. Both are
// optional-injection add-ons reached from ONE new line in ReconcileOnce (the frontier_depth_policy /
// off_gate_demand idiom), so an unwired coordinator behaves byte-identically to pre-slice-C.
//
//   - BUY (cross-coordinator bridge): the FLEET autosizer, not this coordinator, buys the explorer.
//     It reads off-gate demand through a shared bridge (OffGateDemandSink write side ↔ the autosizer's
//     OffGateDemandSource read side — the contract_delivery_bridge idiom). This coordinator only
//     PUBLISHES its per-tick signal to the sink; it never spends a credit.
//   - DISPATCH: once the autosizer has bought+dedicated an explorer, THIS loop warps the idle hull to
//     the selected off-gate target via slice-A ExecuteWarpRoute. On arrival slice A charts the system,
//     so growFrontierGraph reaches the new cluster next cycle and the cheap probe frontier resumes.
//     An explorer in transit is not idle, so it is never re-dispatched mid-flight; once it arrives it
//     either ADVANCES to the next off-gate target (demand still firing) or PARKS (frontier resumed).

// explorerFleetTag is the dedication tag the autosizer's dedicate-at-purchase stamps on a bought
// explorer (autosizerDedicatedFleet(HullClassExplorer) == "explorer"). Kept in sync by value; a
// cross-package drift would only make the dispatch loop find no explorer (fail-safe: no warp), never
// warp the wrong hull.
const explorerFleetTag = "explorer"

// OffGateDemandSink is the write side of the buy-path bridge: the frontier coordinator mirrors each
// tick's off-gate demand signal here so the fleet autosizer's explorer demand provider (the read
// side) can gate its buy on it. Optional (a nil sink makes the emit a no-op).
type OffGateDemandSink interface {
	EmitOffGateDemand(playerID int, signal OffGateDemandSignal)
}

// ExplorerDispatchPort warps a bought+dedicated explorer to an off-gate target and charts it on
// arrival — satisfied by an adapter over slice-A's RouteExecutor.ExecuteWarpRoute. Driven port; faked
// at the boundary in tests. A returned error is logged and swallowed (a failed warp never aborts the
// reconcile pass).
type ExplorerDispatchPort interface {
	DispatchExplorer(ctx context.Context, playerID int, shipSymbol string, target OffGateTarget) error
}

// SetOffGateDemandSink wires the buy-path bridge (sp-a3yn). Leaving it unset publishes nowhere — the
// autosizer then reads no explorer demand and buys nothing (fail-safe).
func (h *RunFrontierExpansionCoordinatorHandler) SetOffGateDemandSink(s OffGateDemandSink) {
	h.offGateSink = s
}

// SetExplorerDispatchPort wires the slice-A warp dispatch (sp-a3yn). Leaving it unset makes the
// dispatch loop a no-op — a bought explorer simply sits idle until the port is wired.
func (h *RunFrontierExpansionCoordinatorHandler) SetExplorerDispatchPort(p ExplorerDispatchPort) {
	h.explorerDispatch = p
}

// emitOffGateDemand publishes the tick's signal to the buy-path bridge (nil-safe).
func (h *RunFrontierExpansionCoordinatorHandler) emitOffGateDemand(playerID int, signal OffGateDemandSignal) {
	if h.offGateSink == nil {
		return
	}
	h.offGateSink.EmitOffGateDemand(playerID, signal)
}

// dispatchOffGateExplorer warps a bought+dedicated idle explorer to this cycle's off-gate target. It
// is gated on BOTH the off-gate demand firing (with a reachable target) AND an idle dedicated explorer
// existing — so it dispatches nothing on a bare deploy (no demand) and nothing before the autosizer
// buys one (no explorer). Idempotent across ticks by construction: an explorer in transit is not
// idle, so it is not re-dispatched until it arrives and either advances or parks.
func (h *RunFrontierExpansionCoordinatorHandler) dispatchOffGateExplorer(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) {
	if h.explorerDispatch == nil {
		return
	}
	signal, ok := h.OffGateDemand(cmd.PlayerID.Value())
	if !ok || !signal.Demanded || !signal.HasTarget {
		return // no off-gate demand / no reachable target this cycle → the explorer parks
	}

	logger := common.LoggerFromContext(ctx)
	explorer, found, err := h.idleDedicatedExplorer(ctx, cmd)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Off-gate dispatch: idle-explorer read failed (no warp this cycle): %v", err), map[string]interface{}{
			"action": "off_gate_dispatch_read_failed", "container_id": cmd.ContainerID,
		})
		return
	}
	if !found {
		return // no bought+dedicated explorer yet — the autosizer buys one; nothing to warp
	}

	if err := h.explorerDispatch.DispatchExplorer(ctx, cmd.PlayerID.Value(), explorer.ShipSymbol(), signal.Target); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Off-gate dispatch of %s → %s failed (non-fatal): %v", explorer.ShipSymbol(), signal.Target.SystemSymbol, err), map[string]interface{}{
			"action": "off_gate_dispatch_failed", "container_id": cmd.ContainerID, "ship": explorer.ShipSymbol(), "target_system": signal.Target.SystemSymbol,
		})
		return
	}
	logger.Log("INFO", fmt.Sprintf("Off-gate dispatch: warping explorer %s → %s (from %s, fuel %d) to chart off-gate; growFrontierGraph resumes on arrival", explorer.ShipSymbol(), signal.Target.SystemSymbol, signal.Target.FromSystem, signal.Target.WarpFuelCost), map[string]interface{}{
		"action": "off_gate_dispatch", "container_id": cmd.ContainerID, "ship": explorer.ShipSymbol(),
		"target_system": signal.Target.SystemSymbol, "from_system": signal.Target.FromSystem, "warp_fuel": signal.Target.WarpFuelCost,
	})
}

// idleDedicatedExplorer returns the first idle, warp-capable, "explorer"-dedicated hull — the
// bought+dedicated explorer waiting to be warped. It reads only idle hulls (an in-transit explorer is
// excluded, which is what makes the dispatch idempotent) and requires the warp drive so a mis-tagged
// non-warp hull is never handed to ExecuteWarpRoute. found=false when none exists.
func (h *RunFrontierExpansionCoordinatorHandler) idleDedicatedExplorer(ctx context.Context, cmd *RunFrontierExpansionCoordinatorCommand) (*navigation.Ship, bool, error) {
	ships, err := h.fleetRepo.FindIdleByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to find idle ships: %w", err)
	}
	for _, ship := range ships {
		if ship.DedicatedFleet() == explorerFleetTag && ship.HasWarpDrive() {
			return ship, true, nil
		}
	}
	return nil, false, nil
}
