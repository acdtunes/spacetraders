package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultWorkingCapitalReserve is the IMMUTABLE lower bound of the working-capital spend floor
// applied to factory INPUT purchases, mirroring the identically-named trade-circuit floor in
// run_trade_route_coordinator.go: unguarded factory input buys drain exactly the float the trade
// side is guarded against draining.
//
// It is the NON-CONTRACT floor, deliberately above the contract one. The band between the two is
// contract-exclusive — margin-blind gate-fill buys otherwise spend the treasury down to the
// contract engine's own floor and park the sole earner, a full-economy deadlock.
const defaultWorkingCapitalReserve = common.GateFillWorkingCapitalFloor

// effectiveReserveFloor is the working-capital floor enforced at a factory input buy: the
// flat, immutable defaultWorkingCapitalReserve. It takes no inputs deliberately — there is
// no treasury-proportional shrink and no per-run override seam to lower it (RULINGS #5).
func effectiveReserveFloor() int {
	return defaultWorkingCapitalReserve
}

// budgetedReserveFloor is the floor a construction/factory buy is ACTUALLY guarded against: the
// flat effectiveReserveFloor RAISED by the share of deployable capital reserved for the trade
// side. Without that split the two non-contract engines draw on one undivided pool, and since
// construction carries no proportional cap while trade caps itself at a share of treasury,
// construction consumes everything above its floor — including the band trade needs to function.
//
// It can only ever RAISE the floor (common.BudgetedSpendFloor is >= floor for every input), so it
// ADDS a constraint and weakens nothing (RULINGS #4), and derives the deployable pool from THIS
// engine's own floor rather than a second constant (RULINGS #5).
//
// Three resolutions, deliberately different: no sensor wired -> the flat floor (the optional-port
// contract the package's fixtures rely on; the daemon always wires one); trade idle -> the WHOLE
// deployable pool, which is exactly the flat floor, so capital never idles behind a side with no
// live hull; sensor ERROR -> fail CONSERVATIVE, assuming trade IS working and taking only the
// proportional share, because a blind read must never hand construction the whole treasury.
//
// Construction passes `true` for its OWN side unconditionally — an executor asking this question
// is one about to buy — so a sensor miss can never budget it to zero and park the gate.
func (e *ProductionExecutor) budgetedReserveFloor(ctx context.Context, playerID, treasury int) int {
	floor := effectiveReserveFloor()
	if e.workSensor == nil {
		return floor
	}

	logger := common.LoggerFromContext(ctx)
	tradeHasWork := true
	if has, err := e.workSensor.TradeHasWork(ctx, playerID); err != nil {
		// Cause in the MESSAGE: the container log renderer drops the metadata map.
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

// SpendReservationLedger is the CROSS-OPERATION concurrent spend cap. The per-buy floor checks
// live treasury per caller, but N callers can each pass that independent check inside the
// check->buy window and collectively dip below the reserve. This ledger closes that race using
// shared DB state: a spender records its intent and, in one serialized atomic step, verifies the
// balance minus the SUM of all active in-flight reservations still clears the reserve.
//
// IT SUMS PER PLAYER, NOT PER CONTAINER. containerID reads like a scope and is not one — it is
// best-effort log attribution. Reservations sum per player and the DB advisory lock is keyed per
// player, so buys from a SINGLE container serialise against each other exactly as buys from N
// containers do, and a construction buy serialises against a contract buy.
//
// Reserve READS THE BUDGET ITSELF, through readBudget, rather than accepting a pre-read balance.
// A balance read before the call can describe a different instant than the SUM: a sibling that
// commits and releases in between appears in neither, and the headroom is counted twice. Passing
// the read in as a callback lets the implementation take it inside its own critical section,
// which is the only place the two halves can be made to agree.
//
// readBudget returns the balance AND the floor that balance is judged against together, because
// the construction floor is DERIVED from the balance (the capital budget raises the flat reserve
// by trade's share of deployable capital). Returning them separately would let the check and the
// floor be sized from two different treasury figures.
//
// Reserve reports ok==false when the combined spend would breach (caller PARKS) and rolls the
// reservation back. On ok==true the caller Releases the returned id after the buy. A readBudget
// error surfaces as err, never as a zero balance — callers PARK on it (RULINGS #4). ExpireStale
// reclaims reservations a dead container never released.
type SpendReservationLedger interface {
	Reserve(ctx context.Context, playerID int, containerID string, projectedCost int, readBudget func(context.Context) (credits int64, reserveFloor int, err error)) (reservationID string, ok bool, err error)
	Release(ctx context.Context, playerID int, reservationID string) error
	ExpireStale(ctx context.Context, maxAge time.Duration) (int, error)
}

// TreasuryReader reports a player's credit balance for the factory money guards. The daemon
// satisfies it with the LEDGER-backed reader, which serves the balance from the transaction
// ledger — no API call — and falls back to the coalesced live read only when the ledger is older
// than its freshness bound. `Get Agent` reads are invalidation-driven rather than duplicated, so
// request coalescing cannot cut them; the only way to remove them is to stop asking.
//
// An error means UNREADABLE, and both callers here PARK the input buy on it (RULINGS #4). The
// reader never converts a failed read into a stale or zero success.
type TreasuryReader interface {
	Credits(ctx context.Context, playerID int) (int64, error)
}

// SetSpendLedger wires the cross-container concurrent spend cap. Leaving it unset keeps the cap
// fail-open, which is what every non-daemon caller wants; the daemon always wires one.
func (e *ProductionExecutor) SetSpendLedger(ledger SpendReservationLedger) {
	e.spendLedger = ledger
}

// SetTreasuryReader wires the daemon's shared ledger-backed treasury reader into BOTH factory
// money guards. The daemon calls it UNCONDITIONALLY — no config key, no arming step. Leaving it
// unset is the test-fixture path only, and falls back to the direct live read.
func (e *ProductionExecutor) SetTreasuryReader(r TreasuryReader) {
	e.treasury = r
}

// treasuryCredits reads the player's balance for a factory money guard: through the ledger-backed
// reader when one is wired, otherwise the direct live call. The direct path is the optional-port
// contract the package's fixtures rely on, not an arming switch.
//
// Every failure is an ERROR — never a zero, never a retained value. Both callers read that as
// "PARK this input buy" (RULINGS #4).
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

// SetCapitalWorkSensor wires the per-operation capital budget's hasWork sensor. The daemon calls
// it UNCONDITIONALLY — no config key, no arming step between the sensor and a live budget.
// Leaving it unset is the test-fixture path only, and keeps the flat reserve floor as sole guard.
func (e *ProductionExecutor) SetCapitalWorkSensor(sensor common.CapitalWorkSensor) {
	e.workSensor = sensor
}

// spendFloorBreached reports whether buying an input tranche costing projectedCost would drop
// treasury below the floor this buy is guarded against, and returns that floor so the caller can
// name the ACTUAL enforced number in its park log rather than re-deriving a base the capital
// budget may have raised. It mirrors the trade-side spendFloorBreached in
// run_trade_route_coordinator.go — a pre-commit treasury read taken right before the buy commits,
// so the caller can PARK instead of spending — and reads through the same shared ledger-backed
// reader so both enforce the same line the same way. It stays a genuine pre-commit read: the
// freshness bound is what makes a ledger answer admissible, and past it the live call answers.
//
// The floor is budgetedReserveFloor: the flat non-contract reserve raised by the share of
// deployable capital reserved for trade. Layering the budget HERE covers both spend paths at once
// — raw input buys (buyGood) and fabricated-output harvests both funnel through this one
// primitive — with no second guard to drift.
//
// Fails OPEN when NO treasury source is wired at all (neither the ledger-backed reader nor an
// apiClient): the guard is simply unavailable — the optional-port contract the package's fixtures
// rely on (they pass nil), never the daemon, which always wires both.
//
// Fails CLOSED on every treasury-read failure (unresolvable player token, GetAgent erroring, or a
// ledger too stale to trust with the live fallback also down): a guard whose whole job is keeping
// treasury above the reserve must never let a buy through just because it went blind.
func (e *ProductionExecutor) spendFloorBreached(ctx context.Context, playerID, projectedCost int) (bool, int) {
	logger := common.LoggerFromContext(ctx)
	if e.apiClient == nil && e.treasury == nil {
		return false, effectiveReserveFloor()
	}

	credits, err := e.treasuryCredits(ctx, playerID)
	if err != nil {
		// Cause in the MESSAGE: the container log renderer drops the metadata map.
		logger.Log("WARNING", fmt.Sprintf("Could not read treasury for factory spend-floor check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return true, effectiveReserveFloor()
	}
	treasury := int(credits)

	// Resolved against the SAME treasury figure the breach test below uses, so the budget and the
	// check can never disagree about the balance they are sizing.
	reserve := e.budgetedReserveFloor(ctx, playerID, treasury)

	if treasury-projectedCost < reserve {
		logger.Log("WARNING", fmt.Sprintf("Factory input buy would breach the working-capital reserve — treasury %d, projected cost %d, reserve %d", treasury, projectedCost, reserve), map[string]interface{}{
			"treasury": treasury, "projected_cost": projectedCost, "reserve": reserve,
		})
		return true, reserve
	}

	return false, reserve
}

// minViableTrancheUnits is the smallest input buy worth a leg.
//
// IT IS THE FEED SIDE'S OWN MIN-EFFECTIVE DELIVERY, NOT A NEW NUMBER. defaultFeedSaturationMinUnits
// is the quantity below which a delivery moves a factory's activity nothing, and the feeding policy
// already floors every delivery at it. Buying below it would fly, dock, spend and deliver a dribble.
//
// Deriving it rather than picking a round number keeps the two sides from disagreeing: a buy floor
// BELOW the feed floor purchases amounts the feeding policy would round up or waste, and one ABOVE
// it refuses buys the feed side considers effective.
const minViableTrancheUnits = defaultFeedSaturationMinUnits

// MinViableTrancheUnits exports the floor so the gate feed's PRE-dispatch affordability check can
// price the same quantity the buy will actually shrink to. Without that agreement the precheck
// declines a full-size tranche it cannot afford and the sizing-down below never runs, stalling a
// step the buy would have completed.
const MinViableTrancheUnits = minViableTrancheUnits

// TrancheSink names what the goods being bought are FOR, because the two sinks fillFromSource
// serves have different physics and the shrink floor is a property of the sink, not of the loop.
//
// A saturation floor is sound when the goods are SOLD INTO a factory's import listing, and
// meaningless when they are DELIVERED TO A CONSTRUCTION SITE: a gate has no activity to move, it
// has a BILL, and any partial delivery against it is permanent, irreversible progress. Applying
// the factory floor to a gate refuses affordable buys indefinitely.
//
// IT IS A PARAMETER, NOT A CONTEXT VALUE, DELIBERATELY — the same argument GateBuyer makes for
// carrying its probe on the interface. An ambient marker is inherited by whoever forgets to stamp
// it, so a new caller of BuyAtTerminalFactory would silently keep the first one's floor. A
// parameter makes the sink a compile-time obligation, so no caller inherits physics it never chose.
type TrancheSink int

const (
	// SinkFactoryFeed sells the goods into a factory's import listing, where the delivery must be
	// large enough to move that factory's activity to be worth the hull-hours. ZERO VALUE, so any
	// caller that does not choose keeps today's stricter floor rather than the looser one.
	SinkFactoryFeed TrancheSink = iota
	// SinkConstructionSite delivers the goods to a jump-gate construction site, which consumes them
	// against a fixed bill. Every unit counts; there is no saturation threshold to clear.
	SinkConstructionSite
)

// constructionMinHullFraction bounds how far a construction leg may under-fill its hull before the
// trip stops being worth flying: the floor is this fraction of the hold.
//
// WHY A HULL FRACTION AND NOT A CONSTANT, AND NOT THE MARKET. The hull round trip is the unit of
// work this floor rations, so the hold is the honest denominator, and deriving from it scales with
// the fleet instead of pinning a magic number a bigger hauler would silently invalidate. It is
// explicitly NOT read off the market's trade_volume: a floor that tracked depth would make a
// healthier market harder to buy from, which is backwards.
//
// WHY THIS SIZE. It is a trip-count bound and that is the whole of its justification: at worst a
// bill is cleared in this many times the legs a full hull would take — a real cost but a bounded
// one, against the alternative of making NO progress while capital recovers. Fuel cannot set the
// bound (a single unit's ask already dwarfs a leg's fuel) and gate materials are consumed rather
// than resold, so there is no margin to price the leg against either. The binding cost is
// hull-time. Named rather than buried so a re-tune has a stated consequence to weigh.
const constructionMinHullFraction = 8

// minTrancheUnitsFor resolves the smallest shrunken buy worth a leg for this sink.
//
// IT ONLY EVER LOWERS A FLOOR, AND ONLY FOR A CONSTRUCTION SINK (RULINGS #4). The factory path
// returns minViableTrancheUnits unchanged. Nothing here decides a spend: the caller re-runs
// spendFloorBreached on whatever size it proposes, so this can admit a SMALLER buy for
// consideration and never a larger one, and never one the reserve refuses.
//
// IT DELIBERATELY DOES NOT TAKE THE REMAINING REQUIREMENT. The obvious clamp — "never floor higher
// than what is left of the bill", so a nearly-finished material closes in one small leg — is
// PROVABLY INERT, pinned by TestMinTrancheUnitsFor_AClampToTheRemainingBillCouldNotChangeAnyOutcome.
// This is consulted ONLY after a breach, and a breach means trancheQty*ask > headroom, so
// affordableTrancheUnits (min(want, headroom/ask)) necessarily returns something STRICTLY BELOW
// trancheQty, itself <= tripTarget. Whenever tripTarget is small enough for the clamp to bind, the
// clamped floor is tripTarget and affordable is already below it — the buy declines either way.
// A small bill is still bought normally: an affordable remainder never reaches this function.
func minTrancheUnitsFor(sink TrancheSink, capacity int) int {
	if sink != SinkConstructionSite {
		return minViableTrancheUnits
	}
	floor := capacity / constructionMinHullFraction
	if floor < 1 {
		floor = 1 // a hull too small to divide still buys something, or it could never buy at all
	}
	// THE CAP IS THE RULINGS #4 GUARANTEE, AND IT IS NOT DECORATIVE. A hull fraction is unbounded
	// above, so on a large enough hauler it would put the CONSTRUCTION floor ABOVE the factory floor
	// it exists to relax — refusing buys that work today, on exactly the fleets best able to make
	// them. Pinned by TestMinTrancheUnitsFor_OnlyEverLowersAndOnlyForConstruction.
	if floor > minViableTrancheUnits {
		floor = minViableTrancheUnits
	}
	return floor
}

// affordableTrancheUnits is the largest tranche of `want` units at `ask` that the working-capital
// reserve can absorb right now, or 0 when even one unit cannot fit.
//
// IT PROPOSES A SIZE; IT DOES NOT DECIDE A SPEND (RULINGS #4). Every caller re-runs
// spendFloorBreached on the size this returns, so the commit-time guard remains the sole authority
// on whether a buy may proceed and still runs on every tranche. This can only make a proposed buy
// SMALLER — it never raises one, never skips the guard, and returning 0 collapses to a decline.
// That direction is pinned by TestFillFromSource_SizesDownButNeverPastTheGuard.
//
// It reads treasury and the reserve through the SAME primitives spendFloorBreached uses
// (treasuryCredits, budgetedReserveFloor) rather than re-deriving either, so the size proposed and
// the size validated can never be computed from different numbers.
//
// Fails CLOSED (returns 0) on an unreadable treasury, exactly as the guard does, and fails OPEN
// (returns want) only when no treasury source is wired at all — the package's fixture contract,
// never the daemon.
func (e *ProductionExecutor) affordableTrancheUnits(ctx context.Context, playerID, ask, want int) int {
	if ask <= 0 || want <= 0 {
		return 0
	}
	if e.apiClient == nil && e.treasury == nil {
		return want // no guard wired: the fixture contract, unchanged
	}
	credits, err := e.treasuryCredits(ctx, playerID)
	if err != nil {
		return 0 // blind: propose nothing, exactly as the guard parks
	}
	treasury := int(credits)
	headroom := treasury - e.budgetedReserveFloor(ctx, playerID, treasury)
	if headroom <= 0 {
		return 0
	}
	if affordable := headroom / ask; affordable < want {
		return affordable
	}
	return want
}

// SpendFloorWouldBreach exports the floor test above so a PLANNER can ask it before choosing a
// step, and returns the reserve actually enforced so the caller names the real number rather than
// re-deriving a base the capital budget may have raised.
//
// IT IS THE SAME GUARD, NOT A SECOND COPY (RULINGS #4). It delegates to spendFloorBreached and adds
// no arithmetic of its own — that is the entire reason it exists as a delegation rather than as a
// treasury read the caller does for itself. A planner re-implementing "credits minus cost against a
// floor" would be free to drift silently, and the drift would be invisible because a precheck that
// is merely WRONG still returns a plausible boolean.
//
// IT IS A PURE READ: no tranche sized, no reservation taken, nothing here can let a spend through
// that the per-tranche guard would refuse. The commit-time check still runs on every tranche after
// it, so this is strictly a chance to DECLINE EARLIER and never a substitute — the pre-dispatch
// answer comes from a CACHED quote, and only the commit-time guard sees the live ask.
//
// It inherits the guard's directions verbatim, and both are the ones a planner wants: FAIL CLOSED
// on an unreadable treasury (report breached, so the caller dispatches nothing it cannot pay for),
// and fail OPEN when no treasury source is wired at all (the fixture contract, never the daemon).
func (e *ProductionExecutor) SpendFloorWouldBreach(ctx context.Context, playerID, projectedCost int) (bool, int) {
	return e.spendFloorBreached(ctx, playerID, projectedCost)
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
// The treasury read here is deliberately independent of spendFloorBreached: factory input buys
// are low-frequency — one per market visit, after a navigate+dock — so a second read costs
// little next to keeping the two guards decoupled, each with its own legible park reason. It is
// the ledger-backed read, so on the common path there is no API call at all.
//
// WHY A LEDGER BALANCE IS SOUND FOR THE CONCURRENT CAP. The cap subtracts in-flight RESERVATIONS
// from a base balance, precisely because a balance read cannot see spend that has not committed
// yet. Committed spend is in the transaction ledger (every transaction records its
// post-transaction balance); uncommitted spend is in the reservation ledger. NOTHING FALLS
// BETWEEN THE TWO: the cargo transaction handler records the transaction SYNCHRONOUSLY, in the
// same call, before the buy returns — and the reservation is released only after that call
// returns — so there is no window in which a spend is absent from both.
//
// The read stays OUTSIDE the ledger transaction (passed in as a value) so the DB is never held
// open across it.
func (e *ProductionExecutor) reserveConcurrentSpendOrPark(ctx context.Context, playerID, projectedCost int, market, good string) (reservationID string, parked bool) {
	logger := common.LoggerFromContext(ctx)
	if e.spendLedger == nil || (e.apiClient == nil && e.treasury == nil) {
		return "", false
	}

	// Best-effort attribution only: the staleness sweep is time-based, so a missing id costs
	// log legibility and never correctness. It is NOT a scope — the ledger sums per player,
	// which is why hulls inside ONE construction container still serialise against each other.
	containerID := "factory-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	// The treasury read and the floor it is judged against are BOTH taken inside the ledger's own
	// critical section. Reading them out here and passing values in un-counts a sibling that
	// commits its buy and releases its reservation in between: it lands in neither the snapshot
	// nor the SUM. observed* capture what the callback saw so the park log names real numbers.
	var observedTreasury, observedReserve int
	readBudget := func(ctx context.Context) (int64, int, error) {
		credits, err := e.treasuryCredits(ctx, playerID)
		if err != nil {
			return 0, 0, err
		}
		treasury := int(credits)
		// The concurrent cap must serialize against the SAME reserve the per-buy floor enforces,
		// or the two guards disagree on where the line is. That is the BUDGETED floor, resolved
		// against THIS read, so a construction buy cannot slip past the budget via the ledger path.
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
		// Counted separately from a per-buy park: a per-buy park shows up in the coordinator's own
		// logs, while an AGGREGATE denial is otherwise only recoverable by reading the transaction
		// ledger and noticing that several buys shared a balance_after.
		metrics.RecordAggregateSpendDenial("construction_supply")
		// Numbers in the MESSAGE: the container log renderer drops the metadata map.
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
