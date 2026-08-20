package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// resolveTourReserve is the working-capital reserve every tour this coordinator launches
// carries (sp-9bacx). Re-derived from live treasury each reconcile pass, never stamped at
// coordinator-launch time: this container is launched once and runs forever, so the one a
// mature fleet has IS the one cold start started, and a frozen phase decision would follow
// the fleet into an economy it was never meant for.
//
// The tour's own 150k default (NonContractWorkingCapitalFloor, sp-q8bon) only protects a
// treasury that EXCEEDS it. At or below, the capital budget is max(0, treasury−reserve) = 0:
// every tour exits capital_denied and the treasury cannot grow because it cannot trade — the
// permanent deadlock a cold start hits at ~147.5k once probes are bought. There the reserve
// falls back to ImmutableReserveFloor, RULINGS #5's backstop for trading out of a low
// treasury. It never resolves below that floor, and above the mature line nothing changes.
//
// Fails closed: a configured [trade_fleet] reserve is the captain's number and settles it
// without a read, and an unwired or unreadable balance keeps the tour's default — a balance
// nobody could read never lowers a money guard (RULINGS #4).
func (h *RunTradeFleetCoordinatorHandler) resolveTourReserve(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, logger common.ContainerLogger) int64 {
	if cmd.WorkingCapitalReserve != 0 || h.treasury == nil {
		return cmd.WorkingCapitalReserve
	}

	credits, err := h.treasury.Credits(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Trade fleet: treasury unreadable (%v) — launching on the tour's own working-capital default (failing closed, not lowering it)", err), map[string]interface{}{
			"action": "trade_fleet_reserve_unreadable", "container_id": cmd.ContainerID, "error": err.Error(),
		})
		return cmd.WorkingCapitalReserve
	}
	if credits > common.NonContractWorkingCapitalFloor {
		return cmd.WorkingCapitalReserve
	}

	logger.Log("INFO", fmt.Sprintf(
		"Trade fleet: treasury %d is at or below the %d mature working-capital default, which would deploy 0 capital and deny every tour — launching on the %d immutable anti-stall floor instead so the fleet can trade its way out (RULINGS #5)",
		credits, common.NonContractWorkingCapitalFloor, common.ImmutableReserveFloor), map[string]interface{}{
		"action": "trade_fleet_reserve_crunch", "container_id": cmd.ContainerID,
		"treasury": credits, "resolved_reserve": common.ImmutableReserveFloor,
	})
	return common.ImmutableReserveFloor
}

// tourReserveResolver resolves the launch reserve at most ONCE per pass and hands the same
// answer to every hull launched in it, so a fleet-wide relaunch is priced off one balance.
// Lazy: a pass that launches nothing reads no treasury. Per-pass only — the next pass
// re-derives from scratch, so there is no in-memory cursor to go stale (RULINGS #2).
func (h *RunTradeFleetCoordinatorHandler) tourReserveResolver(ctx context.Context, cmd *RunTradeFleetCoordinatorCommand, logger common.ContainerLogger) func() int64 {
	var reserve int64
	resolved := false
	return func() int64 {
		if !resolved {
			reserve, resolved = h.resolveTourReserve(ctx, cmd, logger), true
		}
		return reserve
	}
}
