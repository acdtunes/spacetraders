package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// The growth coordinator's ACT step: read the tick's facts once, then — on a HEAVY wave — assemble
// ONE fully-resolved PurchaseRequest, run it through the package's fail-CLOSED guard stack, and on
// approval buy ONE hull and dedicate it to the trade pool in the same breath. Every decision logs
// its full arithmetic so the captain retunes the blocking knob from evidence.

// growthTickInputs are the per-tick shared reads the wave and the guard stack are derived from.
type growthTickInputs struct {
	treasury   int64
	treasuryOK bool
	apiUtil    float64
	apiOK      bool
	// heaviesOwned is the BROAD, tag-independent heavy-hull census. heaviesOwnedOK=false ⇒ the
	// heavy cap guard fails CLOSED.
	heaviesOwned   int
	heaviesOwnedOK bool
	// heavyTarget is the ask the fleet is SAVING TOWARD, derived ONCE per tick by
	// common.HeavyReserve. It is NOT a credit count any guard may subtract.
	heavyTarget common.HeavyReserveTarget
	// heavyReserve is the credits actually withheld toward heavyTarget at this tick's treasury.
	// The heavy buy waives its own reserve inside guardAffordability; it is carried for the gauge.
	heavyReserve int64
	// laneDemand is the trade surface in BOTH dimensions the regime judges: the unserved lane COUNT
	// (demand clause and class shortfall) and the DEPTH verdict. ONE read, so ONE readability flag.
	laneDemand   fleetgrowth.LaneDemand
	laneDemandOK bool
	// highWater is the peak balance across one trade cycle — the fleet's demonstrated capacity,
	// and the ONLY balance the reachability clause judges.
	highWater   int64
	highWaterOK bool
	// tradeHulls is the trade-DEDICATED pool count: the current side of demand and the hold-fill
	// term's multiplier.
	tradeHulls   int
	tradeHullsOK bool
	// workingCapital is what the trading fleet's observed activity has spoken for, carried as BOTH
	// arms so the line and the gauges name the binding one. Not-OK REFUSES: unknowable is not zero.
	workingCapital   fleetgrowth.WorkingCapitalTerms
	workingCapitalOK bool
}

// readTickInputs reads the tick's shared inputs once. Every read is fail-safe: a nil reader or an
// error yields readable=false, and the guards fail closed on that.
func (h *RunFleetGrowthCoordinatorHandler) readTickInputs(ctx context.Context, playerID int, cfg growthRunConfig) growthTickInputs {
	in := growthTickInputs{}
	if h.heavyCensus != nil {
		if n, err := h.heavyCensus.HeaviesOwned(ctx, playerID); err == nil {
			in.heaviesOwned, in.heaviesOwnedOK = n, true
		}
	}
	// The reservation is DERIVED once per tick from durable facts, never stored, so the wave and
	// the buy judge the SAME target. common.HeavyReserve is the ONE definition — the arithmetic is
	// never re-derived here.
	if h.heavyYard != nil && in.heaviesOwnedOK {
		if target, err := h.heavyYard.HeavyTarget(ctx, playerID); err == nil {
			in.heavyTarget = common.HeavyReserve(common.HeavyReserveInputs{
				CapabilityOpen:  target.CapabilityOpen,
				HeaviesOwned:    in.heaviesOwned,
				HeavyCap:        cfg.HeavyCap,
				TargetYardPrice: target.PurchasePrice,
			})
		}
	}
	if h.treasury != nil {
		if c, ok, err := h.treasury.Treasury(ctx, playerID); err == nil {
			in.treasury, in.treasuryOK = c, ok
		}
	}
	if in.treasuryOK {
		in.heavyReserve = in.heavyTarget.HoldAt(in.treasury)
	}
	if h.apiUtil != nil {
		if u, ok, err := h.apiUtil.UtilizationPct(ctx); err == nil {
			in.apiUtil, in.apiOK = u, ok
		}
	}
	if h.lanes != nil {
		if demand, ok, err := h.lanes.LaneDemand(ctx, playerID); err == nil {
			in.laneDemand, in.laneDemandOK = demand, ok
		}
	}
	// The demonstrated-capacity read. Its window is the SHARED one — the drain measures the same
	// fleet over the same span, and two windows would be two answers to one question.
	if h.highWater != nil {
		if pid, perr := shared.NewPlayerID(playerID); perr == nil {
			if hw, ok, err := h.highWater.TreasuryHighWaterSince(ctx, pid, h.clock.Now().Add(-fleetgrowth.TradeCycleWindow)); err == nil {
				in.highWater, in.highWaterOK = hw, ok
			}
		}
	}
	if h.tradeHulls != nil {
		if n, err := h.tradeHulls.TradeHulls(ctx, playerID); err == nil {
			in.tradeHulls, in.tradeHullsOK = n, true
		}
	}
	h.readWorkingCapital(ctx, playerID, cfg, &in)
	return in
}

