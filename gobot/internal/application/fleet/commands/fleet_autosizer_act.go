package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The ACT step: the coordinator reads the tick's shared inputs (treasury, era clock,
// API utilization, total fleet size) once, then for each class with unmet demand assembles a fully
// -resolved PurchaseRequest, runs it through the fail-closed guard stack, and on approval buys
// ONE hull and dedicates it to its class fleet IN THE SAME BREATH (dedicate-at-purchase). Every
// decision logs its full arithmetic (the park-line idiom), so the captain retunes the
// blocking knob from evidence. Purchases are bounded per tick by
// purchase_cap_per_tick; the heavy class additionally requires its unserved-lane shortfall to
// persist heavy_unserved_lanes_min consecutive ticks (anti-thrash). A tick that has demand but buys
// nothing for zero_effect_alarm_ticks consecutive passes raises ONE edge-triggered alarm (the
// no-silent-dry-run corollary).

// --- buy-path ports (wired by setters at boot; every one nil-safe, fail-closed on unread) ---

// TreasuryReader reads the player's live credit balance. readable=false ⇒ the treasury guards fail
// closed (a buy must never proceed on an unknown balance).
type TreasuryReader interface {
	Treasury(ctx context.Context, playerID int) (credits int64, readable bool, err error)
}

// EraClockReader reads the hours remaining until the universe reset (era end). readable=false ⇒
// the era-payback guard fails closed (a hull must pay back before it evaporates at reset).
type EraClockReader interface {
	HoursToEraEnd(ctx context.Context) (hours float64, readable bool, err error)
}

// APIUtilizationReader reads the sustained request-utilization percent. readable=false ⇒ the
// API-util guard fails CLOSED: an unreadable/absent utilization surface holds concurrency growth.
type APIUtilizationReader interface {
	UtilizationPct(ctx context.Context) (pct float64, readable bool, err error)
}

// FleetSizeReader reads the player's total hull count for the absolute fleet ceiling.
type FleetSizeReader interface {
	TotalHulls(ctx context.Context, playerID int) (int, error)
}

// YardPriceReader reads the purchase price for a ship type at the preferred yard (demand-proximal
// when preferProximal), plus the cheapest known yard ask (for the premium ceiling) and the yard
// waypoint the buy targets. readable=false ⇒ the price guards fail closed.
type YardPriceReader interface {
	PriceFor(ctx context.Context, playerID int, class HullClass, shipType string, preferProximal bool) (price, cheapest int64, yard string, readable bool, err error)
}

// BuyOrder is one approved hull purchase, dedicated to its class fleet at purchase time.
type BuyOrder struct {
	PlayerID      int
	Class         HullClass
	ShipType      string
	Yard          string
	ExpectedPrice int64
}

// BuyResult reports the executed purchase.
type BuyResult struct {
	ShipSymbol string
	Price      int64
	Dedicated  bool
}

// Purchaser buys ONE hull and dedicates it to its class fleet in the same breath (dedicate-at
// -purchase). The concrete impl buys through the batch-purchase money-integrity path and
// stamps the ship's DedicatedFleet before any coordinator tick can see an undedicated idle hull.
type Purchaser interface {
	BuyAndDedicate(ctx context.Context, order BuyOrder) (BuyResult, error)
}

// PurchaseNotifier posts a captain purchase notice — a buy is real news (parentless-equivalent).
type PurchaseNotifier interface {
	NotifyPurchase(ctx context.Context, playerID int, class HullClass, shipType string, price int64, note string) error
}

// MetricsSink records the autosizer's observation series (pure observation; nil-safe).
type MetricsSink interface {
	RecordDemand(class HullClass, demand, current int)
	RecordPurchase(class HullClass)
	RecordBlocked(class HullClass, guard GuardName)
	// RecordZeroEffectAlarm fires when demand persisted but the coordinator bought nothing for
	// zero_effect_alarm_ticks consecutive ticks — a fleet-level "stuck" signal, not per-class.
	RecordZeroEffectAlarm()
}

// tickInputs are the per-tick shared reads the guard stack needs for every class.
type tickInputs struct {
	treasury   int64
	treasuryOK bool
	eraHours   float64
	eraOK      bool
	apiUtil    float64
	apiOK      bool
	totalHulls int
	totalOK    bool
}

