package services

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// noteInsufficientCredits enriches and logs a mid-purchase 4600 (a treasury state,
// not a bug), returning the sentinel unchanged for the caller's clean nil-error exit.
func (e *DeliveryExecutor) noteInsufficientCredits(ctx context.Context, err error, shipSymbol, tradeSymbol string, playerID shared.PlayerID, purchaseCost int) error {
	var insufficientErr *ErrInsufficientCredits
	if !errors.As(err, &insufficientErr) {
		return err
	}
	insufficientErr.CreditsNeeded = purchaseCost
	insufficientErr.CreditsAvailable = e.lookupLiveCredits(ctx, playerID)
	common.LoggerFromContext(ctx).Log("WARNING", insufficientErr.Error(), map[string]interface{}{
		"ship_symbol":       shipSymbol,
		"action":            "parked",
		"reason":            "insufficient_credits",
		"trade_symbol":      tradeSymbol,
		"units_attempted":   insufficientErr.UnitsAttempted,
		"credits_needed":    insufficientErr.CreditsNeeded,
		"credits_available": insufficientErr.CreditsAvailable,
	})
	return err
}

// lookupLiveCredits fetches a fresh treasury snapshot for the WARNING log
// enrichment above. Returns -1 if the live lookup itself fails, so the log
// message still emits (with an explicit sentinel value) rather than being
// lost to a second error.
func (e *DeliveryExecutor) lookupLiveCredits(ctx context.Context, playerID shared.PlayerID) int {
	pid := playerID.Value()
	resp, err := e.mediator.Send(ctx, &playerQueries.GetPlayerQuery{PlayerID: &pid})
	if err != nil {
		return -1
	}
	playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
	if !ok || playerResp.Player == nil {
		return -1
	}
	return playerResp.Player.Credits
}

