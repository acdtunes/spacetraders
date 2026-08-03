package parkedsensing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// offgate.go is the LIVE driver for off-gate warp expansion.
//
// WHY IT IS A DRIVER AND NOT AN ENGINE. Every piece of off-gate expansion already existed and was
// already tested — the target ranking (adapters/expansion.OffGateWarpTargetSelector), the
// demand-to-buy bridge (ExplorerOffGateBridge, whose READ side the fleet autosizer's
// ExplorerDemandProvider is already wired to in main.go), the warp dispatcher
// (ExplorerWarpDispatcher over slice-A ExecuteWarpRoute), and the warp execution itself. What did
// not exist was anything alive to drive them: all of it hangs off the RETIRED frontier expansion
// coordinator, which refuses to start. The bridge is therefore permanently unwritten, so the
// autosizer's explorer demand reads UNREADABLE and fails closed forever.
//
// So this file writes NO ranking, NO bridge and NO dispatch. It re-uses those verbatim — including
// their types, so there is exactly one definition of an off-gate target in the codebase — and
// supplies the one missing thing: a trigger derived from the live sensing tick, and the calls that
// carry it to machinery that was already built.
//
// THE TRIGGER. Off-gate demand fires only when the gate-reachable frontier is exhausted: there is
// charting work outstanding and NOT ONE of it is within MaxWalkRings of a system we hold. That is
// the sensing-engine analogue of the retired coordinator's "gate-reachable virgin set exhausted"
// trigger, and it is strictly better in one respect: it is DERIVED FRESH from the ledger every tick
// (RULINGS #2) rather than debounced through a cross-tick empty-queue streak, so a restart cannot
// lose it and a stale streak cannot fire it.
//
// GATE ALWAYS WINS. A single gate-reachable target suppresses off-gate demand entirely. Warp is the
// expensive fallback — a 769k hull and a multi-system flight against a probe that walks a gate — so
// it must never be the default while a cheap ring remains. This is the property most worth guarding,
// and it is the one the fixtures below are built to be able to fail.
//
// IT SPENDS NOTHING. This driver raises a demand SIGNAL; the fleet autosizer owns the purchase and
// applies every money guard unchanged — the hard cap of 1, the price ceiling, and the big-ticket
// 25%-of-treasury rule that legitimately refuses at today's treasury. There is deliberately no
// second path to the treasury from here, exactly as expansion's seed requests have none.

// OffGateSelector ranks gate-unreachable systems and picks the best warp target.
//
// The signature is the RETIRED coordinator's port verbatim, so
// adapters/expansion.OffGateWarpTargetSelector satisfies it with no shim and no second ranking. Its
// scoring — exploration value against warp fuel from the nearest gate-connected system, bounded by
// warp range, with a symbol tiebreak for stability — is used exactly as written.
type OffGateSelector interface {
	SelectTarget(ctx context.Context, playerID int, params OffGateSelectionParams) (OffGateTarget, bool, error)
}

// OffGateDemandSink is the write side of the demand-to-buy bridge — the seam the fleet autosizer's
// explorer demand provider reads. adapters/expansion.ExplorerOffGateBridge satisfies it.
//
// IT IS WRITTEN ON EVERY TICK, including the no-demand ticks, and that is load-bearing rather than
// tidy. The bridge reads UNREADABLE until its first write for a player, and the autosizer fails
// CLOSED on unreadable — so a driver that only wrote when demand fired would leave the buy side
// permanently blind on a fleet that never triggers, which is indistinguishable from the retirement
// this file exists to undo.
type OffGateDemandSink interface {
	EmitOffGateDemand(playerID int, signal OffGateDemandSignal)
}

// ExplorerFinder returns an idle, warp-capable, explorer-dedicated hull, if the fleet holds one.
// found=false is the normal state before the autosizer has bought one.
type ExplorerFinder interface {
	IdleExplorer(ctx context.Context, playerID int) (shipSymbol string, found bool, err error)
}

