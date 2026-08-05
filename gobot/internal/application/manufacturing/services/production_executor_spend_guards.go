package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultWorkingCapitalReserve is the IMMUTABLE lower bound of the working-capital
// spend floor applied to factory INPUT purchases. It mirrors bp6f's
// trade-circuit floor (the identically-named const in run_trade_route_coordinator.go)
// and closes the same drain class one layer over — re-enabling 4 goods factories at
// ~848k drained the float to 23k in ~1min because bp6f guarded trade circuits but NOT
// factory input buys.
//
// sp-agzj unified this with the fleet's per-run working-capital reserve; sp-05glh then
// scrapped the proportional/per-run-override apparatus entirely (nothing ever stamped a
// non-default value in production) — the factory input floor is now simply this flat
// immutable constant, unconditionally (RULINGS #5). sp-q8bon raised it from the 50k base
// to the 150k non-contract floor: margin-blind gate-fill buys dragged the treasury
// 638k→142k and the contract engine — the sole earner — parked against ITS 50k floor, a
// full-economy deadlock; the 50k–150k band is now contract-exclusive.
const defaultWorkingCapitalReserve = common.NonContractWorkingCapitalFloor // sp-q8bon: the non-contract 150k floor (was the 50k base)

// effectiveReserveFloor is the working-capital floor enforced at a factory input buy: the
// flat, immutable defaultWorkingCapitalReserve. It takes no inputs deliberately — there is
// no treasury-proportional shrink and no per-run override seam to lower it (RULINGS #5).
func effectiveReserveFloor() int {
	return defaultWorkingCapitalReserve
}

