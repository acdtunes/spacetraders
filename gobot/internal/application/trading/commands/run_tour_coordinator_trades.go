package commands

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// tourPlanRun is what every leg and trade of one flown plan shares: the run's
// accumulators plus the plan-scoped sink dispositions the buy gate consults.
type tourPlanRun struct {
	cmd             *RunTourCoordinatorCommand
	response        *RunTourCoordinatorResponse
	netBought       map[string]int
	cumulativeSpend *int64
	shadowSinks     map[shadowSinkKey]bool
	dispositions    tourGoodDispositions
	maxSpend        int64
	reserve         int64
}

func (r tourPlanRun) forPlan(plan *routing.TourPlan, shadowSinks map[shadowSinkKey]bool) tourPlanRun {
	r.shadowSinks = shadowSinks
	r.dispositions = planDispositions(plan)
	return r
}

// legTradesToFly orders a leg's trades sells-before-buys and, once the plan has degraded into a
// discharge run, drops the buys: the tour is flying the rest of the route only to get cargo off
// the hull, and the broken plan is no basis for acquiring more.
func legTradesToFly(trades []routing.TourTrade, discharging bool) []routing.TourTrade {
	ordered := sellsBeforeBuys(trades)
	if !discharging {
		return ordered
	}
	sells := make([]routing.TourTrade, 0, len(ordered))
	for _, t := range ordered {
		if !t.IsBuy {
			sells = append(sells, t)
		}
	}
	return sells
}

// legsCanDischargeHold reports whether any of legs sells (or deposits) a good the hull is
// holding right now — the "reachable bid" test at plan scope. The hull is already standing at
// the head of that route, so flying it is strictly cash-positive and cannot strand. An
// unreadable hull proves nothing and answers no, degrading exactly as before.
func (h *RunTourCoordinatorHandler) legsCanDischargeHold(ctx context.Context, cmd *RunTourCoordinatorCommand, legs []routing.TourLeg) bool {
	if len(legs) == 0 {
		return false
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false
	}
	held := h.tourShipState(ship).Cargo // reserved cargo excluded: the executor refuses to sell it
	for _, leg := range legs {
		for _, trade := range leg.Trades {
			if !trade.IsBuy && held[trade.Good] > 0 {
				return true
			}
		}
	}
	return false
}

// executeTrade live-re-verifies one trade against the plan and, if within tolerance,
// dispatches it. Returns executed=false (a skip) when the live price has degraded past
// tourPriceTolerancePct or cannot be read — the caller degrades the leg and re-plans.
func (h *RunTourCoordinatorHandler) executeTrade(
	ctx context.Context,
	run tourPlanRun,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	legSells map[string]*tourSinkSale,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// A DEPOSIT tranche is a haul-to-storage transfer, not a market trade — there is
	// no live market bid to re-verify (its value is the synthetic bid). Route it
	// straight to the warehouse deposit path, BYPASSING the live-price observe +
	// tolerance gate the market trades below run.
	if trade.IsDeposit {
		return h.executeDeposit(ctx, run, leg, legIdx, trade)
	}

	live, oerr := h.legs.observeGood(ctx, leg.Waypoint, trade.Good, run.cmd.PlayerID)
	if oerr != nil {
		logger.Log("WARNING", fmt.Sprintf("No live price for %s at %s - skipping (will re-plan): %v", trade.Good, leg.Waypoint, oerr), map[string]interface{}{
			"good": trade.Good, "waypoint": leg.Waypoint, "error": oerr.Error(),
		})
		return false, nil
	}
	planned := trade.ExpectedUnitPrice
	if planned <= 0 {
		return false, nil
	}
	// sell_price is the bid we receive, purchase_price the ask we pay. Read the
	// other way round until sp-en5h7, correct only against transposed rows.
	livePrice := live.SellPrice() // sell: what the market pays us
	if trade.IsBuy {
		livePrice = live.PurchasePrice() // buy: what we pay
	}
	degradationPct := math.Abs(float64(livePrice-planned)) / float64(planned) * 100
	if degradationPct > tourPriceTolerancePct {
		logger.Log("WARNING", fmt.Sprintf("Leg %d %s %s: live %d vs planned %d = %.1f%% moved (> %d%%) - skipping, will re-plan",
			legIdx, tradeSide(trade), trade.Good, livePrice, planned, degradationPct, tourPriceTolerancePct), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live": livePrice, "planned": planned, "degradation_pct": degradationPct,
		})
		return false, nil
	}

	if trade.IsBuy {
		return h.executeBuy(ctx, run.cmd, leg, legIdx, trade, run.shadowSinks, run.dispositions, live, run.response, run.netBought, run.cumulativeSpend, run.maxSpend, run.reserve)
	}
	return h.executeSell(ctx, run, leg, legIdx, trade, live, legSells)
}

