package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// adoptPurchaseObligation hands this run the hull's outstanding tour-purchase ledger,
// seeded with whatever an earlier run left undischarged, for the run to accumulate into.
// A copy, so a concurrent read of the carry never races the run's own buys and sells.
func (h *RunTourCoordinatorHandler) adoptPurchaseObligation(shipSymbol string) map[string]int {
	h.purchaseObligationMu.Lock()
	defer h.purchaseObligationMu.Unlock()
	outstanding := map[string]int{}
	for good, units := range h.purchaseObligation[shipSymbol] {
		outstanding[good] = units
	}
	return outstanding
}

// retainPurchaseObligation hands the run's ledger back for the hull's NEXT run, so a
// purchase survives the restart that interrupted it. A hull with nothing outstanding is
// dropped entirely — a settled obligation must never linger to veto a later, unrelated run.
func (h *RunTourCoordinatorHandler) retainPurchaseObligation(shipSymbol string, outstanding map[string]int) {
	carry := map[string]int{}
	for good, units := range outstanding {
		if units > 0 {
			carry[good] = units
		}
	}
	h.purchaseObligationMu.Lock()
	defer h.purchaseObligationMu.Unlock()
	if len(carry) == 0 {
		delete(h.purchaseObligation, shipSymbol)
		return
	}
	h.purchaseObligation[shipSymbol] = carry
}

// dischargePurchaseObligation books units of good OFF the hull's outstanding obligation as
// they leave the hold — sold, deposited or liquidated. It never falls below zero: moving
// cargo the tour did NOT buy (a load the hull was handed, liquidated as launch inventory)
// discharges nothing, so it cannot buy credit against a LATER purchase the run then fails
// to sell. That netting is how a run could empty an inherited hold, refill it, and still
// report success.
func dischargePurchaseObligation(outstanding map[string]int, good string, units int) {
	remaining := outstanding[good] - units
	if remaining <= 0 {
		delete(outstanding, good)
		return
	}
	outstanding[good] = remaining
}

// vetoLadenExit terminalizes the run FAILED when the hull is ending laden with cargo the
// tour bought — the honest-completion veto (RULINGS #4: a hold the tour bought and did not
// sell is a failure, never a success). Called from the deferred epilogue so no exit path
// can release a laden hull as a clean completion.
func (h *RunTourCoordinatorHandler) vetoLadenExit(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, netBought map[string]int) {
	reason, stranded := h.strandedReason(ctx, cmd, netBought)
	if !stranded {
		return
	}
	response.CargoStranded = true
	response.CargoStrandedReason = reason
	response.Completed = false
	common.LoggerFromContext(ctx).Log("ERROR", reason, map[string]interface{}{"ship_symbol": cmd.ShipSymbol})
}

// strandedReason reports whether cargo the tour bought is still aboard — an
// honest-completion veto. Each good's strand is bounded by what the hull ACTUALLY holds,
// so an obligation the hold no longer carries (sold on another ground, transferred off,
// hand-rescued by the captain) is discharged rather than wedging the hull in permanent
// failure. It also settles the ledger against that live read, so the discharge carries to
// the next run. The message names each good, its stranded units, and the hull's current
// location so the strand is greppable and hand-recoverable.
func (h *RunTourCoordinatorHandler) strandedReason(ctx context.Context, cmd *RunTourCoordinatorCommand, netBought map[string]int) (string, bool) {
	if len(netBought) == 0 {
		return "", false
	}
	// An unreadable hull cannot prove the hold is empty. Fail CLOSED on the ledger alone
	// (RULINGS #4) — a read failure must never convert a strand into a success — and leave
	// the ledger untouched so the next run re-checks against a live read.
	loc := "unknown"
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err == nil {
		loc = ship.CurrentLocation().Symbol
	}
	var parts []string
	for good, bought := range netBought {
		stranded := bought
		if err == nil {
			stranded = min(bought, unitsOfGoodAboard(ship, good))
			netBought[good] = stranded
		}
		if stranded > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", stranded, good))
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	return fmt.Sprintf("stranded cargo: %s still aboard at %s (tour-bought, unsold) - reporting failure", strings.Join(parts, ", "), loc), true
}
