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
// WHY THE CONTRACT SIDE NEEDS THIS AT ALL. affordableSourceBuyLot already floors this buy against
// common.ImmutableReserveFloor from a live treasury read. That guard is correct and untouched — but
// it is PER-BUY, and a contract hauler does not spend alone. Several buys landing inside one
// check->buy window are each individually affordable while their AGGREGATE takes treasury below the
// reserve, after which the contract engine parks against its own floor with nothing left to earn
// with. A cap serialising only one operation against itself leaves this path free to race the float.
//
// THE FLOOR IS UNCHANGED AND UNWEAKENED (RULINGS #4/#5). This reserves against
// common.ImmutableReserveFloor — the same CONTRACT floor affordableSourceBuyLot enforces, not the
// higher non-contract floor construction uses. That asymmetry is deliberate: the band between the
// two is contract-exclusive so the sole earner is not starved by margin-blind gate fills, and the
// shared ledger preserves it exactly, each operation checking the ONE in-flight total against ITS
// OWN floor. This adds a constraint and removes none.
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
	// A zero/unknown projected cost has no basis to reserve against: reserving 0 records an intent
	// that constrains nobody while still taking a row.
	//
	// This is NOT an unpriced-buy hole and must not be inverted into a park. It would be a
	// fail-OPEN if an unpriced lot could reach the purchase below, but it cannot —
	// affordableSourceBuyLot REFUSES a non-positive unit price outright, upstream and
	// unconditionally, so any buy reaching here has a positive cost. This branch is a defensive
	// contract for a caller with nothing to spend, not a guard judging a real buy; parking here
	// would report a cap denial for a buy no cap refused, while the real refusal belongs to the
	// floor.
	if projectedCost <= 0 {
		return "", false
	}

	logger := common.LoggerFromContext(ctx)
	pid := playerID.Value()

	containerID := "contract-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	// The balance is read INSIDE the ledger's critical section, never before it. Reading out here
	// and passing a value in un-counts a sibling that commits its buy and releases its reservation
	// in between: it appears in neither the snapshot nor the SUM, so the headroom is claimed twice.
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
