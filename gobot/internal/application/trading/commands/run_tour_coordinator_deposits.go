package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// depositCandidates assembles the haul-to-storage deposit sinks for the planner. It
// gates on the CAPITAL stage first (RULINGS #4/#5) and only enters the funnel
// (BuildDepositCandidates) when there is real capital to spend:
//   - pre-positioning off (Enabled=false) → silent off-switch;
//   - subsystem unwired → WARNING (a wiring bug);
//   - no capital ceiling configured (depositCeilingPct<=0) → DORMANT, fail closed —
//     opportunistic tour money movement is a captain/analyst decision, NOT an
//     auto-10%-of-treasury default that turns spending ON with no ruled number;
//   - live balance unreadable → WARNING, fail closed (RULINGS #4);
//   - ceiling resolves to 0 (treasury at/below the reserve) → nothing to stock now.
//
// Each parked outcome logs at most ONCE per container per distinct state via
// recordDepositParked, so a hull whose deposits stay parked does not re-emit the
// same fail-closed verdict every re-plan. The capital-available path clears the
// remembered state (a later park re-logs as a state change) and delegates to
// BuildDepositCandidates, whose own funnel verdict then fires only when there is
// capital to spend, never as per-tick parked noise.
func (h *RunTourCoordinatorHandler) depositCandidates(ctx context.Context, cmd *RunTourCoordinatorCommand, allowedSystems []string, reserve int64) []routing.TourDepositCandidate {
	if !h.prePositioning.Enabled {
		return nil // deliberate off-switch — not a silent zero, no verdict
	}
	key := cmd.ContainerID
	if key == "" {
		key = cmd.ShipSymbol
	}
	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.demandMiner == nil {
		// Enabled but a dependency is unwired: a WIRING BUG, not an off-switch — make it
		// LOUD, but once per container, not per re-plan.
		h.recordDepositParked(ctx, key, "WARNING",
			"pre-positioning enabled but subsystem unwired (storageCoordinator/warehouseFinder/demandMiner nil)",
			map[string]interface{}{
				"ship_symbol":               cmd.ShipSymbol,
				"container_id":              cmd.ContainerID,
				"storage_coordinator_wired": h.storageCoordinator != nil,
				"warehouse_finder_wired":    h.warehouseFinder != nil,
				"demand_miner_wired":        h.demandMiner != nil,
			})
		return nil
	}
	// Money movement is a captain/analyst decision (RULINGS #5): pre-positioning stays
	// PARKED until an explicit capital ceiling is configured. A 0/absent
	// capital_ceiling_pct is the dormant, fail-closed default — NOT an auto-10% of
	// treasury (which would turn spending ON the moment treasury cleared the reserve,
	// with no analyst-ruled number). The captain enables it by setting
	// contract.pre_positioning.capital_ceiling_pct>0.
	if h.depositCeilingPct <= 0 {
		h.recordDepositParked(ctx, key, "INFO",
			"pre-positioning dormant — no capital ceiling configured (set contract.pre_positioning.capital_ceiling_pct>0 to enable; money movement is a captain/analyst decision, RULINGS #5)",
			map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID})
		return nil
	}
	ceiling, known := h.depositCapitalCeiling(ctx, cmd.PlayerID, reserve)
	if !known {
		// Unreadable live balance: fail CLOSED (RULINGS #4) — a genuine anomaly (WARNING),
		// but de-duped per container so a rate-limit blip does not spam.
		h.recordDepositParked(ctx, key, "WARNING",
			"capital ceiling unreadable (live balance) — pre-positioning parked, fail closed (RULINGS #4)",
			map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID})
		return nil
	}
	if ceiling <= 0 {
		// Treasury at/below the working-capital reserve: the ceiling (junior to the
		// reserve) is 0, so nothing can be pre-positioned this pass. A transient economic
		// condition (INFO, mirroring the stocker's "capital ceiling is 0" line), de-duped.
		h.recordDepositParked(ctx, key, "INFO",
			fmt.Sprintf("capital ceiling 0 (treasury at/below the %d working-capital reserve) — nothing to pre-position this pass", reserve),
			map[string]interface{}{"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "reserve": reserve})
		return nil
	}
	// Capital available — forget any prior parked state (a later park re-logs as a state
	// change) and run the funnel.
	h.clearDepositParked(key)
	return tradingsvc.BuildDepositCandidates(
		ctx, h.demandMiner, h.warehouseFinder, h.storageCoordinator,
		allowedSystems, cmd.PlayerID, ceiling, known, h.prePositioning,
	)
}

// recordDepositParked emits a pre-positioning parked/dormant verdict for a container at
// most ONCE per distinct (level, reason): a hull whose deposits stay parked across
// hundreds of re-plans logs one line, not one per tick. A genuine state change — a
// different reason, or a transition through the capital-available path, which clears
// the remembered signature — re-emits. Concurrency-safe: the tour handler is a shared
// singleton dispatched for every touring hull at once.
func (h *RunTourCoordinatorHandler) recordDepositParked(ctx context.Context, key, level, reason string, fields map[string]interface{}) {
	sig := level + "|" + reason
	h.depositParkedMu.Lock()
	if h.depositParked[key] == sig {
		h.depositParkedMu.Unlock()
		return
	}
	h.depositParked[key] = sig
	h.depositParkedMu.Unlock()
	common.LoggerFromContext(ctx).Log(level, "Pre-positioning parked: "+reason, fields)
}

// clearDepositParked forgets a container's last parked verdict so that, should the hull
// fall back to a parked state, it logs afresh — the "or on state change" half of the
// once-per-container discipline.
func (h *RunTourCoordinatorHandler) clearDepositParked(key string) {
	h.depositParkedMu.Lock()
	delete(h.depositParked, key)
	h.depositParkedMu.Unlock()
}

// treasuryCredits reads the player's balance for this coordinator's money decisions:
// through the ledger-backed reader when the daemon has wired one (sp-muq66 — no API call
// on the common path), otherwise the direct live call the tour has always made. The direct
// path is the optional-port contract every nil-apiClient test relies on, not an arming
// switch. An error means UNREADABLE and every caller fails closed on it (RULINGS #4).
func (h *RunTourCoordinatorHandler) treasuryCredits(ctx context.Context, playerID int) (int64, error) {
	return readTreasuryCredits(ctx, h.treasury, h.apiClient, playerID)
}

// depositCapitalCeiling resolves the pre-positioning capital ceiling: depositCeilingPct
// percent of treasury, held JUNIOR to the working-capital reserve (never tie up
// capital that would breach it). The caller (depositCandidates) gates on
// depositCeilingPct>0 before calling this; a non-positive pct here returns a KNOWN zero
// (fail closed, parked) rather than substituting a default — an unset ceiling can never
// silently turn money movement ON. Returns known=false when the balance is
// UNREADABLE — the caller then offers no candidates (fail closed, RULINGS #4). The
// foreign buys the deposits fund still pass the per-buy working-capital floor and the
// cumulative max-spend cap at execution; this ceiling is layered on top.
func (h *RunTourCoordinatorHandler) depositCapitalCeiling(ctx context.Context, playerID int, reserve int64) (int64, bool) {
	if h.apiClient == nil && h.treasury == nil {
		return 0, false
	}
	credits, err := h.treasuryCredits(ctx, playerID)
	if err != nil {
		return 0, false
	}
	pct := int64(h.depositCeilingPct)
	if pct <= 0 {
		return 0, true // no ceiling configured — parked, fail closed (caller gates on this)
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