// readTickInputs reads the shared inputs once per tick. Every read is fail-safe: a nil reader or an
// error yields readable=false, and the guards fail closed on that (API-util included).
// totalOK=false (nil/erroring fleet-size reader) blocks all buys — the ceiling cannot be judged
// without the total.
func (h *RunFleetAutosizerCoordinatorHandler) readTickInputs(ctx context.Context, playerID int) tickInputs {
	in := tickInputs{}
	if h.treasury != nil {
		if c, ok, err := h.treasury.Treasury(ctx, playerID); err == nil {
			in.treasury, in.treasuryOK = c, ok
		}
	}
	if h.era != nil {
		if hrs, ok, err := h.era.HoursToEraEnd(ctx); err == nil {
			in.eraHours, in.eraOK = hrs, ok
		}
	}
	if h.apiUtil != nil {
		if u, ok, err := h.apiUtil.UtilizationPct(ctx); err == nil {
			in.apiUtil, in.apiOK = u, ok
		}
	}
	if h.fleetSize != nil {
		if n, err := h.fleetSize.TotalHulls(ctx, playerID); err == nil {
			in.totalHulls, in.totalOK = n, true
		}
	}
	return in
}

// classGuardConfig resolves the per-class guard knobs from the run config.
func classGuardConfig(class HullClass, cfg autosizerRunConfig) (shipType string, classCeiling int, maxPrice int64, treasuryPct int) {
	switch class {
	case HullClassLight:
		// Lights are protected by the treasury-floor guard; the analyst %-affordability rule is a
		// big-ticket cap applied to heavies/warehouse, not the worker pool.
		return cfg.ShipTypeLights, cfg.FleetCeilingLights, cfg.MaxPriceLights, 0
	case HullClassHeavy:
		return cfg.ShipTypeHeavies, cfg.FleetCeilingHeavies, cfg.MaxPriceHeavies, cfg.HeavyTreasuryPctPerPurchase
	case HullClassWarehouse:
		// Warehouse buys a light frame by default (the capacity ladder); the big-ticket
		// affordability rule applies.
		return cfg.ShipTypeLights, cfg.FleetCeilingWarehouse, cfg.MaxPriceLights, cfg.HeavyTreasuryPctPerPurchase
	case HullClassExplorer:
		// The explorer's ship type (SHIP_EXPLORER), its HARD-CAP-1 class ceiling, its price
		// ceiling (~819k+premium — a REAL cap, not 0=off), and the 25% big-ticket affordability rule.
		// The realized-$/hr payback exemption is applied class-gated INSIDE EvaluateGuards, not here —
		// every knob returned here is a REAL guard bound the explorer must still clear.
		return cfg.ShipTypeExplorer, cfg.FleetCeilingExplorer, cfg.MaxPriceExplorer, cfg.ExplorerTreasuryPctPerPurchase
	case HullClassContractDelivery:
		// sp-nkqn: the routine contract-hauler class. A light frame, a conservative per-class ceiling,
		// no absolute price cap by default (the premium ceiling applies), and the 25% affordability
		// rule (RULINGS #6). NOT explorer-exempt — EvaluateGuards runs the full realized-$/hr income
		// guards on it, so a routine buy is a MEASURED-demand buy.
		return cfg.ShipTypeContractDelivery, cfg.FleetCeilingContractDelivery, cfg.MaxPriceContractDelivery, cfg.ContractDeliveryTreasuryPctPerPurchase
	default:
		return "", 0, 0, 0
	}
}