// readWorkingCapital derives the credits the trading fleet's observed activity has already spoken
// for. FAIL-CLOSED AS A WHOLE: an unreadable ledger or pool count leaves workingCapitalOK false,
// which refuses the buy — an unknowable commitment is NOT a zero one, and reading it as zero hands
// back the cheapest floor available exactly when we understand the least.
//
// A POOL THAT SPENT NOTHING ALL CYCLE IS UNOBSERVED, NOT UNCOMMITTED. Both terms of the formula
// read the SAME rows, so an empty window collapses them together — including the hold-fill term,
// which exists precisely because a hull bought a minute ago has no spend history. A pool of
// all-fresh hulls would otherwise reserve nothing at all above the immutable floor against a
// seven-figure hull, which is the one state the term was added for. Every ledger row carries a
// non-zero amount by its own validation, so a zero largest row IS an empty window.
//
// THE COLD POOL IS THE EXCEPTION, and it is the difference between a guard and a deadlock: with no
// trade hull standing there is no cargo commitment to protect, and refusing on absence would make
// the FIRST trade hull unbuyable forever.
func (h *RunFleetGrowthCoordinatorHandler) readWorkingCapital(ctx context.Context, playerID int, cfg growthRunConfig, in *growthTickInputs) {
	if h.outflow == nil || !in.tradeHullsOK {
		return
	}
	obs, err := h.outflow.CargoOutflowSince(ctx, playerID, h.clock.Now().Add(-fleetgrowth.TradeCycleWindow))
	if err != nil {
		return
	}
	if in.tradeHulls > 0 && obs.Largest == 0 {
		return
	}
	in.workingCapital = fleetgrowth.WorkingCapital(cfg.RunwayMilliHours, obs, in.tradeHulls)
	in.workingCapitalOK = true
}

// readHeavyDemand sizes the trade pool from the tick's inputs: one wanted hull per unserved
// profitable lane beyond the pool. It reuses the pure demand math rather than restating it, and
// fails CLOSED on either blind input — buying trade hulls against a demand signal we cannot see is
// exactly the runaway the guard stack exists to prevent.
func (h *RunFleetGrowthCoordinatorHandler) readHeavyDemand(in growthTickInputs) ClassDemand {
	if !in.tradeHullsOK {
		return unreadableHeavy("trade-pool count unreadable — heavies fail closed")
	}
	if !in.laneDemandOK {
		return unreadableHeavy("unserved-lane signal unreadable — heavies fail closed")
	}
	return computeHeavyDemand(heavyDemandInputs{
		CurrentHeavies: in.tradeHulls,
		UnservedLanes:  in.laneDemand.UnservedLanes,
	})
}

// advanceShortfallDwell moves the anti-thrash WINDOW every tick, on the DEMAND alone. It is advanced
// HERE (where the tick state lives) and JUDGED by guardDemand.
//
// IT ANCHORS RATHER THAN COUNTS: set once when a shortfall appears and left alone while it stands,
// so the window grows with wall clock however often the coordinator looked. DELIBERATELY OUTSIDE THE
// WAVE GATE — resetting on a wave flip would measure the regime rather than the demand. An UNREADABLE
// demand clears it: a blind tick is not evidence the shortfall held.
func (h *RunFleetGrowthCoordinatorHandler) advanceShortfallDwell(st *growthState, d ClassDemand, now time.Time) {
	if !d.Readable || d.Shortfall() <= 0 {
		st.heavyShortfallSince = time.Time{}
		return
	}
	if st.heavyShortfallSince.IsZero() {
		st.heavyShortfallSince = now
	}
}

