package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// contractSpendOperation labels this operation on the shared cap — in the reservation's
// attribution and on the aggregate-denial counter. Three operations draw on one treasury, so
// which of them was turned away is the question the counter exists to answer.
const contractSpendOperation = "contract"

// reserveConcurrentSpendOrPark records this source-buy's spend intent on the shared
// cross-operation cap and reports whether it must PARK because the COMBINED in-flight spend
// would breach the working-capital reserve. On the proceed path it returns the reservation id
// the caller must release after the buy.
//
// WHY THE CONTRACT SIDE NEEDS THIS AT ALL. affordableSourceBuyLot already floors this buy
// against common.ImmutableReserveFloor from a live treasury read. That guard is correct and
// untouched — but it is PER-BUY, and a contract hauler does not spend alone. In the sp-ps2oc
// incident three construction_supply buys landed inside 68ms, each individually affordable,
// and the aggregate took treasury 75k below the reserve; the contract engine then parked
// against its own floor with nothing left to earn with. A cap that serialised only
// construction against itself would have left this path free to race the same float.
//
// THE FLOOR IS UNCHANGED AND UNWEAKENED (RULINGS #4/#5). This reserves against
// common.ImmutableReserveFloor — the same 50k contract floor affordableSourceBuyLot enforces,
// not the 150k non-contract floor construction uses. That asymmetry is deliberate and
// pre-existing (sp-q8bon made the 50k–150k band contract-exclusive so the sole earner is not
// starved by margin-blind gate fills), and the shared ledger preserves it exactly: each
// operation checks the ONE in-flight total against ITS OWN floor. This adds a constraint and
// removes none.
//
// Fails OPEN when no cap is wired — the optional-port contract every existing test relies on.
// Fails CLOSED (parks) on any ledger or balance-read error: a cap whose job is protecting the
// reserve must never let a buy through blind.
func (e *DeliveryExecutor) reserveConcurrentSpendOrPark(
	ctx context.Context,
	playerID shared.PlayerID,
	projectedCost int,
	good, market string,
) (reservationID string, parked bool) {
	if e.spendLedger == nil {
		return "", false
	}
	// A zero/unknown projected cost has no basis to reserve against. The per-buy floor treats
	// an unpriced lot the same way (it cannot size a partial without a unit price), and
	// reserving 0 would record an intent that constrains nobody while still taking a row.
	if projectedCost <= 0 {
		return "", false
	}

	logger := common.LoggerFromContext(ctx)
	pid := playerID.Value()

	containerID := "contract-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	// The balance is read INSIDE the ledger's critical section, never before it. Reading out
	// here and passing a value in is the sp-ps2oc defect: a sibling that commits its buy and
	// releases its reservation in between appears in neither the snapshot nor the SUM, so its
	// spend is silently un-counted and the headroom is claimed twice.
	var observedTreasury int
	readBudget := func(ctx context.Context) (int64, int, error) {
		treasury := e.lookupLiveCredits(ctx, playerID)
		if treasury < 0 {
			return 0, 0, fmt.Errorf("live treasury unreadable")
		}
		observedTreasury = treasury
		return int64(treasury), common.ImmutableReserveFloor, nil
	}

	resID, ok, err := e.spendLedger.Reserve(ctx, pid, containerID, projectedCost, readBudget)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Contract source-buy concurrent-spend-cap error — parking the buy (fail-closed): %v", err), map[string]interface{}{
			"action": "source_buy_cap_park", "reason": "cap_unavailable", "error": err.Error(),
		})
		return "", true
	}
	if !ok {
		metrics.RecordAggregateSpendDenial(contractSpendOperation)
		// Numbers in the MESSAGE: the container log renderer drops the metadata map, so an
		// operator who never opens the ledger still sees why this hauler parked.
		logger.Log("WARNING", fmt.Sprintf(
			"Parked contract source-buy of %s at %s — cross-operation concurrent spend cap: treasury %d minus in-flight reservations from every operation would breach the working-capital reserve %d (this buy %d); resumes when the competing spend completes",
			good, market, observedTreasury, common.ImmutableReserveFloor, projectedCost), map[string]interface{}{
			"action": "source_buy_cap_park", "reason": "concurrent_spend_cap",
			"good": good, "market": market, "projected_cost": projectedCost,
			"treasury": observedTreasury, "reserve": common.ImmutableReserveFloor,
		})
		return "", true
	}
	return resID, false
}

// releaseSpendReservation consumes the reservation once the buy completes, success or failure.
// A failed release is logged, never surfaced: the reservation leaks until the ledger's
// staleness sweep reclaims it, so cleanup can never fail an otherwise-successful buy.
func (e *DeliveryExecutor) releaseSpendReservation(ctx context.Context, playerID shared.PlayerID, reservationID string) {
	if e.spendLedger == nil || reservationID == "" {
		return
	}
	if err := e.spendLedger.Release(ctx, playerID.Value(), reservationID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to release contract spend reservation %s (staleness sweep will reclaim it): %v", reservationID, err), map[string]interface{}{
			"reservation_id": reservationID, "error": err.Error(),
		})
	}
}