func (h *RunTourCoordinatorHandler) executeBuy(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	shadowSinks map[shadowSinkKey]bool,
	dispositions tourGoodDispositions,
	live *market.TradeGood,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	cumulativeSpend *int64,
	maxSpend, reserve int64,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	liveAsk := live.PurchasePrice() // the ASK is purchase_price — what we pay (sp-en5h7)
	if liveAsk <= 0 {
		return false, nil
	}
	units := trade.Units
	if space := ship.AvailableCargoSpace(); space < units {
		units = space
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv // each transaction ≤ tradeVolume
	}
	if maxSpend > 0 {
		remaining := maxSpend - *cumulativeSpend
		if remaining <= 0 {
			logger.Log("WARNING", "Cumulative tour spend cap reached - skipping buy", map[string]interface{}{
				"good": trade.Good, "cap": maxSpend, "spent": *cumulativeSpend,
			})
			return false, nil
		}
		if affordable := int(remaining / int64(liveAsk)); affordable < units {
			units = affordable
		}
	}

	// Firm-sink gate (sp-pcxju): "we absolutely cannot buy cargo and not sell it." Bound
	// this buy to the depth THIS hull's OWN downstream sink reservation can still absorb,
	// and refuse entirely when no firm sink is held — validated HERE at buy EXECUTION (not
	// just at plan time), so a sink saturated or dropped since planning cannot slip an
	// on-spec buy. sinkBound < 0 is "not gated" (no ledger wired / warehouse-bound good),
	// keeping the happy path byte-identical; 0 fails closed (RULINGS #4 — never buy blind).
	if sinkBound := h.firmSinkUnits(ctx, cmd, trade.Good, dispositions); sinkBound >= 0 {
		if sinkBound == 0 {
			metrics.RecordAbsorptionConsultVerdict(cmd.PlayerID, "skip_reserved", absorptionEngineTour)
			logger.Log("WARNING", fmt.Sprintf("Tour leg %d: no firm sink held for %s at %s - not buying (sp-pcxju: never buy cargo we cannot sell)",
				legIdx, trade.Good, leg.Waypoint), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint, "reason": "no_firm_sink",
			})
			return false, nil
		}
		if sinkBound < units {
			metrics.RecordAbsorptionConsultVerdict(cmd.PlayerID, "skip_reserved", absorptionEngineTour)
			logger.Log("INFO", fmt.Sprintf("Tour leg %d: shrinking buy of %s from %d to %d units to fit firm sink depth (sp-pcxju)",
				legIdx, trade.Good, units, sinkBound), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "planned_units": units, "firm_sink_units": sinkBound,
			})
			units = sinkBound
		}
	}

	if units <= 0 {
		return false, nil
	}

	// Working-capital spend floor at BUY time (RULINGS #4). Re-read the LIVE balance
	// immediately before the purchase and SHRINK this tranche to the units the
	// reserve can still afford, rather than an all-or-nothing skip — a floor that
	// binds should still buy what fits beneath it. Skip only if even one unit
	// pierces the floor; fail CLOSED (no spend, re-plan) if the balance can't be
	// read; proceed unconstrained when no live client is wired (the guard's
	// optional-port contract, which every nil-apiClient test relies on). This shares
	// the circuit's live-treasury seam (reserveHeadroom) rather than forking a
	// parallel read. NOTE: the read is live but not atomic with the purchase, so
	// concurrent hulls draining the shared treasury in the read→buy window remain a
	// residual; this binds the floor at execution, it does not lock it.
	headroom, liveBalance, guardOn, readable := h.legs.reserveHeadroom(ctx, cmd.PlayerID, int(reserve))
	if guardOn && !readable {
		response.CapitalDeniedBuys++
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: live balance unreadable at buy time for %d %s @ %d (reserve %d) - not spending, will re-plan (fail-closed)",
			legIdx, units, trade.Good, liveAsk, reserve), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "planned_units": units, "ask": liveAsk, "reserve": reserve,
		})
		return false, nil
	}
	if guardOn {
		floorMaxUnits := headroom / liveAsk // floor-respecting max; headroom may be <= 0 (skip)
		if floorMaxUnits <= 0 {
			// The guard is right to refuse and stays exactly as it is; what the run records
			// here is the DIAGNOSIS — this tour was denied capital, so the loop must not read
			// its zero trades as a dead ground.
			response.CapitalDeniedBuys++
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "skip") // floor bound the whole tranche
			logger.Log("WARNING", fmt.Sprintf("Tour leg %d: buy of %d %s @ %d would breach working-capital floor - live balance %d, reserve %d, even 1 unit pierces - skipping, will re-plan",
				legIdx, units, trade.Good, liveAsk, liveBalance, reserve), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "planned_units": units, "ask": liveAsk, "live_balance": liveBalance, "reserve": reserve,
			})
			return false, nil
		}
		if floorMaxUnits < units {
			metrics.RecordTourReserveFloorEngagement(cmd.PlayerID, "shrink") // floor cut the tranche to fit
			logger.Log("WARNING", fmt.Sprintf("Tour leg %d: shrinking buy of %s from %d to %d units @ %d to respect working-capital floor (live balance %d, reserve %d)",
				legIdx, trade.Good, units, floorMaxUnits, liveAsk, liveBalance, reserve), map[string]interface{}{
				"leg": legIdx, "good": trade.Good, "planned_units": units, "floor_max_units": floorMaxUnits, "ask": liveAsk, "live_balance": liveBalance, "reserve": reserve,
			})
			units = floorMaxUnits
		}
	}

	plannedAt := h.clock.Now()
	// Arm the per-tranche buy ceiling at the plan's tolerated ask — the planned basis
	// plus the same tourPriceTolerancePct the leg-level gate above applied. That gate
	// checked only the first live read; this bounds the intra-buy ladder a
	// multi-tranche purchase walks up itself, aborting the remainder once a
	// sub-tranche prices past the plan's tolerance.
	planned := trade.ExpectedUnitPrice
	maxAskPerUnit := planned + planned*tourPriceTolerancePct/100
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID, maxAskPerUnit)
	if err != nil {
		return false, fmt.Errorf("purchase of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: buy ceiling aborted %s at %s (live ask %d > ceiling %d) - skipping, will re-plan",
			legIdx, trade.Good, leg.Waypoint, buyResp.CeilingObservedAsk, maxAskPerUnit), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
		})
		return false, nil
	}
	*cumulativeSpend += int64(buyResp.TotalCost)
	response.TotalSpent += int64(buyResp.TotalCost)
	response.TradesExecuted++
	netBought[trade.Good] += buyResp.UnitsAdded
	h.recordLeg(ctx, cmd, trading.LegEngineSolver, leg, legIdx, trade, buyResp.UnitsAdded, realizedUnitPrice(buyResp.TotalCost, buyResp.UnitsAdded), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: bought %d %s at %s (cost %d)", legIdx, buyResp.UnitsAdded, trade.Good, leg.Waypoint, buyResp.TotalCost), nil)
	// A buy that LANDED on ground carrying an outstanding EXECUTED recovery shadow is
	// a cross-plan ladder incident — the fleet re-buying into a market still
	// recovering from its own dump. Pure observation off the plan-time probe set; a
	// nil-map read is false, so this is inert when no shadows were netted.
	if buyResp.UnitsAdded > 0 && shadowSinks[shadowSinkKey{leg.Waypoint, trade.Good}] {
		metrics.RecordAbsorptionLadderIncident(cmd.PlayerID, trade.Good)
	}
	return true, nil
}