// buyHeavy runs the tick's demand→guard→buy for the heavy class. It reports whether a hull was
// bought, whether there was unmet demand that did NOT result in a buy (feeding the zero-effect
// alarm), and the first failing guard when the guard stack named one — which is what lets the stall
// verdict say WHY rather than merely that nothing happened. It never returns an error: a tick that
// cannot buy simply does not buy.
func (h *RunFleetGrowthCoordinatorHandler) buyHeavy(
	ctx context.Context,
	cmd *RunFleetGrowthCoordinatorCommand,
	cfg growthRunConfig,
	d ClassDemand,
	in growthTickInputs,
	st *growthState,
) (bought bool, unmetNoBuy bool, blocked GuardName, blockedKnown bool) {
	logger := common.LoggerFromContext(ctx)

	// Fail-closed: an unreadable demand signal never buys.
	if !d.Readable {
		logger.Log("INFO", fmt.Sprintf("Fleet growth: demand unreadable — no buy (%s)", d.Reason), map[string]interface{}{
			"action": "growth_demand_unreadable", "container_id": cmd.ContainerID,
		})
		return false, false, "", false
	}
	if d.Shortfall() <= 0 {
		return false, false, "", false
	}

	// The working-capital rung, ahead of the request so an unreadable ledger cannot reach the
	// guard as a zero commitment.
	if !in.workingCapitalOK {
		logger.Log("INFO", "Fleet growth: cargo commitment unreadable — no buy", map[string]interface{}{
			"action": "growth_working_capital_unreadable", "container_id": cmd.ContainerID,
		})
		return false, true, "", false
	}

	req, yard := h.buildPurchaseRequest(ctx, cmd, cfg, d, in, st)
	decision := EvaluateGuards(req)

	logger.Log("INFO", fmt.Sprintf("Fleet growth heavy buy-decision (%s): %s", decisionWord(decision), decision.Arithmetic()), map[string]interface{}{
		"action": "growth_decision", "container_id": cmd.ContainerID,
		"approved": decision.Approved, "blocked_by": string(decision.BlockedBy), "ship_type": req.ShipType, "price": req.Price, "yard": yard,
	})

	if !decision.Approved {
		if h.metrics != nil {
			h.metrics.RecordBlocked(HullClassHeavy, decision.BlockedBy)
		}
		return false, true, decision.BlockedBy, true
	}

	// An unwired purchaser evaluates + logs the APPROVED buy but spends nothing — loudly, and
	// still counting toward the zero-effect alarm.
	if h.purchaser == nil {
		logger.Log("WARN", fmt.Sprintf("Fleet growth APPROVED but no purchaser wired — WOULD BUY %s @ %d at %s (mis-wire: the coordinator is armed but cannot spend)", req.ShipType, req.Price, yard), map[string]interface{}{
			"action": "growth_no_purchaser", "container_id": cmd.ContainerID,
		})
		return false, true, "", false
	}

	res, err := h.purchaser.BuyAndDedicate(ctx, BuyOrder{PlayerID: cmd.PlayerID, Class: HullClassHeavy, ShipType: req.ShipType, Yard: yard, ExpectedPrice: req.Price, ContainerID: cmd.ContainerID})
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Fleet growth heavy buy failed: %v", err), map[string]interface{}{
			"action": "growth_buy_error", "container_id": cmd.ContainerID,
		})
		return false, true, "", false
	}

	if h.metrics != nil {
		h.metrics.RecordPurchase(HullClassHeavy)
		// Bind on the ACTUAL price paid, not the quote: the premium is only honest if it measures
		// what left the treasury.
		h.metrics.ObserveHeavyPricePremium(strconv.Itoa(cmd.PlayerID), res.Price, req.CheapestKnownPrice)
	}
	if h.notifier != nil {
		note := fmt.Sprintf("fleet growth bought %s (%s) @ %d, dedicated=%v — demand %d/%d", res.ShipSymbol, req.ShipType, res.Price, res.Dedicated, d.Demand, d.Current)
		if nerr := h.notifier.NotifyPurchase(ctx, cmd.PlayerID, HullClassHeavy, req.ShipType, res.Price, note); nerr != nil {
			logger.Log("WARN", fmt.Sprintf("Fleet growth purchase notice failed: %v", nerr), nil)
		}
	}
	logger.Log("INFO", fmt.Sprintf("Fleet growth BOUGHT %s @ %d at %s, dedicated=%v (demand %d, current %d)", res.ShipSymbol, res.Price, yard, res.Dedicated, d.Demand, d.Current), map[string]interface{}{
		"action": "growth_bought", "container_id": cmd.ContainerID,
		"ship_symbol": res.ShipSymbol, "price": res.Price, "dedicated": res.Dedicated,
	})
	return true, false, "", false
}

