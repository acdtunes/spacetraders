package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// tourPlanBudget is the constraint set a tour plan is priced under. Resolved once per run
// (or per relocation pre-flight) and carried whole to the planner and every evaluator.
type tourPlanBudget struct {
	maxHops      int
	maxSpend     int64
	reserve      int64
	modelVersion string
}

func (b tourPlanBudget) withMaxSpend(maxSpend int64) tourPlanBudget {
	b.maxSpend = maxSpend
	return b
}

// errTourBudgetUnreadable is the constant streak key for the dynamic-budget resolve
// checkpoint. Constant so consecutive unreadable-treasury iterations count as the SAME
// error and accumulate toward the threshold (a varying message would reset the streak
// every pass).
var errTourBudgetUnreadable = errors.New("dynamic tour budget unresolved: live treasury unreadable")

// noteTourBudget records one iteration of the continuous loop's dynamic-budget resolve
// at the "resolve_tour_budget" streak checkpoint. unreadable=true is a failure
// (fail-closed pause) that, repeated for DefaultStreakThreshold consecutive iterations,
// crosses and emits the coordinator error-loop captain event; a readable resolve resets
// the streak. Edge-triggered and nil-safe on the recorder (health.RecordErrorLoop). Only
// reached on the dynamic-budget path (an explicit --max-spend never resolves, so this
// checkpoint stays inert for it).
func (h *RunTourCoordinatorHandler) noteTourBudget(ctx context.Context, cmd *RunTourCoordinatorCommand, budgetMon *health.Monitor, unreadable bool) {
	msg := ""
	if unreadable {
		msg = errTourBudgetUnreadable.Error()
	}
	if streak, crossed := budgetMon.Note("resolve_tour_budget", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID, "resolve_tour_budget", errTourBudgetUnreadable, streak)
	}
}

func (h *RunTourCoordinatorHandler) resolveWorkingCapitalReserve(cmd *RunTourCoordinatorCommand, logger common.ContainerLogger) int64 {
	if cmd.WorkingCapitalReserve != 0 {
		return cmd.WorkingCapitalReserve
	}
	// RULINGS #4: never resolve the reserve to a default SILENTLY. A built command
	// reaching here with reserve==0 means the launch config carried no reserve (a
	// captain CLI tour with no --reserve, or a fleet whose [trade_fleet] reserve is
	// unset); surfacing it makes a fleet accidentally running on the floor visible in
	// the log, not only in the P&L. The present-but-unparseable case cannot
	// reach here — it fails the build closed (PresentOrFailInt in
	// buildTourCoordinatorCommand).
	logger.Log("WARNING", fmt.Sprintf(
		"Tour %s: working-capital reserve resolved to the %d default (launch config carried no reserve) - every buy is floored at %d, not a fleet reserve",
		cmd.ShipSymbol, defaultWorkingCapitalReserve, defaultWorkingCapitalReserve), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "resolved_reserve": defaultWorkingCapitalReserve,
	})
	return int64(defaultWorkingCapitalReserve)
}

func resolveTourHopBudget(cmd *RunTourCoordinatorCommand) (maxHops, replanLimit int) {
	maxHops = cmd.MaxHops
	if maxHops <= 0 || maxHops > maxTourHops {
		maxHops = maxTourHops
	}
	replanLimit = cmd.ReplanLimit
	if replanLimit <= 0 {
		replanLimit = tourMaxReplansDefault
	}
	return maxHops, replanLimit
}