// budgetedReserveFloor is the floor a construction/factory buy is ACTUALLY guarded against:
// the flat effectiveReserveFloor RAISED by the share of deployable capital reserved
// for the trade side. Without that allocation the two non-contract engines draw on a shared pool
// with no split between them, and because construction carries no proportional cap while trade
// caps itself at 25% of treasury, construction consumes everything above its floor — including
// the whole band trade needs to function.
//
// It can only ever RAISE the floor (common.BudgetedSpendFloor is >= floor for every input), so
// this ADDS a constraint and weakens nothing (RULINGS #4), and it derives the deployable pool
// from THIS engine's own floor rather than a second constant (RULINGS #5).
//
// Three resolutions, deliberately different:
//
//   - No sensor wired -> the flat floor, unchanged. The optional-port contract for the package's
//     fixtures; the daemon always wires one.
//   - Sensor says trade is idle -> graceful degradation hands construction the WHOLE deployable
//     pool, and BudgetedSpendFloor returns exactly the flat floor. Capital never idles because
//     no trade hull is live.
//   - Sensor errors -> fail CONSERVATIVE, not open: assume trade IS working and take only the
//     proportional share. A blind read must never hand construction 100% of the treasury, which
//     is precisely the failure this floor exists to remove.
//
// Note that construction passes `true` for its OWN side unconditionally — an executor asking this
// question is an executor about to buy — so a sensor miss can never budget construction to zero
// and park the gate.
func (e *ProductionExecutor) budgetedReserveFloor(ctx context.Context, playerID, treasury int) int {
	floor := effectiveReserveFloor()
	if e.workSensor == nil {
		return floor
	}

	logger := common.LoggerFromContext(ctx)
	tradeHasWork := true
	if has, err := e.workSensor.TradeHasWork(ctx, playerID); err != nil {
		// Numbers/cause in the MESSAGE: the container log renderer drops the
		// metadata map, so a conservative resolution must name its cause in the text.
		logger.Log("WARNING", fmt.Sprintf("Could not sense whether trade is live for the capital budget — assuming it is and taking only construction's %d%% share (fail-conservative): %v", 100-common.TradeCapitalSharePct, err), map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		tradeHasWork = has
	}

	deployable := common.CapitalDeployable(int64(treasury), int64(floor))
	_, constructionBudget := common.CapitalSplit(common.TradeCapitalSharePct, deployable, tradeHasWork, true)
	return int(common.BudgetedSpendFloor(int64(floor), deployable, constructionBudget))
}

// SpendReservationLedger is the CROSS-OPERATION concurrent spend cap (sp-w3he, extended by
// sp-ps2oc). The per-buy floor checks live treasury per caller, but N callers can each pass
// that independent check inside the check->buy window and collectively dip below the reserve.
// This ledger closes that race using shared DB state: a spender records its intent and, in one
// serialized atomic step, verifies the balance minus the SUM of all active in-flight
// reservations still clears the reserve.
//
// IT SUMS PER PLAYER, NOT PER CONTAINER. sp-w3he was built when the only concurrent buyers
// were N goods-factory containers, and the containerID parameter reads like a scope — it is
// not one. Reservations sum per player and the DB advisory lock is keyed per player, so buys
// from a SINGLE container serialise against each other exactly as buys from N containers do,
// and a construction buy serialises against a contract buy. containerID is best-effort
// attribution for logs. This is why sp-ps2oc — three racing buys from one construction
// container — was always within this cap's reach; it simply was not wired to production.
//
// Reserve READS THE BUDGET ITSELF, through readBudget, rather than accepting a pre-read
// balance. A balance read before the call can describe a different instant than the SUM: a
// sibling that commits and releases in between appears in neither, and the headroom is counted
// twice. Passing the read in as a callback lets the implementation take it inside its own
// critical section, which is the only place the two halves can be made to agree.
//
// readBudget returns the balance AND the floor that balance is judged against, together,
// because the construction floor is DERIVED from the balance (the capital budget raises the
// flat reserve by trade's share of deployable capital). Returning them separately would let
// the check and the floor be sized from two different treasury figures.
//
// Reserve reports ok==false when the combined spend would breach (caller PARKS) and rolls the
// reservation back. On ok==true the caller Releases the returned id after the buy. A
// readBudget error surfaces as err, never as a zero balance — callers PARK on it (RULINGS #4).
// ExpireStale reclaims reservations a dead container never released.
type SpendReservationLedger interface {
	Reserve(ctx context.Context, playerID int, containerID string, projectedCost int, readBudget func(context.Context) (credits int64, reserveFloor int, err error)) (reservationID string, ok bool, err error)
	Release(ctx context.Context, playerID int, reservationID string) error
	ExpireStale(ctx context.Context, maxAge time.Duration) (int, error)
}

// TreasuryReader reports a player's credit balance for the factory money guards. The daemon
// satisfies it with the LEDGER-backed reader (sp-muq66), which serves the balance from the
// transaction ledger — no API call — and falls back to the coalesced live read only when the
// ledger is older than its freshness bound. `Get Agent` was 9-10% of the API ceiling and did
// not fall under request coalescing, because the reads are invalidation-driven rather than
// duplicated; the only way to remove them is to stop asking.
//
// An error means UNREADABLE, and both callers here PARK the input buy on it (RULINGS #4).
// The reader never converts a failed read into a stale or zero success.
type TreasuryReader interface {
	Credits(ctx context.Context, playerID int) (int64, error)
}

// SetSpendLedger wires the cross-container concurrent spend cap. The daemon calls
// this after construction (main.go, via the coordinator handler); leaving it unset keeps the
// cap fail-open, which is exactly what every non-daemon caller wants.
func (e *ProductionExecutor) SetSpendLedger(ledger SpendReservationLedger) {
	e.spendLedger = ledger
}

// SetTreasuryReader wires the daemon's shared ledger-backed treasury reader into
// BOTH factory money guards. The daemon calls this UNCONDITIONALLY when it builds the
// construction executor — no config key, no default-off, no arming step. Leaving it unset is
// the test-fixture path only, and falls back to the direct live read.
func (e *ProductionExecutor) SetTreasuryReader(r TreasuryReader) {
	e.treasury = r
}

// treasuryCredits reads the player's balance for a factory money guard: through the
// ledger-backed reader when the daemon has wired one, otherwise the direct live call this
// guard has always made. The direct path is the optional-port contract the package's
// fixtures rely on, not an arming switch.
//
// Every failure is an ERROR — never a zero, never a retained value. Both callers read that
// as "PARK this input buy" (RULINGS #4).
func (e *ProductionExecutor) treasuryCredits(ctx context.Context, playerID int) (int64, error) {
	if e.treasury != nil {
		return e.treasury.Credits(ctx, playerID)
	}
	if e.apiClient == nil {
		return 0, fmt.Errorf("no treasury source wired")
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unavailable: %w", err)
	}
	agentData, err := e.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("live agent read failed: %w", err)
	}
	return int64(agentData.Credits), nil
}

