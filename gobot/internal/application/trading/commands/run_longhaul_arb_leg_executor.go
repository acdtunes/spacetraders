package commands

// run_longhaul_arb_leg_executor.go — the production binding of the worker's directedLegExecutor
// port to the REUSED one-shot arb executor (RunArbCoordinatorHandler, sp-p4ua). This is the
// reuse linchpin: a long-haul directed leg IS a guarded one-shot arb run, so the worker
// composes RunArbCoordinatorHandler rather than re-implementing buy/travel/sell/held-cargo. The
// mapping sets the money-envelope backstop — WorkingCapitalReserve = the 200k ContractScalerCushion
// fence, MaxSpend = the per-haul cap — so the arb executor's OWN fail-closed guards enforce the
// envelope a second time at execution (belt-and-suspenders to the worker's selectHaul sizing).

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// longHaulLegExecutor adapts RunArbCoordinatorHandler to the worker's directedLegExecutor port.
type longHaulLegExecutor struct {
	arb *RunArbCoordinatorHandler
}

// newLongHaulLegExecutor wires the binding over a fully-configured arb handler (the daemon
// constructs the arb handler with the same ports the CLI arb-run uses, plus gate graph / event
// subscriber / absorption ledger so cross-gate travel and the cross-engine shadow work).
func newLongHaulLegExecutor(arb *RunArbCoordinatorHandler) *longHaulLegExecutor {
	return &longHaulLegExecutor{arb: arb}
}

// arbCommandForLeg maps a directed long-haul leg onto a one-shot arb command. WorkingCapitalReserve
// is pinned to the 200k ContractScalerCushion (the cushion fence long-haul must never dip below)
// and MaxSpend to the per-haul cap, so the reused arb guards backstop the money envelope. MaxUnits
// carries the worker's impact-/absorption-sized tranche, so the arb buy never exceeds it.
func arbCommandForLeg(cmd directedLegCommand) *RunArbCoordinatorCommand {
	return &RunArbCoordinatorCommand{
		ShipSymbol:            cmd.ShipSymbol,
		Good:                  cmd.Good,
		BuyAt:                 cmd.BuyAt,
		SellAt:                cmd.SellAt,
		MaxUnits:              cmd.Units,
		MaxSpend:              int(cmd.PerHaulCap),
		MinMargin:             cmd.MinMargin,
		PlayerID:              cmd.PlayerID,
		ContainerID:           cmd.ContainerID,
		WorkingCapitalReserve: int(common.ContractScalerCushion),
		// sp-ry741: align the reused arb leg's jump horizon — BOTH Guard-0's pre-buy delivery
		// routability CHECK and the travel-to-sink FLIGHT — with the long-haul reposition bound. A
		// long-haul lane's sell leg is 6–12 gate hops from its source (the whole reason long-haul
		// exists — lanes beyond the 1-hop/5-hop horizon); at the default bound-5 the check vetoed every
		// one at buy time, and once the check alone was first widened the flight then fail-looped the
		// sell leg at 5 (the residual). 25 = longHaulRepositionJumps, the SAME bound discovery ranks
		// and the reposition flies — so admission, reposition, buy, and delivery now agree on the
		// horizon. This is the ONLY builder that sets it, so the shared handler's one-shot arb runs
		// stay at 5.
		LegJumpBound: longHaulRepositionJumps,
	}
}

// RunLeg executes one directed leg through the reused arb handler and maps its realized outcome
// back. A held remainder / abort surfaces as the arb handler's non-nil error (an honest failure
// completion); the worker's next-episode DB-driven re-derive is what actually resumes any cargo
// left aboard, so the result carries no separate held count.
func (e *longHaulLegExecutor) RunLeg(ctx context.Context, cmd directedLegCommand) (directedLegResult, error) {
	resp, err := e.arb.Handle(ctx, arbCommandForLeg(cmd))
	arbResp, _ := resp.(*RunArbCoordinatorResponse)
	if arbResp == nil {
		return directedLegResult{}, err
	}
	return directedLegResult{
		UnitsTraded: arbResp.UnitsTraded,
		NetProfit:   arbResp.NetProfit,
		Aborted:     arbResp.Aborted,
		AbortReason: arbResp.AbortReason,
	}, err
}