// ExplorerDispatcher warps an explorer to an off-gate target.
// adapters/expansion.ExplorerWarpDispatcher satisfies it.
//
// IT MUST NOT BLOCK, and the existing adapter does not: it resolves the arrival waypoint FIRST and
// fails closed if the target system's waypoints are unknown, then runs the warp itself on a
// background goroutine, so an unreachable destination never buys a wasted flight and a multi-minute
// warp never holds the tick open. Both properties mirror RouteAcross, and both belong to the
// ADAPTER rather than to this interface — a replacement must be checked for them, never assumed
// from its name.
type ExplorerDispatcher interface {
	DispatchExplorer(ctx context.Context, playerID int, shipSymbol string, target OffGateTarget) error
}

// OffGatePorts are the off-gate slice's collaborators. All four are wired in the daemon; a nil one
// is a WIRING GAP, not a feature switch, and the slice degrades to doing nothing rather than
// panicking — see advanceOffGate.
type OffGatePorts struct {
	Select   OffGateSelector
	Demand   OffGateDemandSink
	Explorer ExplorerFinder
	Warp     ExplorerDispatcher
}

// wired reports whether the slice can run at all.
func (o OffGatePorts) wired() bool {
	return o.Select != nil && o.Demand != nil && o.Explorer != nil && o.Warp != nil
}

// The ranking inputs handed to the off-gate selector.
//
// PLAIN CONSTANTS, deliberately not knobs. They pace an exploration choice, not an economic one —
// the money decision is the autosizer's and is guarded there — and the retired coordinator's own
// config carried them only because it had a config to carry them in. A knob nobody sets is another
// arming step, which is the failure mode this whole file exists to undo.
//
// WarpRangeFuel is bounded by the SHIP_EXPLORER's fuel capacity of 800 (read live from the shipyard
// API), so a target this admits is one the hull can actually reach on a full tank; the warp executor
// tops off before departure and independently refuses a leg that would strand
// (ErrWarpWouldStrand), so this bound is the cheap first filter rather than the safety one.
const (
	offGateWarpRangeFuel = 800
	offGateValueWeight   = 100
	offGateFuelWeight    = 1
)

// retractOffGateDemand withdraws any standing explorer demand, and it is what a spend pause does
// INSTEAD OF simply not running advanceOffGate.
//
// THE BRIDGE LATCHES, and that is the whole reason this exists. ExplorerOffGateBridge keeps the last
// signal it was handed and answers the autosizer from it on every read, forever, until something
// overwrites it. So a pause that merely SKIPPED the emit would leave a `Demanded: true` raised
// moments earlier standing indefinitely — and the autosizer, reading it, would buy a 769k explorer
// while the operator had just said stop spending. Not emitting is not the same as not demanding, and
// the difference is a purchase.
//
// So the pause retracts. It is the one write the paused half of the tick makes, it costs a map
// assignment, and it moves strictly in the money-safe direction (RULINGS #4): a zero signal can only
// ever cause FEWER purchases than the one it replaces.
//
// GUARDED ON THE SINK ALONE, not on wired(). A retraction needs nothing but somewhere to write, and
// requiring the selector, the finder and the dispatcher too would mean a partial wiring gap left a
// latched demand standing — the exact failure this function prevents, reintroduced through the
// nil-check. The other three are only needed to RAISE demand, which the pause never does.
func retractOffGateDemand(p ExpandPorts, playerID int) {
	if p.OffGate.Demand == nil {
		return
	}
	p.OffGate.Demand.EmitOffGateDemand(playerID, OffGateDemandSignal{})
}