// SetCapitalWorkSensor wires the per-operation capital budget's hasWork sensor. The
// daemon calls this UNCONDITIONALLY when it builds the construction executor — there is no config
// key, no default-off and no arming step between the sensor and a live budget. Leaving it unset
// is the test-fixture path only, and keeps the flat reserve floor as the sole guard.
func (e *ProductionExecutor) SetCapitalWorkSensor(sensor common.CapitalWorkSensor) {
	e.workSensor = sensor
}

// spendFloorBreached reports whether buying an input tranche costing projectedCost would drop
// treasury below the floor this buy is guarded against, and returns that floor so the caller
// can name the ACTUAL enforced number in its park log rather than re-deriving a base that the
// capital budget may have raised. It mirrors bp6f's trade floor (spendFloorBreached in
// run_trade_route_coordinator.go): a pre-commit treasury read checked right before the buy
// commits, so the caller can PARK instead of spending.
//
// That mirror is why the read is now the ledger-backed one: bp6f's floor was routed
// through the shared reader in sp-muq66, and this guard exists to enforce the SAME line the
// same way. It stays a genuine pre-commit read — the freshness bound is what makes the ledger
// answer admissible, and past it the coalesced live call still answers.
//
// The floor is budgetedReserveFloor (sp-ftqgp): the flat non-contract reserve, raised by the share
// of deployable capital reserved for the trade side. Layering the budget HERE covers both spend
// paths at once — raw input buys (buyGood) and fabricated-output harvests both funnel through
// this one primitive — with no second guard to drift.
//
// Fails OPEN when NO treasury source is wired at all (neither the ledger-backed reader nor
// an apiClient): the guard is simply unavailable — the optional-port contract the package's
// test fixtures rely on (they pass nil), never the daemon, which always wires both.
//
// Fails CLOSED on every treasury-read failure (an unresolvable player token, GetAgent itself
// erroring, or a ledger too stale to trust with the live fallback also down): a guard whose
// whole job is keeping treasury above the reserve must never let a buy through just because
// it went blind. A hiccup here parks the input rather than spending unseen — the factory-side
// analogue of bp6f's fail-closed.
func (e *ProductionExecutor) spendFloorBreached(ctx context.Context, playerID, projectedCost int) (bool, int) {
	logger := common.LoggerFromContext(ctx)
	if e.apiClient == nil && e.treasury == nil {
		return false, effectiveReserveFloor()
	}

	credits, err := e.treasuryCredits(ctx, playerID)
	if err != nil {
		// Numbers/cause in the MESSAGE: the container log renderer drops the
		// metadata map, so a blind fail-closed park must name its cause in the text.
		logger.Log("WARNING", fmt.Sprintf("Could not read treasury for factory spend-floor check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return true, effectiveReserveFloor()
	}
	treasury := int(credits)

	// The flat non-contract floor (sp-05glh/sp-q8bon), RAISED by trade's reserved share of
	// deployable capital (sp-ftqgp). Resolved against the SAME treasury figure the breach test
	// uses, so the budget and the check can never disagree about the balance they are sizing.
	reserve := e.budgetedReserveFloor(ctx, playerID, treasury)

	if treasury-projectedCost < reserve {
		logger.Log("WARNING", fmt.Sprintf("Factory input buy would breach the working-capital reserve — treasury %d, projected cost %d, reserve %d", treasury, projectedCost, reserve), map[string]interface{}{
			"treasury": treasury, "projected_cost": projectedCost, "reserve": reserve,
		})
		return true, reserve
	}

	return false, reserve
}

// reserveConcurrentSpendOrPark records this input buy's spend intent in the shared ledger
// and reports whether it must PARK because the COMBINED in-flight factory spend would
// breach the reserve. On the proceed path it returns the reservation id the
// caller must Release after the buy.
//
// Fails OPEN when the cap is unavailable (no ledger wired, or no treasury source at all to
// read the balance from) — the same optional-port contract as the per-buy floor, so every
// non-daemon caller is unaffected. Fails CLOSED (parks) on any treasury-read or ledger error:
// a cap whose job is protecting the reserve must never let a buy through blind.
//
// The treasury read here is deliberately independent of spendFloorBreached (sp-9aoc, left
// unchanged): factory input buys are low-frequency — one per market visit, after a
// multi-second navigate+dock — so the second read is negligible next to keeping the two
// guards decoupled, each with its own legible park reason. It is now the ledger-backed read
// (sp-45s6f), which makes "negligible" literal on the common path: no API call at all.
//
// WHY A LEDGER BALANCE IS SOUND FOR THE CONCURRENT CAP. The cap subtracts in-flight
// RESERVATIONS from a base balance, precisely because a balance read cannot see spend that
// has not committed yet. Committed spend is in the ledger (every transaction records its
// post-transaction balance); uncommitted spend is in the reservation ledger. Nothing falls
// between the two: the cargo transaction handler records the transaction SYNCHRONOUSLY, in
// the same call, before the buy returns — and the reservation is only released after that
// call returns — so there is no window in which a spend is absent from both.
//
// The read stays OUTSIDE the ledger transaction (passed in as a value) so the DB is never
// held open across it.
func (e *ProductionExecutor) reserveConcurrentSpendOrPark(ctx context.Context, playerID, projectedCost int, market, good string) (reservationID string, parked bool) {
	logger := common.LoggerFromContext(ctx)
	if e.spendLedger == nil || (e.apiClient == nil && e.treasury == nil) {
		return "", false
	}

	// Container id attributes the reservation to the owning container (already threaded into
	// ctx by the coordinator, sp-9aoc's operation context). Best-effort: the staleness sweep
	// is time-based, so a missing id never affects correctness, only log/debug attribution.
	// It is NOT a scope — the ledger sums per player, which is why the gate fleet's four hulls
	// inside ONE construction container still serialise against each other (sp-ps2oc).
	containerID := "factory-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	// The treasury read and the floor it is judged against are BOTH taken inside the ledger's
	// own critical section (sp-ps2oc). Reading them out here and passing values in is the bug:
	// a sibling that commits its buy and releases its reservation in between lands in neither
	// the snapshot nor the SUM, and its spend is silently un-counted. observed* capture what
	// the callback saw so the park log can name the real numbers.
	var observedTreasury, observedReserve int
	readBudget := func(ctx context.Context) (int64, int, error) {
		credits, err := e.treasuryCredits(ctx, playerID)
		if err != nil {
			return 0, 0, err
		}
		treasury := int(credits)
		// Same floor as the per-buy check: the concurrent cap must serialize against the SAME
		// reserve the per-buy floor enforces, or the two guards would disagree on where the
		// line is (sp-agzj/sp-05glh). That reserve is the BUDGETED floor (sp-ftqgp) — the flat
		// non-contract base raised by trade's reserved share — resolved against THIS read, so a
		// construction buy cannot slip past the budget by taking the ledger path.
		reserve := e.budgetedReserveFloor(ctx, playerID, treasury)
		observedTreasury, observedReserve = treasury, reserve
		return credits, reserve, nil
	}

	resID, ok, err := e.spendLedger.Reserve(ctx, playerID, containerID, projectedCost, readBudget)
	treasury, reserve := observedTreasury, observedReserve
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Factory concurrent-spend-cap ledger error — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}
	if !ok {
		// AGGREGATE denial, counted (sp-ps2oc acceptance 5). A per-buy park is visible in the
		// coordinator's own logs; an aggregate one was only ever recoverable by reading the
		// transaction ledger and noticing that three buys shared a balance_after. The counter
		// separates the two so "concurrent spend is contending" is a graph, not an excavation.
		metrics.RecordAggregateSpendDenial("construction_supply")
		// Numbers in the MESSAGE: the container log renderer drops the metadata map,
		// so the cause — combined in-flight factory spend breaching the reserve — must be legible
		// in the text or an operator never sees why this factory parked.
		logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — cross-operation concurrent spend cap: treasury %d minus in-flight reservations would breach the working-capital reserve %d (this buy %d)", good, market, treasury, reserve, projectedCost), map[string]interface{}{
			"good":           good,
			"market":         market,
			"projected_cost": projectedCost,
			"treasury":       treasury,
			"reserve":        reserve,
			"action":         "factory_parked",
			"reason":         "concurrent_spend_cap",
		})
		return "", true
	}

	return resID, false
}

// releaseSpendReservation consumes a spend reservation after its buy completes (success or
// failure). A failed release is logged, never surfaced: the reservation simply leaks until
// the staleness sweep reclaims it, so cleanup can never fail an otherwise-successful buy.
func (e *ProductionExecutor) releaseSpendReservation(ctx context.Context, playerID int, reservationID string) {
	if e.spendLedger == nil || reservationID == "" {
		return
	}
	if err := e.spendLedger.Release(ctx, playerID, reservationID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to release factory spend reservation %s (staleness sweep will reclaim it): %v", reservationID, err), map[string]interface{}{
			"reservation_id": reservationID,
			"error":          err.Error(),
		})
	}
}