// affordableSourceBuyLot sizes the source-buy lot the flat, immutable working-capital
// reserve floor (common.ImmutableReserveFloor, sp-zq635 §4b / sp-05glh) allows, from a
// live treasury read right before the buy. It is the contract-side analogue of the
// factory's spendFloorBreached and the trade/arb spend-floor, extended by sp-8f8fg:
// an all-or-nothing park deadlocked the sole earner over a small gap (nothing else
// refilled treasury), so when the FULL lot would breach the floor the largest
// affordable lot is bought instead — the floor itself is untouched (RULINGS #4/#5),
// since the partial's projected cost still leaves treasury >= the reserve.
//
// Returns (units, held):
//   - guard unarmed (WithSourceBuyFloor not wired) → (unitsWanted, false): the
//     optional-guard contract every existing caller/test relies on, byte-identical
//   - unpriced lot (unitPrice ≤ 0) → (0, true): fails CLOSED, see sp-gef01 below
//   - treasury unreadable → (0, true): fails CLOSED — a guard whose job is keeping
//     treasury above the reserve must never let a buy through blind
//   - full lot clears the floor → (unitsWanted, false), byte-identical
//   - full lot breaches but ≥ MinPartialSourceBuyUnits are affordable →
//     (floor((treasury−reserve)/unitPrice), false)
//   - anything smaller → (0, true): PARK (not crash), resuming when treasury
//     recovers, as before
func (e *DeliveryExecutor) affordableSourceBuyLot(ctx context.Context, playerID shared.PlayerID, unitsWanted, unitPrice int) (int, bool) {
	if !e.enforceSourceBuyFloor {
		return unitsWanted, false
	}
	logger := common.LoggerFromContext(ctx)

	// UNPRICED LOT — FAIL CLOSED (sp-gef01). A non-positive unit price is not a cheap
	// lot, it is an UNKNOWN one, and both money guards downstream reason in COST:
	//
	//   - this floor computes fullCost = unitsWanted × 0 = 0, and treasury minus 0 is
	//     always above the reserve, so it waves the FULL UNSHRUNK lot through; then
	//   - reserveConcurrentSpendOrPark returns early on projectedCost ≤ 0, so no
	//     reservation is taken either.
	//
	// The buy would then execute at REAL prices with both armed guards blind — a money
	// guard failing OPEN on missing data, the inverse of RULINGS #4. Refusing is the only
	// answer available here: a lot nobody can price cannot be sized against the floor
	// (that is why the partial-lot arithmetic below already requires unitPrice > 0), and
	// reserving 0 would record an intent that constrains nobody.
	//
	// This cannot wedge sourcing. It is strictly narrower than the park that already
	// happens when no market sells the good at all: an unscanned or aged-out good never
	// reaches this guard, because PlanDeliverySourcing errors and the profitability
	// evaluation fails upstream. Only a market row quoting a non-positive ask lands here,
	// and UpsertMarketData rewrites that waypoint's whole price set on the next scan,
	// while the coordinator re-projects the parked delivery each pass.
	if unitPrice <= 0 {
		logger.Log("WARNING", fmt.Sprintf(
			"Contract source-buy of %d units has no positive unit price to project a cost from — parking the buy (fail-closed, sp-gef01). An unpriced lot would clear the working-capital reserve floor and the aggregate spend cap at a projected cost of 0 while charging the real amount; resumes when the market is re-priced",
			unitsWanted), map[string]interface{}{
			"action": "source_buy_floor_park", "reason": "unpriced_lot",
			"units_wanted": unitsWanted, "unit_price": unitPrice,
		})
		return 0, true
	}

	treasury := e.lookupLiveCredits(ctx, playerID)
	if treasury < 0 {
		logger.Log("WARNING", "Contract source-buy: live treasury unreadable for the working-capital reserve check — parking the buy (fail-closed)", map[string]interface{}{
			"action": "source_buy_floor_park", "reason": "treasury_unreadable",
		})
		return 0, true
	}
	const floor = common.ImmutableReserveFloor
	fullCost := int64(unitsWanted) * int64(unitPrice)
	if int64(treasury)-fullCost >= floor {
		return unitsWanted, false
	}
	if unitPrice > 0 {
		affordable := int((int64(treasury) - floor) / int64(unitPrice))
		if affordable >= appContract.MinPartialSourceBuyUnits {
			logger.Log("WARNING", fmt.Sprintf(
				"Contract source-buy full lot would breach the working-capital reserve floor — treasury %d, full cost %d, reserve %d; buying the affordable partial lot of %d units (cost %d) instead so fulfillment keeps advancing (sp-8f8fg)",
				treasury, fullCost, floor, affordable, affordable*unitPrice), map[string]interface{}{
				"action": "source_buy_floor_partial", "treasury": treasury, "full_cost": fullCost, "reserve": floor,
				"units_wanted": unitsWanted, "units_affordable": affordable,
			})
			return affordable, false
		}
	}
	logger.Log("WARNING", fmt.Sprintf(
		"Contract source-buy would breach the working-capital reserve floor — treasury %d, projected cost %d, reserve %d; parking (resumes when treasury recovers)",
		treasury, fullCost, floor), map[string]interface{}{
		"action": "source_buy_floor_park", "reason": "reserve_floor", "treasury": treasury, "projected_cost": fullCost, "reserve": floor,
	})
	return 0, true
}