// advanceOffGate is the off-gate slice: one call from AdvanceExpansion, after the gate-based passes
// have had their turn.
//
// gateReachable is whether ANY outstanding charting target is within gate reach this tick. When it
// is, this emits NO demand and returns — the cheap frontier is still moving and warp must not
// compete with it.
func advanceOffGate(
	ctx context.Context,
	p ExpandPorts,
	playerID int,
	targets []ExpandSystem,
	gateReachable bool,
	rep *ExpandReport,
) {
	off := p.OffGate
	if !off.wired() {
		// A wiring gap, not a switch. Silent rather than logged per-tick because the daemon wires
		// all four together and the unit tests that omit them are testing the gate passes.
		return
	}

	// NO CHARTING WORK AT ALL, or work the gates can still serve: no off-gate demand. Both are
	// written to the bridge rather than skipped, so the autosizer's read is readable-and-zero
	// instead of unreadable-and-fail-closed.
	if len(targets) == 0 || gateReachable {
		off.Demand.EmitOffGateDemand(playerID, OffGateDemandSignal{})
		return
	}

	signal := OffGateDemandSignal{
		Demanded:      true,
		ExplorerCount: 1, // hard cap of one; ExplorerDemandProvider clamps its want to this, and that clamp is the ONLY enforcement — no class ceiling backs it up
		Reason:        fmt.Sprintf("gate-reachable frontier exhausted (%d target(s) outstanding, none within %d gate hops)", len(targets), MaxWalkRings),
	}

	target, found, err := off.Select.SelectTarget(ctx, playerID, OffGateSelectionParams{
		WarpRangeFuel: offGateWarpRangeFuel,
		ValueWeight:   offGateValueWeight,
		FuelWeight:    offGateFuelWeight,
	})
	switch {
	case err != nil:
		// The demand still stands — the frontier IS exhausted — but there is no target to warp to
		// this tick. Logged rather than swallowed: a persistently unreadable selector is a wiring
		// bug that would otherwise present as a fleet that quietly never explores.
		logging.LoggerFromContext(ctx).Log("WARN", "off-gate target selection failed; explorer demand stands but no warp target this tick", map[string]interface{}{
			"action": "parked_sensing_off_gate_select_failed",
			"error":  err.Error(),
		})
	case found:
		signal.HasTarget = true
		signal.Target = target
	}

	off.Demand.EmitOffGateDemand(playerID, signal)
	rep.OffGateDemanded = true
	rep.OffGateTarget = signal.Target.SystemSymbol

	if !signal.HasTarget {
		return
	}
	dispatchExplorer(ctx, off, playerID, signal.Target, rep)
}

// dispatchExplorer warps the idle explorer, if the fleet has one, at the selected target.
//
// NOTHING HERE BUYS. Before the autosizer has bought an explorer there is simply no hull to find,
// and that is the ordinary state — the demand signal above is what asks for one, under every money
// guard the autosizer applies. An explorer already in transit is not idle, so it is never
// re-dispatched mid-warp; when it arrives it is idle again and the next tick sends it to the next
// off-gate target, which is what makes it advance rather than park.
func dispatchExplorer(
	ctx context.Context,
	off OffGatePorts,
	playerID int,
	target OffGateTarget,
	rep *ExpandReport,
) {
	explorer, found, err := off.Explorer.IdleExplorer(ctx, playerID)
	if err != nil {
		logging.LoggerFromContext(ctx).Log("WARN", "off-gate dispatch could not read the explorer fleet; no warp this tick", map[string]interface{}{
			"action": "parked_sensing_off_gate_explorer_read_failed",
			"error":  err.Error(),
		})
		return
	}
	if !found {
		return // none owned yet, or the one we own is already warping
	}

	if err := off.Warp.DispatchExplorer(ctx, playerID, explorer, target); err != nil {
		// Non-fatal by design: a refused warp leaves the hull exactly where it was, and the next
		// tick re-derives and retries for free. The adapter fails closed BEFORE moving anything, so
		// a refusal here has cost no fuel and stranded nothing.
		logging.LoggerFromContext(ctx).Log("WARN", "off-gate warp dispatch refused; the explorer stays put and the next tick retries", map[string]interface{}{
			"action":        "parked_sensing_off_gate_dispatch_failed",
			"ship_symbol":   explorer,
			"target_system": target.SystemSymbol,
			"error":         err.Error(),
		})
		return
	}
	rep.OffGateWarped++
	logging.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("warping explorer %s to off-gate system %s (from %s, warp fuel %d)", explorer, target.SystemSymbol, target.FromSystem, target.WarpFuelCost), map[string]interface{}{
		"action":        "parked_sensing_off_gate_warp",
		"ship_symbol":   explorer,
		"target_system": target.SystemSymbol,
		"from_system":   target.FromSystem,
		"warp_fuel":     target.WarpFuelCost,
	})
}