// resolveTourSpendCap returns unresolved=true when the caller must skip this tour and
// re-resolve on the next pass; a non-nil error is a resumable ctx cancel during the backoff.
func (h *RunTourCoordinatorHandler) resolveTourSpendCap(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, budgetMon *health.Monitor, reserve int64, logger common.ContainerLogger) (spend int64, unresolved bool, err error) {
	// RULINGS #6: an explicit --max-spend is a constant per-tour cap; --max-spend
	// 0/omitted re-resolves 25% of LIVE treasury at EACH tour's plan, so a
	// continuous run sizes each tour to the treasury it has grown into. The per-buy
	// working-capital floor guards every spend regardless.
	if cmd.MaxSpend != 0 {
		return cmd.MaxSpend, false, nil
	}
	resolved, unreadable := h.defaultMaxSpend(ctx, cmd.PlayerID, reserve)
	if unreadable {
		// The dynamic budget could NOT be re-resolved — a treasury SOURCE is
		// wired but the live read failed (transient GetAgent blip / token gone).
		// RULINGS #4 fail-CLOSED: do NOT spend this iteration and NEVER fall back
		// to unlimited or a stale budget. But failing closed must PAUSE and
		// RETRY, not end the loop: proceeding here with a 0 budget is exactly
		// what the planner refused (spend_cap 0 → infeasible), which — nothing
		// earned yet on a relaunch — the caller would misread as "tour
		// unavailable" and COMPLETE a -1 container after one iteration. Skip the
		// tour, wait an interruptible backoff, and re-resolve next pass; a
		// Stop/shutdown during the wait exits RESUMABLE (ctx error), the same as
		// the boundary check above. The no-progress starvation streak is left
		// UNTOUCHED — an unreadable treasury is a transient guard trip, not
		// margin-death.
		logger.Log("WARNING", "Dynamic tour budget unresolved (live treasury unreadable) - failing closed: not spending, pausing before retry (loop stays alive)", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "tours_completed": response.ToursCompleted,
			"backoff_seconds": int(tourTreasuryRetryBackoff.Seconds()),
		})
		h.noteTourBudget(ctx, cmd, budgetMon, true)
		if werr := h.legs.sleepInterruptibly(ctx, tourTreasuryRetryBackoff); werr != nil {
			return 0, false, werr
		}
		return 0, true, nil
	}
	h.noteTourBudget(ctx, cmd, budgetMon, false) // readable resolve resets the streak
	// Record the RESOLVED dynamic cap (25% of live treasury) — the same value the
	// Guards dashboard panel proxies with a treasury x 0.25 line. Only on the
	// dynamic path — an explicit --max-spend constant has nothing dynamic to track.
	metrics.SetTourResolvedMaxSpend(cmd.PlayerID, resolved)
	return resolved, false, nil
}

// defaultMaxSpend resolves the 25%-of-treasury cap (RULINGS #6) when --max-spend is 0.
// It returns (cap, unreadable) so the caller can tell "no treasury source, plan
// uncapped" apart from "have a source but the read FAILED, fail closed" — a single
// int64(0) would conflate the two, letting a transient read failure masquerade as a
// 0 budget:
//
//   - unreadable=false, cap>0  → treasury read; size the tour to 25% of it.
//   - unreadable=false, cap=0  → NO apiClient wired at all (structural; the daemon
//     always wires one, so this is the test-harness / pure-env path). 0 is "no explicit
//     cumulative cap" — the per-buy working-capital floor still guards every spend.
//   - unreadable=true,  cap=0  → a treasury SOURCE is wired but the read FAILED
//     (no player token, GetAgent errored, or a stale ledger with the live fallback also
//     down). The caller MUST fail closed: never spend
//     on this, never fall back to unlimited or a stale budget — pause and retry so a
//     continuous (--iterations -1) loop survives the transient (a shared-agent GetAgent
//     blip must not complete every hull after one iteration).
func (h *RunTourCoordinatorHandler) defaultMaxSpend(ctx context.Context, playerID int, reserve int64) (int64, bool) {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil && h.treasury == nil {
		return 0, false // no treasury source wired — 0 = no explicit cap (floor guards)
	}
	credits, err := h.treasuryCredits(ctx, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Cannot re-resolve dynamic tour max-spend: treasury read failed (%v) - failing closed (will not spend uncapped)", err), map[string]interface{}{
			"error": err.Error(),
		})
		return 0, true // source exists but UNREADABLE — fail closed
	}
	spendCap := credits * tourDefaultMaxSpendTreasuryPct / 100
	logger.Log("INFO", fmt.Sprintf("Default tour max-spend = %d (25%% of treasury %d)", spendCap, credits), map[string]interface{}{
		"max_spend": spendCap, "treasury": credits,
	})
	return h.applyCapitalBudget(ctx, playerID, reserve, credits, spendCap), false
}