func (h *RunTourCoordinatorHandler) executeSell(
	ctx context.Context,
	run tourPlanRun,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	live *market.TradeGood,
	legSells map[string]*tourSinkSale,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)
	cmd, response, netBought := run.cmd, run.response, run.netBought

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}

	// Fail-closed: never sell cargo the hull has reserved as do-not-sell (a staged
	// outfitting module, or an operator-protected good). Skip the leg with a
	// reason=reserved line rather than liquidate a module a coordinator wrongly
	// treated as manifest. tourShipState already keeps reserved cargo out of the
	// planner, so this only fires on a planning leak — the executor refuses
	// independently so a leak can never realize the loss. Returning a skip degrades
	// the leg, and the re-plan (with reserved cargo excluded) drops the doomed sell.
	if ship.IsCargoReserved(trade.Good) {
		logger.Log("INFO", fmt.Sprintf("Tour leg %d: skipped selling %s at %s - cargo is reserved (do-not-sell), held aboard", legIdx, trade.Good, leg.Waypoint), map[string]interface{}{
			"action": "reserved_cargo_skip", "ship_symbol": cmd.ShipSymbol, "good": trade.Good, "waypoint": leg.Waypoint, "reason": "reserved", "leg": legIdx,
		})
		return false, nil
	}

	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if tv := live.TradeVolume(); tv > 0 && tv < units {
		units = tv
	}
	if units <= 0 {
		return false, nil // nothing to sell here (cargo already gone) — not a degrade
	}

	plannedAt := h.clock.Now()
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, trade.Good, units, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("sell of %d %s at %s failed: %w", units, trade.Good, leg.Waypoint, err)
	}
	response.TotalRevenue += int64(sellResp.TotalRevenue)
	response.TradesExecuted++
	dischargePurchaseObligation(netBought, trade.Good, sellResp.UnitsSold)
	// Accumulate the realized units sold into this sink for the per-sink conversion
	// at leg completion. The solver splits a sink's A-cap depth into SEPARATE
	// price-tiered tranches (distinct trades), so a single sink can sell across several
	// executeSell calls in one leg; converting per tranche would record only the first,
	// under-stating the multi-tranche co-dump crush this ledger exists to shadow. The
	// live re-verify tier + trade_volume (stable across a sink's tranches) size the shadow.
	h.noteSinkSale(legSells, trade.Good, sellResp.UnitsSold, live)
	h.recordLeg(ctx, cmd, trading.LegEngineSolver, leg, legIdx, trade, sellResp.UnitsSold, realizedUnitPrice(sellResp.TotalRevenue, sellResp.UnitsSold), plannedAt)
	logger.Log("INFO", fmt.Sprintf("Tour leg %d: sold %d %s at %s (revenue %d)", legIdx, sellResp.UnitsSold, trade.Good, leg.Waypoint, sellResp.TotalRevenue), nil)
	return true, nil
}

