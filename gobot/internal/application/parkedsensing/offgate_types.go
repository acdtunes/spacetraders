package parkedsensing

import (
	"context"
)

// This file holds the OFF-GATE DEMAND SIGNAL: the frontier coordinator's hook that raises
// "explorer demand" — a flag, a count, and a selected warp target — when the gate-reachable
// frontier can no longer serve the fleet's need to expand. It is a SIGNAL ONLY: nothing here
// warps, buys, or dispatches; the consumer reads OffGateDemand(playerID) and acts. Two
// triggers raise it:
//
//   (a) the gate-reachable virgin set is EXHAUSTED — the expansion queue has been empty for N
//       consecutive cycles, debounced so a one-cycle dip never fires; OR
//   (b) heavyShortfall > 0 AND no heavy yard is known AND the gate-reachable shipyards are
//       scan-exhausted, so a missing heavy yard is CONCLUSIVE rather than merely
//       undiscovered. It never fires while shipyard coverage is still sparse, since a heavy
//       yard might yet be found on-gate.
//
// The real adapters live in internal/adapters/expansion.

// OffGateTarget is the selected warp-exploration target the demand signal carries: an off-gate
// system, the frontier system a warp would launch FROM (the nearest gate-connected system —
// the frontier edge), the warp fuel that leg costs under the CRUISE fuel model, and the
// exploration value that ranked it.
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

// OffGateDemandSignal is the off-gate explorer demand the frontier coordinator raises for its
// consumer. Demanded is the flag; ExplorerCount the count of explorers wanted; Reason the
// human-readable trigger; and Target the selected warp target, present (HasTarget) iff a
// reachable off-gate system exists within warp range. A zero value means no demand this cycle.
type OffGateDemandSignal struct {
	Demanded      bool
	ExplorerCount int
	Reason        string
	HasTarget     bool
	Target        OffGateTarget
}
