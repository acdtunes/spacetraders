package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// --- cross-system travel (sp-wlev: the multi-system gate-crossing unlock) ---

const (
	// DefaultCooldownMarginFactor mirrors ship.DefaultArrivalMarginFactor
	// (sp-ht1f) exactly: the multiplicative term scales naturally with
	// however long the wait is, so the same ratio is appropriate whether
	// the thing being waited on is an arrival or a jump cooldown.
	DefaultCooldownMarginFactor = 1.25

	// DefaultCooldownMinMargin is sized for jump cooldowns specifically -
	// NOT copied from arrival's 2-minute floor. Arrival's 2-minute margin
	// is a small fraction of transits that are themselves minutes-to-hours
	// long; reusing it here would make the margin the DOMINANT term for a
	// ~60s jump cooldown (a 3x inflation) instead of the small clock-skew/
	// API-latency correction it's meant to be. 10s comfortably absorbs
	// that jitter without ballooning the wait on the much shorter cooldown
	// timescale.
	DefaultCooldownMinMargin = 10 * time.Second
)

// calculateCooldownWaitBudget mirrors calculateArrivalWaitBudget's formula
// (internal/application/ship/arrival_wait.go, sp-ht1f pattern):
// budget = max(remaining*marginFactor, remaining+minMargin). The margin
// absorbs scheduler jitter and API latency around the real cooldown-expiry
// instant; it is not what keeps a short cooldown from being polled early. A
// negative remaining (clock skew already putting "now" past the reported
// expiry) is clamped to zero before either term is computed.
func calculateCooldownWaitBudget(remaining time.Duration, marginFactor float64, minMargin time.Duration) time.Duration {
	if remaining < 0 {
		remaining = 0
	}
	scaled := time.Duration(float64(remaining) * marginFactor)
	floor := remaining + minMargin
	if scaled > floor {
		return scaled
	}
	return floor
}

// waitForJumpCooldown waits out the cooldown the jump API just reported,
// using an ETA-scaled budget (sp-ht1f pattern) instead of a flat buffer
// (contrast run_siphon_worker.go's waitForShipCooldown, which adds a flat
// 1s - that resumes a cooldown persisted from a PREVIOUS session and has no
// fresher number to scale from; here the jump response just told us the
// exact cooldown synchronously, so the ETA-scaled budget applies cleanly).
func (h *RunTradeRouteCoordinatorHandler) waitForJumpCooldown(ctx context.Context, cooldownSeconds int) {
	if cooldownSeconds <= 0 {
		return
	}
	remaining := time.Duration(cooldownSeconds) * time.Second
	budget := calculateCooldownWaitBudget(remaining, DefaultCooldownMarginFactor, DefaultCooldownMinMargin)

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Waiting out jump cooldown before continuing the circuit", map[string]interface{}{
		"action":              "jump_cooldown_wait",
		"cooldown_seconds":    cooldownSeconds,
		"wait_budget_seconds": int(budget.Seconds()),
	})
	h.clock.Sleep(budget)
}

// travel moves the ship toward destinationWaypoint, crossing a system
// boundary via jump (sp-n0x7's ship-jump verb) when needed - the sp-wlev
// multi-system trade-route unlock. A same-system destination takes the
// existing navigate/dock fast path unchanged, returning the SAME ship
// pointer untouched. A cross-system destination jumps instead: the jump
// opts out of taking its own claim (SkipClaim: true) since the coordinator
// already holds the ship claimed under its own container for the whole
// circuit, waits out the resulting cooldown, then reloads the ship from the
// repository so the caller continues with a pointer reflecting the ship's
// new system - every downstream verb (dock/purchase/sell) already
// dispatches by ship SYMBOL, never the cached pointer, so this reload is
// the only place staleness could otherwise leak in.
func (h *RunTradeRouteCoordinatorHandler) travel(
	ctx context.Context,
	ship *navigation.Ship,
	destinationWaypoint string,
	playerID int,
) (*navigation.Ship, error) {
	currentSystem := ship.CurrentLocation().SystemSymbol
	destSystem := shared.ExtractSystemSymbol(destinationWaypoint)

	if currentSystem == destSystem {
		if err := h.navigate(ctx, ship, destinationWaypoint, playerID); err != nil {
			return ship, err
		}
		return ship, nil
	}

	resp, err := h.mediator.Send(ctx, &navCmd.JumpShipCommand{
		ShipSymbol:        ship.ShipSymbol(),
		DestinationSystem: destSystem,
		PlayerID:          &playerID,
		SkipClaim:         true,
	})
	if err != nil {
		return ship, fmt.Errorf("jump %s to %s failed: %w", ship.ShipSymbol(), destSystem, err)
	}
	jumpResp, ok := resp.(*navCmd.JumpShipResponse)
	if !ok {
		return ship, fmt.Errorf("unexpected jump response type %T", resp)
	}

	h.waitForJumpCooldown(ctx, jumpResp.CooldownSeconds)

	freshShip, err := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), shared.MustNewPlayerID(playerID))
	if err != nil {
		return ship, fmt.Errorf("failed to reload ship %s after jump to %s: %w", ship.ShipSymbol(), destSystem, err)
	}
	return freshShip, nil
}

// --- cross-system ranking penalty (sp-wlev) ---

// crossSystemRankingPenaltyPerUnit is subtracted from a lane's per-unit
// spread, for RANKING PURPOSES ONLY, when its source and destination sit in
// different systems - representing the jump-plus-cooldown time cost against
// an equally-profitable same-system lane. It never mutates the lane's real
// economics (SpreadPerUnit, CappedSpread, ClearsFloor all still read the
// lane's own untouched values), so a cross-system lane that wins the
// ranking is still evaluated for floor-discipline on its true numbers.
const crossSystemRankingPenaltyPerUnit = 200

// rankLanesWithGatePenalty re-orders lanes already ranked by
// trading.RankSpreads so cross-system lanes must clear a materially higher
// bar than same-system ones before being preferred - reflecting the extra
// time cost of a jump plus cooldown that RankSpreads' pure per-unit-spread
// view can't see. It returns a NEW slice of the original, unmodified
// ArbitrageLane values; only ordering changes, mirroring RankSpreads' own
// tie-break chain (score desc, then the lane's REAL unpenalized
// SpreadPerUnit desc, then Good asc) with the penalty-adjusted score
// substituted as the primary key only.
func rankLanesWithGatePenalty(lanes []trading.ArbitrageLane) []trading.ArbitrageLane {
	type scoredLane struct {
		lane  trading.ArbitrageLane
		score int
	}

	scored := make([]scoredLane, len(lanes))
	for i, lane := range lanes {
		effectiveSpread := lane.SpreadPerUnit
		if shared.ExtractSystemSymbol(lane.SourceWaypoint) != shared.ExtractSystemSymbol(lane.DestWaypoint) {
			effectiveSpread -= crossSystemRankingPenaltyPerUnit
		}
		scored[i] = scoredLane{lane: lane, score: effectiveSpread * lane.VolumeCap}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].lane.SpreadPerUnit != scored[j].lane.SpreadPerUnit {
			return scored[i].lane.SpreadPerUnit > scored[j].lane.SpreadPerUnit
		}
		return scored[i].lane.Good < scored[j].lane.Good
	})

	result := make([]trading.ArbitrageLane, len(scored))
	for i, s := range scored {
		result[i] = s.lane
	}
	return result
}