// executeDeposit deposits a haul-to-storage tranche into the home warehouse using
// the gas-proven protocol: ReserveSpaceForDeposit → TransferCargo (API) →
// ConfirmDeposit, releasing the reservation on transfer failure. It runs NO
// live-price re-verify (the value is the synthetic bid, not a market price) and
// books ZERO revenue — a deposit is an inventory transfer, not a sale, so no
// ledger transaction row is written (recordLeg is deliberately NOT called) and
// realized P&L is not inflated; the synthetic savings value is logged for
// observability only.
//
// Honest-completion composure (RULINGS #1): a successful deposit decrements
// netBought (the good LEFT the hull into inventory — not stranded). A deposit
// that cannot complete (no warehouse, warehouse full/gone) returns a SKIP
// (executed=false) so the leg degrades and the tour re-plans; the un-deposited
// cargo is then carried as held cargo and the next plan liquidates it at market
// rather than stranding it. An API transfer failure returns an error the runner
// retries (it re-plans cargo-aware from the current hold).
func (h *RunTourCoordinatorHandler) executeDeposit(
	ctx context.Context,
	run tourPlanRun,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
) (bool, error) {
	logger := common.LoggerFromContext(ctx)
	cmd, response, netBought := run.cmd, run.response, run.netBought

	if h.storageCoordinator == nil || h.warehouseFinder == nil || h.mediator == nil {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: deposit of %s planned but storage subsystem unwired - degrading to re-plan (held cargo will liquidate)", legIdx, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	// The deposit sink is the CO-LOCATED warehouse group at the leg's waypoint (the
	// anchor plus any additive-capacity siblings). None running → degrade.
	group := h.warehousesAt(ctx, cmd.PlayerID, leg.Waypoint)
	if len(group) == 0 {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: no running warehouse at %s for %s deposit - degrading to re-plan (held cargo will liquidate)", legIdx, leg.Waypoint, trade.Good), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "waypoint": leg.Waypoint,
		})
		return false, nil
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}
	held := 0
	if c := ship.Cargo(); c != nil {
		held = c.GetItemUnits(trade.Good)
	}
	units := trade.Units
	if held < units {
		units = held
	}
	if units <= 0 {
		return false, nil // nothing to deposit (cargo already gone) — not a degrade
	}

	// Deposit across the group, spilling from the newest member with space into the
	// next as each fills (additive capacity). Each member: reserve atomically → transfer
	// → confirm (Lane B / siphon protocol). "Full" — and the degrade — is reached only
	// when the WHOLE group is saturated.
	deposited := 0
	for deposited < units {
		remaining := units - deposited
		dst := tradingsvc.SelectDepositWarehouse(h.storageCoordinator, group, trade.Good)
		if dst == nil {
			break // every co-located member full or unsupported
		}
		storageShip, reserved, ok := h.storageCoordinator.ReserveSpaceForDeposit(dst.ID(), remaining)
		if !ok || storageShip == nil {
			break // race: space vanished between select and reserve
		}
		move := reserved
		if move > remaining {
			move = remaining
		}
		if _, terr := h.mediator.Send(ctx, &gasCmd.TransferCargoCommand{
			FromShip:   cmd.ShipSymbol,
			ToShip:     storageShip.ShipSymbol(),
			GoodSymbol: trade.Good,
			Units:      move,
			PlayerID:   shared.MustNewPlayerID(cmd.PlayerID),
		}); terr != nil {
			h.storageCoordinator.ReleaseReservedSpace(storageShip.ShipSymbol(), reserved)
			return false, fmt.Errorf("deposit transfer of %d %s to warehouse hull %s failed: %w", move, trade.Good, storageShip.ShipSymbol(), terr)
		}
		h.storageCoordinator.ConfirmDeposit(storageShip.ShipSymbol(), trade.Good, move)
		logger.Log("INFO", fmt.Sprintf("Tour leg %d: deposited %d %s into warehouse %s (savings value %d, no revenue)", legIdx, move, trade.Good, storageShip.WaypointSymbol(), move*trade.ExpectedUnitPrice), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "units": move, "warehouse": dst.ID(),
			"storage_ship": storageShip.ShipSymbol(), "savings_value": move * trade.ExpectedUnitPrice,
			"operation_type": "warehouse_deposit",
		})
		deposited += move
	}

	if deposited <= 0 {
		logger.Log("WARNING", fmt.Sprintf("Tour leg %d: warehouse group at %s has no space for %d %s (all %d co-located op(s) full) - degrading to re-plan (held cargo will liquidate at market)", legIdx, leg.Waypoint, units, trade.Good, len(group)), map[string]interface{}{
			"leg": legIdx, "good": trade.Good, "units": units, "waypoint": leg.Waypoint, "group_size": len(group),
		})
		return false, nil // full → degrade → next plan liquidates the held cargo
	}

	response.TradesExecuted++
	dischargePurchaseObligation(netBought, trade.Good, deposited) // left the hull into inventory — not stranded
	return true, nil
}