// ExecutePurchaseLoop executes the multi-trip purchase loop.
//
// projectedUnitAsk is the cached ask the profitability evaluation (and the
// coordinator's sourcing defer gate, sp-1z2h) based its projection on; 0
// disables the ladder cap (no basis to compare against). When a trip realizes
// worse than SourcingLadderCapNumer/Denom (1.5×) of that basis, the loop stops
// buying and delivers what is aboard — the −891k ELECTRONICS incident was
// exactly this shape, a buyer laddering a SCARCE ask upward tranche after
// tranche until the contract filled at any price. The undelivered remainder is
// re-picked-up by the coordinator's next pass, where the defer gate re-projects
// it at live prices (and parks it if still negative). Nothing is skipped.
func (e *DeliveryExecutor) ExecutePurchaseLoop(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	unitsToPurchase int,
	cheapestMarket string,
	projectedUnitAsk int,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, bool, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Cheapest market identified", map[string]interface{}{
		"ship_symbol":   shipSymbol,
		"action":        "identify_market",
		"market_symbol": cheapestMarket,
		"trade_symbol":  tradeSymbol,
	})

	trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
	result.TotalTrips += trips
	logger.Log("INFO", "Multi-trip purchase initiated", map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"action":       "start_multi_trip",
		"trips_needed": trips,
		"trade_symbol": tradeSymbol,
	})

	// sourcingHalted propagates a ladder-cap decision (stop buying a runaway
	// ask) up to the delivery leg so it parks the remainder instead of looping
	// and re-laddering. A full hold ends this loop without setting it.
	sourcingHalted := false
	for trip := 0; trip < trips; trip++ {
		var stop bool
		var err error
		ship, unitsToPurchase, stop, sourcingHalted, err = e.executeSinglePurchaseTrip(ctx, shipSymbol, playerID, ship, tradeSymbol, cheapestMarket, unitsToPurchase, projectedUnitAsk, opContext)
		if err != nil {
			return nil, false, err
		}
		if stop {
			break
		}
	}

	return ship, sourcingHalted, nil
}

// executeSinglePurchaseTrip executes a single purchase trip. It returns two
// distinct stop signals: `stop` ends the trips loop for ANY reason (a full hold
// or a ladder breach), while `sourcingHalted` is set ONLY by a ladder breach —
// a deliberate decision to stop feeding a runaway ask, which the delivery leg
// must honor by delivering what's aboard and parking the remainder rather than
// looping and re-laddering the same ask. A full hold (`stop` without
// `sourcingHalted`) is normal: it just means "go deliver, then come back".
func (e *DeliveryExecutor) executeSinglePurchaseTrip(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	cheapestMarket string,
	unitsToPurchase int,
	projectedUnitAsk int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, int, bool, bool, error) {
	logger := common.LoggerFromContext(ctx)

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

	if unitsThisTrip <= 0 {
		logger.Log("WARNING", "Purchase loop terminated due to no cargo space", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "purchase_loop_ended",
			"reason":      "no_cargo_space",
		})
		return ship, unitsToPurchase, true, false, nil
	}

	// PROACTIVE working-capital reserve floor (sp-zq635 §4b): size the buy BEFORE the
	// flight to market. A lot that would drop treasury below the immutable reserve is
	// shrunk to the largest affordable lot (sp-8f8fg) — and only when even the minimum
	// partial is unaffordable is the buy HELD, parked as ErrInsufficientCredits so the
	// existing park-not-crash path resumes it next tick. This closes the gap where a
	// source-buy leaves treasury above 0 (so the API's reactive 4600 never fires) but
	// under the 50k reserve; the reactive 4600 below stays the backstop. Inert unless
	// WithSourceBuyFloor is wired.
	affordableUnits, held := e.affordableSourceBuyLot(ctx, playerID, unitsThisTrip, projectedUnitAsk)
	if held {
		return nil, 0, false, false, &ErrInsufficientCredits{
			ShipSymbol:     shipSymbol,
			TradeSymbol:    tradeSymbol,
			UnitsAttempted: unitsThisTrip,
		}
	}
	// A floor-shrunk lot leaves treasury AT the reserve — further trips this pass are
	// pointless, so it halts sourcing exactly like a ladder breach: deliver what's
	// aboard, park the remainder for the coordinator's defer gate to re-project.
	floorShrunk := affordableUnits < unitsThisTrip
	unitsThisTrip = affordableUnits

	var err error
	ship, err = e.navigateAndDock(ctx, shipSymbol, cheapestMarket, playerID)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("failed to navigate to market: %w", err)
	}

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: tradeSymbol,
		Units:      unitsThisTrip,
		PlayerID:   playerID,
	}

	// CROSS-OPERATION concurrent spend cap (sp-ps2oc), taken HERE — immediately before the buy
	// and after the flight — rather than beside the per-buy floor above.
	//
	// The floor is sized before the navigate, deliberately (it decides whether the trip is
	// worth flying). A reservation taken there would be held across a multi-minute flight and
	// would wedge the shared budget for every other spender; taken here it lives for the
	// duration of one purchase. That placement is also what acceptance criterion 2 asks for:
	// the reservation is taken BEFORE the API call and released whether it succeeds or fails.
	reservationID, capParked := e.reserveConcurrentSpendOrPark(ctx, playerID, unitsThisTrip*projectedUnitAsk, tradeSymbol, cheapestMarket)
	if capParked {
		// The existing park-not-crash path: the coordinator resumes this next tick, by which
		// time the sibling spend that crowded it out has completed and released.
		return nil, 0, false, false, &ErrInsufficientCredits{
			ShipSymbol:     shipSymbol,
			TradeSymbol:    tradeSymbol,
			UnitsAttempted: unitsThisTrip,
		}
	}
	defer e.releaseSpendReservation(ctx, playerID, reservationID)

	purchaseResp, err := e.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		if IsInsufficientCreditsError(err) {
			return nil, 0, false, false, &ErrInsufficientCredits{
				ShipSymbol:     shipSymbol,
				TradeSymbol:    tradeSymbol,
				UnitsAttempted: unitsThisTrip,
				Cause:          err,
			}
		}
		return nil, 0, false, false, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	unitsToPurchase -= unitsThisTrip

	// SOURCING LADDER CAP: stop feeding an ask that has run away
	// from the projected basis. The tranche just bought stays aboard and gets
	// delivered; only FURTHER buying stops — the remainder re-gates through the
	// coordinator's defer projection at live prices.
	ladderBreached, realizedPerUnit := sourcingLadderBreached(purchaseResp, projectedUnitAsk)
	if ladderBreached {
		logger.Log("WARNING", fmt.Sprintf(
			"Sourcing ladder cap: trip realized %d/unit exceeds %d/%dx projected ask %d for %s at %s - halting purchases with %d units still unsourced, delivering partial load (remainder re-projects through the defer gate next coordinator pass; never-skip stands)",
			realizedPerUnit, appContract.SourcingLadderCapNumer, appContract.SourcingLadderCapDenom,
			projectedUnitAsk, tradeSymbol, cheapestMarket, unitsToPurchase,
		), map[string]interface{}{
			"ship_symbol":       shipSymbol,
			"action":            "sourcing_ladder_cap",
			"trade_symbol":      tradeSymbol,
			"market":            cheapestMarket,
			"realized_per_unit": realizedPerUnit,
			"projected_ask":     projectedUnitAsk,
			"units_unsourced":   unitsToPurchase,
		})
	}

	ship, err = e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("failed to reload ship after purchase: %w", err)
	}

	haltSourcing := ladderBreached || floorShrunk
	return ship, unitsToPurchase, haltSourcing, haltSourcing, nil
}