// applyCapitalBudget clamps this tour's cumulative spend cap to TRADE's share of deployable
// capital (sp-ftqgp) — the other half of the per-operation budget whose construction half lives in
// the production executor's budgetedReserveFloor. It only ever LOWERS the cap (RULINGS #4: this
// bead adds a constraint and weakens none), and it derives the deployable pool from the tour's own
// resolved reserve rather than a second floor constant (RULINGS #5).
//
// It is applied on the DYNAMIC path only (--max-spend 0 → the 25%-of-treasury default, which is
// what the trade fleet runs). An explicit --max-spend is a captain override that already bypasses
// the 25% cap by design; leaving it untouched keeps that path byte-identical rather than adding a
// fail-closed live-treasury read to a path that has never had one.
//
// Three resolutions, deliberately different, mirroring the construction side:
//
//   - No sensor wired -> the 25% cap, unchanged (the optional-port contract; the daemon always
//     wires one).
//   - Sensor says construction is idle -> graceful degradation hands trade the WHOLE deployable
//     pool, which is far above 25% of treasury, so the cap is untouched and NO capital idles.
//     This is the live acceptance case: with the gate pipeline stopped, trade gets 100%.
//   - Sensor errors -> fail CONSERVATIVE, not open: assume construction IS working and take only
//     the proportional share.
//
// Trade passes `true` for its OWN side unconditionally — a tour asking this question is a tour
// about to buy — so a sensor miss can never budget trade to zero and park the fleet.
func (h *RunTourCoordinatorHandler) applyCapitalBudget(ctx context.Context, playerID int, reserve, treasury, spendCap int64) int64 {
	if h.workSensor == nil {
		return spendCap
	}
	logger := common.LoggerFromContext(ctx)

	constructionHasWork := true
	if has, err := h.workSensor.ConstructionHasWork(ctx, playerID); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not sense whether construction is live for the capital budget — assuming it is and taking only trade's %d%% share (fail-conservative): %v", common.TradeCapitalSharePct, err), map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		constructionHasWork = has
	}

	deployable := common.CapitalDeployable(treasury, reserve)
	tradeBudget, _ := common.CapitalSplit(common.TradeCapitalSharePct, deployable, true, constructionHasWork)
	if tradeBudget >= spendCap {
		// The budget is not the binding constraint this tour — either construction is idle and
		// trade holds the whole pool, or the 25% cap is simply tighter. Logged at INFO so the
		// "trade got 100%" acceptance case is directly observable in the container log.
		logger.Log("INFO", fmt.Sprintf("Capital budget: trade's share is %d of %d deployable (construction live=%v, share %d%%) — above the %d max-spend, so the dynamic cap binds", tradeBudget, deployable, constructionHasWork, common.TradeCapitalSharePct, spendCap), map[string]interface{}{
			"trade_budget": tradeBudget, "deployable": deployable, "construction_has_work": constructionHasWork,
			"max_spend": spendCap, "treasury": treasury, "reserve": reserve,
		})
		return spendCap
	}

	logger.Log("INFO", fmt.Sprintf("Capital budget: tour max-spend cut from %d to %d — trade's %d%% share of %d deployable (treasury %d, reserve %d), construction live=%v", spendCap, tradeBudget, common.TradeCapitalSharePct, deployable, treasury, reserve, constructionHasWork), map[string]interface{}{
		"trade_budget": tradeBudget, "deployable": deployable, "construction_has_work": constructionHasWork,
		"max_spend_before": spendCap, "treasury": treasury, "reserve": reserve,
	})
	return tradeBudget
}

func remainingSpend(maxSpend, spent int64) int64 {
	if maxSpend <= 0 {
		return 0 // no explicit cap
	}
	if r := maxSpend - spent; r > 0 {
		return r
	}
	return 0
}