// buildPurchaseRequest resolves the tick's candidate heavy purchase from the demand, the run config
// and the tick's shared reads. It fills the SAME PurchaseRequest the whole package's guard stack
// judges — a second money-guard implementation for one more spender is exactly the drift the shared
// stack exists to prevent.
func (h *RunFleetGrowthCoordinatorHandler) buildPurchaseRequest(
	ctx context.Context,
	cmd *RunFleetGrowthCoordinatorCommand,
	cfg growthRunConfig,
	d ClassDemand,
	in growthTickInputs,
	st *growthState,
) (PurchaseRequest, string) {
	shipType, price, cheapest, yard, priceOK := h.resolveHullPrice(ctx, cmd, cfg, cfg.ShipTypeHeavies)

	return PurchaseRequest{
		Class:    HullClassHeavy,
		ShipType: shipType,

		Shortfall: d.Shortfall(),
		// The anti-thrash window as facts: how long the shortfall stood, and the pool it stands against.
		ShortfallHeld:  st.heldFor(h.clock.Now()),
		ShortfallDwell: cfg.ShortfallDwell,
		PoolCurrent:    d.Current,

		HeaviesOwned:         in.heaviesOwned,
		HeavyCap:             cfg.HeavyCap,
		HeaviesOwnedReadable: in.heaviesOwnedOK,

		// The derived hold-back for the NEXT heavy. guardAffordability waives it for the heavy
		// purchase itself (it would otherwise reserve against itself).
		HeavyReserve: in.heavyReserve,

		// One hull per tick: the next tick re-reads the treasury this one spent from.
		PurchasesThisTick: 0,
		PerTickCap:        growthPurchaseCapPerTick,

		Price:              price,
		PriceReadable:      priceOK,
		CheapestKnownPrice: cheapest,
		MaxPriceClass:      cfg.MaxPriceHeavies,
		MaxPremiumPct:      cfg.MaxPremiumOverCheapestPct,

		LiveTreasury:      in.treasury,
		TreasuryReadable:  in.treasuryOK,
		MarginOverFloor:   cfg.PurchaseMarginOverFloor,
		TreasuryPctPerBuy: cfg.TreasuryPctPerPurchase,

		// The trading fleet's observed commitment, ABOVE the immutable floor and never instead of it,
		// and NOT waived for this buy: a hull bought out of the money the pool is about to spend on
		// cargo stalls the trades that fund everything. The arms name the binding one.
		WorkingCapital:     in.workingCapital.Credits(),
		WorkingCapitalArms: in.workingCapital,

		APIUtilPct:      in.apiUtil,
		APIUtilReadable: in.apiOK,
		APIUtilCeiling:  cfg.APIUtilizationCeilingPct,
	}, yard
}

// resolveHullPrice prices the configured heavy and falls back to the best priceable trade-capable
// hull when it cannot be priced at any reachable yard.
//
// WHY THIS EXISTS. A yard selling the configured heavy is not always discovered, and without a
// fallback the price guard blocks every tick while profitable lanes sit unflown — the pool refusing
// to buy the very hull it is already made of. The preferred type stays the operator's choice and
// wins whenever it can be priced.
//
// IT CHANGES ONLY WHICH HULL IS OFFERED TO THE GUARDS, never whether a guard runs: the substitute
// carries its OWN price and cheapest-known ask into the same PurchaseRequest, so the premium check
// compares like with like. SELF-CORRECTING, because the preferred type is asked FIRST every tick and
// nothing is remembered between them. FAILS CLOSED when no trade-capable type can be priced, and
// logs the substitution per decision so an operator never discovers a changed hull from a ship list.
func (h *RunFleetGrowthCoordinatorHandler) resolveHullPrice(
	ctx context.Context,
	cmd *RunFleetGrowthCoordinatorCommand,
	cfg growthRunConfig,
	preferred string,
) (shipType string, price, cheapest int64, yard string, readable bool) {
	if h.yardPrice == nil {
		return preferred, 0, 0, "", false
	}
	if p, c, y, ok, err := h.yardPrice.PriceFor(ctx, cmd.PlayerID, HullClassHeavy, preferred, cfg.PreferDemandProximalYard); err == nil && ok {
		return preferred, p, c, y, true
	}
	for _, alt := range shipyard.TradeHullPreferenceOrder {
		if alt == preferred {
			continue // already asked, and it could not be priced
		}
		p, c, y, ok, err := h.yardPrice.PriceFor(ctx, cmd.PlayerID, HullClassHeavy, alt, cfg.PreferDemandProximalYard)
		if err != nil || !ok {
			continue
		}
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
			"Fleet growth: preferred hull %s cannot be priced at any reachable yard — substituting %s @ %d at %s for this decision (the preferred type wins back automatically once a yard selling it is found)",
			preferred, alt, p, y), map[string]interface{}{
			"action": "growth_trade_hull_substituted", "container_id": cmd.ContainerID,
			"preferred_ship_type": preferred, "ship_type": alt, "price": p, "yard": y,
		})
		return alt, p, c, y, true
	}
	return preferred, 0, 0, "", false
}