// warehousesAt returns ALL RUNNING warehouse operations parked at waypoint — the
// co-located additive-capacity group (e.g. light + heavy warehouses at the same
// waypoint, whose slots sum). Empty when none is running there or the finder is
// unwired (fail closed — the caller degrades to pure arb for that leg). A stale
// zombie row is included but contributes 0 free space and is never chosen as a
// deposit target, so aggregation composes with the newest-wins zombie fix.
func (h *RunTourCoordinatorHandler) warehousesAt(ctx context.Context, playerID int, waypoint string) []*storage.StorageOperation {
	if h.warehouseFinder == nil {
		return nil
	}
	ops, err := h.warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}
	return tradingsvc.RunningWarehousesAtWaypoint(ops, waypoint)
}

// sellsBeforeBuys reorders a leg's trades so every sell precedes every buy, preserving
// relative order within each side (the planner emits them this way; the executor
// enforces it so the hold is freed before it is refilled).
func sellsBeforeBuys(trades []routing.TourTrade) []routing.TourTrade {
	out := make([]routing.TourTrade, 0, len(trades))
	for _, t := range trades {
		if !t.IsBuy {
			out = append(out, t)
		}
	}
	for _, t := range trades {
		if t.IsBuy {
			out = append(out, t)
		}
	}
	return out
}

