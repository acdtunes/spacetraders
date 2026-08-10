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
	// An explicit --max-spend is a captain override: a constant per-tour cap, never
	// re-resolved. --max-spend 0/omitted re-resolves the CAPITAL BUDGET against LIVE
	// treasury at EACH tour's plan, so a continuous run sizes each tour to the treasury
	// it has grown into. The per-buy working-capital floor guards every spend regardless.
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
	// Record the RESOLVED dynamic cap for the Guards dashboard. Only on the dynamic
	// path — an explicit --max-spend constant has nothing dynamic to track.
	metrics.SetTourResolvedMaxSpend(cmd.PlayerID, resolved)
	return resolved, false, nil
}

// defaultMaxSpend resolves the CAPITAL BUDGET — trade's share of the capital deployable
// above the run's own working-capital reserve — when --max-spend is 0. It returns (cap,
// unreadable) so the caller can tell "no treasury source, plan uncapped" apart from "have
// a source but the read FAILED, fail closed" — a single int64(0) would conflate the two,
// letting a transient read failure masquerade as a 0 budget:
//
//   - unreadable=false, cap>0  → treasury read; size the tour to trade's share of it.
//   - unreadable=false, cap=0  → EITHER no apiClient wired at all (structural; the daemon
//     always wires one, so this is the test-harness / pure-env path, where 0 is "no explicit
//     cumulative cap" and the per-buy working-capital floor is the only guard), OR a live
//     treasury at or below the reserve, which deploys nothing and denies every spend.
//     hasTreasurySource is what tells the two apart.
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
	return h.tradeCapitalBudget(ctx, playerID, reserve, credits), false
}

// tradeCapitalBudget is the cumulative spend cap a dynamic (--max-spend 0) tour plans under:
// TRADE's share of the capital deployable above the run's own working-capital reserve. It is the
// trade half of the per-operation budget whose construction half lives in the production
// executor's budgetedReserveFloor, and it is the ONLY thing sizing this cap — a flat fraction of
// treasury in front of it bound BELOW the allocator and throttled the engine the allocator had
// already sized. The pool is derived from the reserve the run itself enforces rather than a second
// floor constant (RULINGS #5), so the cumulative cap can never authorise a spend the per-buy floor
// would refuse.
//
// DYNAMIC path only. An explicit --max-spend is a captain override that never reaches here, so
// that path keeps its constant cap and its lack of a live-treasury read.
//
// Three resolutions, deliberately different, mirroring the construction side:
//
//   - Sensor says construction is idle -> graceful degradation hands trade the WHOLE deployable
//     pool, so NO capital idles behind a stopped gate.
//   - Sensor errors -> fail CONSERVATIVE, not open: assume construction IS working and take only
//     the proportional share.
//   - No sensor wired -> the same conservative answer. A budget that cannot sense the other side
//     reserves for it (RULINGS #4); the daemon always wires one.
//
// Trade passes `true` for its OWN side unconditionally — a tour asking this question is a tour
// about to buy — so a sensor miss can never budget trade to zero and park the fleet.
func (h *RunTourCoordinatorHandler) tradeCapitalBudget(ctx context.Context, playerID int, reserve, treasury int64) int64 {
	logger := common.LoggerFromContext(ctx)

	constructionHasWork := true
	if h.workSensor != nil {
		if has, err := h.workSensor.ConstructionHasWork(ctx, playerID); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Could not sense whether construction is live for the capital budget — assuming it is and taking only trade's %d%% share (fail-conservative): %v", common.TradeCapitalSharePct, err), map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			constructionHasWork = has
		}
	}

	deployable := common.CapitalDeployable(treasury, reserve)
	tradeBudget, _ := common.CapitalSplit(common.TradeCapitalSharePct, deployable, true, constructionHasWork)
	// Logged at INFO so the resolved cap and the split that produced it are directly readable
	// in the container log — including the "construction idle, trade got 100%" case.
	logger.Log("INFO", fmt.Sprintf("Tour max-spend = %d of %d deployable (treasury %d, reserve %d, construction live=%v, trade share %d%%)", tradeBudget, deployable, treasury, reserve, constructionHasWork, common.TradeCapitalSharePct), map[string]interface{}{
		"max_spend": tradeBudget, "deployable": deployable, "construction_has_work": constructionHasWork,
		"treasury": treasury, "reserve": reserve,
	})
	return tradeBudget
}

// plannerReserveFor is the keep-back the SOLVER is handed for a plan priced under this budget.
// The solver's own money guard is spend_cap = max(0, max_spend − working_capital_reserve), a CASH
// contract that only holds on the EXPLICIT --max-spend path: under the dynamic budget max_spend is
// already a spend BUDGET (trade's share of the capital deployable ABOVE that same reserve), so
// forwarding the absolute fleet reserve would subtract the same capital guard twice and zero the
// planner for any treasury the budget had already sized. One function so the rule the request is BUILT with
// and the rule callers PREDICT the solver's verdict with can never drift apart.
func plannerReserveFor(cmd *RunTourCoordinatorCommand, reserve int64) int64 {
	if cmd.MaxSpend == 0 {
		return 0
	}
	return reserve
}

// hasTreasurySource reports whether a LIVE balance can be read at all. It is the documented
// discriminator between the two opposite meanings of a zero max-spend (defaultMaxSpend): with no
// source wired, 0 is the "no explicit cumulative cap" sentinel and the per-buy working-capital
// floor is the only guard; with a source wired, 0 is a number a live read produced.
func (h *RunTourCoordinatorHandler) hasTreasurySource() bool {
	return h.apiClient != nil || h.treasury != nil
}

// budgetDeniesEverySpend reports whether this budget leaves the solver's own money guard at zero,
// so EVERY plan priced under it is refused before a single market is looked at. That is a SOLVENCY
// verdict, not a market one: the deployable pool is empty (treasury at or below the working-capital
// reserve), or an explicit ceiling sits at or below its keep-back.
//
// It reads the budget's OWN arithmetic rather than the solver's returned reason, so it is a
// prediction the caller can act on BEFORE spending a planner round-trip per candidate — and cannot
// be fooled by a reworded reason string.
//
// It never relaxes a guard: the answer is only ever used to REFUSE work (skip a pre-flight whose
// verdict is already fixed) and to classify a refusal honestly. An unresolvable budget — a treasury
// source wired but unreadable — never reaches here: resolveTourSpendCap fails closed and pauses
// before anything is planned.
func (h *RunTourCoordinatorHandler) budgetDeniesEverySpend(cmd *RunTourCoordinatorCommand, budget tourPlanBudget) bool {
	if !h.hasTreasurySource() {
		return false // 0 means "no explicit cap" here, not "no money"
	}
	return budget.maxSpend <= plannerReserveFor(cmd, budget.reserve)
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