// sourcingLadderBreached reports whether the trip's realized per-unit price ran
// past SourcingLadderCapNumer/Denom (1.5×) of the projected ask, and what it
// realized. A zero/unknown basis, a non-PurchaseCargoResponse, or a zero-unit
// buy never breach (nothing meaningful to compare).
func sourcingLadderBreached(purchaseResp common.Response, projectedUnitAsk int) (bool, int) {
	if projectedUnitAsk <= 0 {
		return false, 0
	}
	resp, ok := purchaseResp.(*shipCargo.PurchaseCargoResponse)
	if !ok || resp == nil || resp.UnitsAdded <= 0 {
		return false, 0
	}
	realizedPerUnit := resp.TotalCost / resp.UnitsAdded
	return realizedPerUnit*appContract.SourcingLadderCapDenom > projectedUnitAsk*appContract.SourcingLadderCapNumer, realizedPerUnit
}

// marketAskBestEffort returns the profitability evaluation's cached market ask
// for good, or 0 when no profitability/market data is available. Used only for
// the savings log line, so it never errors (an unpriced good still withdraws).
func marketAskBestEffort(resp common.Response, good string) int {
	pr, ok := resp.(*contractQueries.ProfitabilityResult)
	if !ok || pr == nil {
		return 0
	}
	return pr.MarketPrices[good]
}