func realizedUnitPrice(total, units int) int {
	if units <= 0 {
		return 0
	}
	return total / units
}

func tradeSide(t routing.TourTrade) string {
	if t.IsBuy {
		return "buy"
	}
	return "sell"
}

// recordLeg persists one leg and emits its price drift. engine is REQUIRED and names the
// path calling in — solver, look-back or liquidation. It is a parameter rather than
// something inferred from legIdx so that a new execution path cannot compile without saying
// who it is: inference would file an unrecognised path under solver, quietly polluting the
// population that grades the market model.
func (h *RunTourCoordinatorHandler) recordLeg(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	engine trading.LegEngine,
	leg routing.TourLeg,
	legIdx int,
	trade routing.TourTrade,
	realizedUnits, realizedUnitPrice int,
	plannedAt time.Time,
) {
	// Emit the realized-vs-planned unit-price drift (feeds the Plan vs Realized Drift %
	// panel) keyed by side and by WHICH plan basis produced the expectation. Independent
	// of the telemetry repo — a nil telemetry sink must not suppress it — and nil-safe,
	// so a metrics miss never touches the trade path (RULINGS #4). ExpectedUnitPrice is
	// the plan basis; a non-positive basis is skipped downstream.
	//
	// The basis label matters because these are not the same measurement (sp-fpgl2). A
	// solver leg's ExpectedUnitPrice is the planner's own projection, so its drift tests
	// the market model. A look-back leg's is the CACHED SourceAsk the manifest was built
	// from, and the buy is gated to a tolerance band around that very number, so a fresh
	// cache reproduces itself: those legs measured a median absolute error of EXACTLY
	// 0.000% over 1423 production rows against the solver's 0.518%. Averaged together
	// they read as one number that describes neither.
	metrics.ObserveTourLegPriceDrift(tradeSide(trade), legPlanBasis(engine), float64(trade.ExpectedUnitPrice), float64(realizedUnitPrice))
	if h.telemetry == nil {
		return
	}
	err := h.telemetry.RecordLeg(ctx, trading.TourLegTelemetry{
		TourID:            cmd.ContainerID,
		ShipSymbol:        cmd.ShipSymbol,
		Engine:            engine,
		LegIndex:          legIdx,
		Waypoint:          leg.Waypoint,
		Good:              trade.Good,
		IsBuy:             trade.IsBuy,
		PlannedUnits:      trade.Units,
		RealizedUnits:     realizedUnits,
		PlannedUnitPrice:  trade.ExpectedUnitPrice,
		RealizedUnitPrice: realizedUnitPrice,
		PlannedAt:         plannedAt,
		RealizedAt:        h.clock.Now(),
		PlayerID:          cmd.PlayerID,
	})
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to record tour leg telemetry: %v", err), map[string]interface{}{
			"tour": cmd.ContainerID, "leg": legIdx, "good": trade.Good, "error": err.Error(),
		})
	}
}

// legPlanBasis names the plan basis behind a leg's ExpectedUnitPrice, using the drift
// metric's own label vocabulary. It reads the engine the executing path DECLARED, so the
// basis label on the metric and the engine column on the row cannot drift apart — they are
// now one fact with two renderings rather than two independent classifications of the same
// leg.
//
// A liquidation reports its own basis rather than borrowing the solver's. No series moves
// either way: it leaves its plan basis at zero rather than inventing one, and a non-positive
// basis is skipped before any drift counter is touched — so this label never materialises.
// It is the honest value for a leg that is genuinely not solver evidence.
func legPlanBasis(engine trading.LegEngine) string {
	switch engine {
	case trading.LegEngineLookback:
		return metrics.PlanBasisLookback
	case trading.LegEngineLiquidation:
		return metrics.PlanBasisLiquidation
	default:
		return metrics.PlanBasisSolver
	}
}