// sizeClass runs one class's demand→guard→buy for the tick. It returns whether a hull was bought
// (so the caller advances the per-tick cap and total-hull accumulator) and whether the class had
// unmet demand that did NOT result in a buy (feeding the zero-effect alarm). It never returns an
// error — a class that cannot size simply does not buy.
func (h *RunFleetAutosizerCoordinatorHandler) sizeClass(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	cfg autosizerRunConfig,
	d ClassDemand,
	in tickInputs,
	st *autosizerState,
	purchasesThisTick int,
) (bought bool, unmetNoBuy bool) {
	logger := common.LoggerFromContext(ctx)
	class := d.Class

	if h.metrics != nil {
		h.metrics.RecordDemand(class, d.Demand, d.Current)
	}

	// Fail-closed: an unreadable demand signal never buys.
	if !d.Readable {
		logger.Log("INFO", fmt.Sprintf("Autosizer %s: demand unreadable — no buy (%s)", class, d.Reason), map[string]interface{}{
			"action": "autosizer_demand_unreadable", "container_id": cmd.ContainerID, "class": string(class),
		})
		return false, false
	}

	shortfall := d.Shortfall()

	// Heavy anti-thrash streak: the unserved-lane shortfall must persist N consecutive ticks before
	// a heavy is bought. Tracked in per-container state; reset the moment the shortfall clears.
	if class == HullClassHeavy {
		if shortfall > 0 {
			st.heavyShortfallStreak++
		} else {
			st.heavyShortfallStreak = 0
		}
	}

	if shortfall <= 0 {
		return false, false
	}

	if class == HullClassHeavy && st.heavyShortfallStreak < cfg.HeavyUnservedLanesMin {
		logger.Log("INFO", fmt.Sprintf("Autosizer heavy: shortfall %d persisting %d/%d ticks — holding for the anti-thrash streak", shortfall, st.heavyShortfallStreak, cfg.HeavyUnservedLanesMin), map[string]interface{}{
			"action": "autosizer_heavy_streak", "container_id": cmd.ContainerID, "streak": st.heavyShortfallStreak, "min": cfg.HeavyUnservedLanesMin,
		})
		return false, true // unmet demand, deliberately not bought yet (streak) — counts toward the alarm
	}

	// Per-tick cap: bound total buys per tick across all classes.
	if purchasesThisTick >= cfg.PurchaseCapPerTick {
		logger.Log("INFO", fmt.Sprintf("Autosizer %s: shortfall %d but per-tick cap %d reached — deferring to next tick", class, shortfall, cfg.PurchaseCapPerTick), map[string]interface{}{
			"action": "autosizer_cap_reached", "container_id": cmd.ContainerID, "class": string(class),
		})
		return false, true
	}

	// Assemble the fully-resolved guard request.
	req, yard := h.buildPurchaseRequest(ctx, cmd, cfg, d, in, purchasesThisTick)
	decision := EvaluateGuards(req)

	logger.Log("INFO", fmt.Sprintf("Autosizer %s buy-decision (%s): %s", class, decisionWord(decision), decision.Arithmetic()), map[string]interface{}{
		"action": "autosizer_decision", "container_id": cmd.ContainerID, "class": string(class),
		"approved": decision.Approved, "blocked_by": string(decision.BlockedBy), "ship_type": req.ShipType, "price": req.Price, "yard": yard,
	})

	if !decision.Approved {
		if h.metrics != nil {
			h.metrics.RecordBlocked(class, decision.BlockedBy)
		}
		return false, true
	}

	// No-silent-dry-run: a dry run (config) or an unwired purchaser evaluates + logs the APPROVED
	// buy but spends nothing — loudly, and still counting toward the zero-effect alarm.
	if cfg.DryRun {
		logger.Log("WARN", fmt.Sprintf("Autosizer %s DRY-RUN: WOULD BUY %s @ %d at %s (set dry_run=false to arm)", class, req.ShipType, req.Price, yard), map[string]interface{}{
			"action": "autosizer_dry_run_would_buy", "container_id": cmd.ContainerID, "class": string(class), "price": req.Price,
		})
		return false, true
	}
	if h.purchaser == nil {
		logger.Log("WARN", fmt.Sprintf("Autosizer %s APPROVED but no purchaser wired — WOULD BUY %s @ %d at %s (mis-wire: the coordinator is armed but cannot spend)", class, req.ShipType, req.Price, yard), map[string]interface{}{
			"action": "autosizer_no_purchaser", "container_id": cmd.ContainerID, "class": string(class),
		})
		return false, true
	}

	res, err := h.purchaser.BuyAndDedicate(ctx, BuyOrder{PlayerID: cmd.PlayerID, Class: class, ShipType: req.ShipType, Yard: yard, ExpectedPrice: req.Price})
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Autosizer %s buy failed: %v", class, err), map[string]interface{}{
			"action": "autosizer_buy_error", "container_id": cmd.ContainerID, "class": string(class),
		})
		return false, true
	}

	if h.metrics != nil {
		h.metrics.RecordPurchase(class)
	}
	if h.notifier != nil {
		note := fmt.Sprintf("autosizer bought %s (%s) @ %d, dedicated=%v — demand %d/%d", res.ShipSymbol, req.ShipType, res.Price, res.Dedicated, d.Demand, d.Current)
		if nerr := h.notifier.NotifyPurchase(ctx, cmd.PlayerID, class, req.ShipType, res.Price, note); nerr != nil {
			logger.Log("WARN", fmt.Sprintf("Autosizer %s purchase notice failed: %v", class, nerr), nil)
		}
	}
	logger.Log("INFO", fmt.Sprintf("Autosizer %s BOUGHT %s @ %d at %s, dedicated=%v (demand %d, current %d)", class, res.ShipSymbol, res.Price, yard, res.Dedicated, d.Demand, d.Current), map[string]interface{}{
		"action": "autosizer_bought", "container_id": cmd.ContainerID, "class": string(class),
		"ship_symbol": res.ShipSymbol, "price": res.Price, "dedicated": res.Dedicated,
	})
	return true, false
}

// buildPurchaseRequest resolves a class's candidate purchase from the demand, the run config, and
// the tick's shared reads. The realized-rate floor is a fraction (heavy_marginal_rate_floor) of the
// class's fleet-average realized rate — buy only while the marginal hull clears that fraction.
func (h *RunFleetAutosizerCoordinatorHandler) buildPurchaseRequest(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	cfg autosizerRunConfig,
	d ClassDemand,
	in tickInputs,
	purchasesThisTick int,
) (PurchaseRequest, string) {
	class := d.Class
	shipType, classCeiling, maxPrice, treasuryPct := classGuardConfig(class, cfg)

	price, cheapest, yard, priceOK := int64(0), int64(0), "", false
	if h.yardPrice != nil {
		if p, c, y, ok, err := h.yardPrice.PriceFor(ctx, cmd.PlayerID, class, shipType, cfg.PreferDemandProximalYard); err == nil {
			price, cheapest, yard, priceOK = p, c, y, ok
		}
	}

	rateFloor := cfg.HeavyMarginalRateFloor * d.FleetAvgRate

	return PurchaseRequest{
		Class:    class,
		ShipType: shipType,

		Shortfall: d.Shortfall(),

		CurrentClassCount: d.Current,
		ClassCeiling:      classCeiling,
		CurrentTotalCount: in.totalHulls,
		TotalCeiling:      cfg.FleetCeilingTotal,

		PurchasesThisTick: purchasesThisTick,
		PerTickCap:        cfg.PurchaseCapPerTick,

		Price:              price,
		PriceReadable:      priceOK && in.totalOK, // total unreadable also blocks (ceiling unjudgeable)
		CheapestKnownPrice: cheapest,
		MaxPriceClass:      maxPrice,
		MaxPremiumPct:      cfg.MaxPremiumOverCheapestPct,

		HoursToEraEnd:  in.eraHours,
		EraReadable:    in.eraOK,
		EraCutoffHours: cfg.PurchaseCutoffAtEraMinus.Hours(),
		PaybackSafety:  cfg.PaybackSafetyFactor,

		MarginalRate:        d.MarginalRate,
		RateFloor:           rateFloor,
		RateReadable:        d.RateReadable,
		RateDeclining:       d.RateDeclining,
		UnservedDemandFloor: cfg.DecliningRateUnservedFloor,

		LiveTreasury:      in.treasury,
		TreasuryReadable:  in.treasuryOK,
		ReserveAbsolute:   cfg.Reserve,
		ReservePct:        cfg.ReserveTreasuryPct,
		MarginOverFloor:   cfg.PurchaseMarginOverFloor,
		TreasuryPctPerBuy: treasuryPct,

		APIUtilPct:      in.apiUtil,
		APIUtilReadable: in.apiOK,
		APIUtilCeiling:  cfg.APIUtilizationCeilingPct,
	}, yard
}

func decisionWord(d PurchaseDecision) string {
	if d.Approved {
		return "APPROVED"
	}
	return "BLOCKED by " + string(d.BlockedBy)
}
