package commands

import (
	"context"
	"fmt"
	"strconv"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// The ACT step: the coordinator reads the tick's shared inputs (treasury, era clock,
// API utilization, owned-heavy census) once, then for each class with unmet demand assembles a fully
// -resolved PurchaseRequest, runs it through the fail-closed guard stack, and on approval buys
// ONE hull and dedicates it to its class fleet IN THE SAME BREATH (dedicate-at-purchase). Every
// decision logs its full arithmetic (the park-line idiom), so the captain retunes the
// blocking knob from evidence. Purchases are bounded per tick by
// purchase_cap_per_tick; the heavy class additionally requires its unserved-lane shortfall to
// persist heavy_unserved_lanes_min consecutive ticks (anti-thrash). A tick that has demand but buys
// nothing for zero_effect_alarm_ticks consecutive passes raises ONE edge-triggered alarm — a buyer
// that never buys must say so.

// --- buy-path ports (wired by setters at boot; every one nil-safe, fail-closed on unread) ---

// TreasuryReader reads the player's live credit balance. readable=false ⇒ the treasury guards fail
// closed (a buy must never proceed on an unknown balance).
type TreasuryReader interface {
	Treasury(ctx context.Context, playerID int) (credits int64, readable bool, err error)
}

// HeavyCensusReader counts the player's owned HEAVY HULLS — every one of them, regardless of
// which fleet they are tagged to. This is deliberately NOT the autosizer's trade-pool count
// (DedicatedFleet=="trade"): that one caps pool size, this one bounds capital exposure in large
// hulls, and a tag-scoped count would make a heavy tagged elsewhere invisible and authorise
// re-buying a hull we already own. An error ⇒ the heavy cap guard fails CLOSED.
type HeavyCensusReader interface {
	HeaviesOwned(ctx context.Context, playerID int) (int, error)
}

// HeavyYardReader reports the cheapest KNOWN, PRICED heavy yard ask. found=false means no known
// yard sells a heavy at a usable price — the capability is CLOSED and nothing is reserved.
type HeavyYardReader interface {
	CheapestHeavyPrice(ctx context.Context, playerID int) (price int64, found bool, err error)
}

// APIUtilizationReader reads the sustained request-utilization percent. readable=false ⇒ the
// API-util guard fails CLOSED: an unreadable/absent utilization surface holds concurrency growth.
type APIUtilizationReader interface {
	UtilizationPct(ctx context.Context) (pct float64, readable bool, err error)
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
	// RecordHeavyReserve reports the per-tick heavy-trade facts (sp-fwk8z): the derived
	// reservation, the tag-independent owned-heavy census, and the cap in force. Emitted
	// EVERY tick, whatever happens — a reserve recorded only when something changes cannot
	// answer "is the fleet saving for a heavy, or stuck?".
	RecordHeavyReserve(playerID string, reserve int64, owned, cap int)
	// ObserveHeavyPricePremium reports what one heavy purchase paid above the cheapest KNOWN
	// yard ask, in percent — the measured cost of buying at the cheapest yard WITH PRESENCE.
	ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64)
}

// tickInputs are the per-tick shared reads the guard stack needs for every class.
type tickInputs struct {
	treasury   int64
	treasuryOK bool
	apiUtil    float64
	apiOK      bool
	// heaviesOwned is the BROAD, tag-independent heavy-hull census (frame list primary,
	// cargo-capacity safety net). heaviesOwnedOK=false ⇒ the heavy cap guard fails CLOSED.
	heaviesOwned   int
	heaviesOwnedOK bool
	// heavyReserve is the derived hold-back for the NEXT heavy, computed ONCE per tick by
	// common.HeavyReserve so every class in the tick judges the same number.
	heavyReserve int64
}

// readTickInputs reads the shared inputs once per tick. Every read is fail-safe: a nil reader or an
// error yields readable=false, and the guards fail closed on that (API-util included).
func (h *RunFleetAutosizerCoordinatorHandler) readTickInputs(ctx context.Context, playerID int, cfg autosizerRunConfig) tickInputs {
	in := tickInputs{}
	if h.heavyCensus != nil {
		if n, err := h.heavyCensus.HeaviesOwned(ctx, playerID); err == nil {
			in.heaviesOwned, in.heaviesOwnedOK = n, true
		}
	}
	// The reservation is DERIVED once per tick from durable facts, never stored, so every
	// class in this tick judges the SAME number. common.HeavyReserve is the ONE definition —
	// the arithmetic is never re-derived here (spec §3: a second copy is how a reservation
	// silently drifts).
	if h.heavyYard != nil && in.heaviesOwnedOK {
		if price, found, err := h.heavyYard.CheapestHeavyPrice(ctx, playerID); err == nil {
			in.heavyReserve = common.HeavyReserve(common.HeavyReserveInputs{
				CapabilityOpen:     found,
				HeaviesOwned:       in.heaviesOwned,
				HeavyCap:           cfg.HeavyCap,
				CheapestKnownPrice: price,
			})
		}
	}
	if h.treasury != nil {
		if c, ok, err := h.treasury.Treasury(ctx, playerID); err == nil {
			in.treasury, in.treasuryOK = c, ok
		}
	}
	if h.apiUtil != nil {
		if u, ok, err := h.apiUtil.UtilizationPct(ctx); err == nil {
			in.apiUtil, in.apiOK = u, ok
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
	case HullClassExplorer:
		// The explorer's ship type (SHIP_EXPLORER), its HARD-CAP-1 class ceiling, its price
		// ceiling (~819k+premium — a REAL cap, not 0=off), and the 25% big-ticket affordability rule.
		// The realized-$/hr payback exemption is applied class-gated INSIDE EvaluateGuards, not here —
		// every knob returned here is a REAL guard bound the explorer must still clear.
		return cfg.ShipTypeExplorer, cfg.FleetCeilingExplorer, cfg.MaxPriceExplorer, cfg.ExplorerTreasuryPctPerPurchase
	default:
		// sp-y2ptq: HullClassContractDelivery's guard config was removed with the autosizer's contract
		// class (the dedicated scaler owns it). An unhandled class yields no buy config.
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
	// a heavy is bought. Tracked in per-container state; reset the moment the shortfall clears. The
	// count is advanced HERE (where the tick state lives) and JUDGED by guardDemand.
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

	// Per-tick cap: bound total buys per tick across all classes.
	if purchasesThisTick >= cfg.PurchaseCapPerTick {
		logger.Log("INFO", fmt.Sprintf("Autosizer %s: shortfall %d but per-tick cap %d reached — deferring to next tick", class, shortfall, cfg.PurchaseCapPerTick), map[string]interface{}{
			"action": "autosizer_cap_reached", "container_id": cmd.ContainerID, "class": string(class),
		})
		return false, true
	}

	// Assemble the fully-resolved guard request. The anti-thrash streak rides IN it (guardDemand
	// judges it) rather than short-circuiting above, so a streak hold now appears in the decision
	// line alongside everything else instead of on a separate log line.
	req, yard := h.buildPurchaseRequest(ctx, cmd, cfg, d, in, st, purchasesThisTick)
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

	// An unwired purchaser evaluates + logs the APPROVED buy but spends nothing — loudly, and
	// still counting toward the zero-effect alarm.
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
		if class == HullClassHeavy {
			// Bind on the ACTUAL price paid, not the quote: the premium is only honest if it
			// measures what left the treasury.
			h.metrics.ObserveHeavyPricePremium(strconv.Itoa(cmd.PlayerID), res.Price, req.CheapestKnownPrice)
		}
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
// the tick's shared reads.
func (h *RunFleetAutosizerCoordinatorHandler) buildPurchaseRequest(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	cfg autosizerRunConfig,
	d ClassDemand,
	in tickInputs,
	st *autosizerState,
	purchasesThisTick int,
) (PurchaseRequest, string) {
	class := d.Class
	shipType, classCeiling, maxPrice, treasuryPct := classGuardConfig(class, cfg)

	shipType, price, cheapest, yard, priceOK := h.resolveHullPrice(ctx, cmd, cfg, class, shipType)

	// The anti-thrash streak applies to the HEAVY class only: its Shortfall is the unserved
	// profitable-lane count, which spikes transiently as the solver re-ranks. Every other class
	// passes streakMin=0, which makes the streak term a no-op in guardDemand.
	streak, streakMin := 0, 0
	if class == HullClassHeavy {
		streak, streakMin = st.heavyShortfallStreak, cfg.HeavyUnservedLanesMin
	}

	return PurchaseRequest{
		Class:    class,
		ShipType: shipType,

		Shortfall:          d.Shortfall(),
		ShortfallStreak:    streak,
		ShortfallStreakMin: streakMin,

		CurrentClassCount: d.Current,
		ClassCeiling:      classCeiling,

		// The heavy-hull cap: a SEPARATE bound from ClassCeiling above (which counts the
		// trade pool by tag). Both are judged; guardHeavyCap is heavy-scoped and passes
		// for every other class.
		HeaviesOwned:         in.heaviesOwned,
		HeavyCap:             cfg.HeavyCap,
		HeaviesOwnedReadable: in.heaviesOwnedOK,

		// The derived hold-back for the NEXT heavy. guardTreasuryFloor waives it for the
		// heavy purchase itself (it would otherwise reserve against itself).
		HeavyReserve: in.heavyReserve,

		PurchasesThisTick: purchasesThisTick,
		PerTickCap:        cfg.PurchaseCapPerTick,

		Price:              price,
		PriceReadable:      priceOK,
		CheapestKnownPrice: cheapest,
		MaxPriceClass:      maxPrice,
		MaxPremiumPct:      cfg.MaxPremiumOverCheapestPct,

		LiveTreasury:      in.treasury,
		TreasuryReadable:  in.treasuryOK,
		MarginOverFloor:   cfg.PurchaseMarginOverFloor,
		TreasuryPctPerBuy: treasuryPct,

		APIUtilPct:      in.apiUtil,
		APIUtilReadable: in.apiOK,
		APIUtilCeiling:  cfg.APIUtilizationCeilingPct,
	}, yard
}

// resolveHullPrice prices the class's PREFERRED hull and, for the TRADE pool only,
// falls back to the best priceable trade-capable hull when the preferred one cannot be
// priced at any reachable yard. It returns the type actually resolved, so the guard
// stack, the buy order and the decision log all name the same hull.
//
// WHY THIS EXISTS. The trade pool buys autosizer_ship_type_heavies, which defaults to
// SHIP_HEAVY_FREIGHTER — and no shipyard discovered this era sells one. The price guard
// therefore blocked every tick while profitable lanes sat unflown, and the pool refused
// to buy the very hull it is already made of (its own hulls are light freighters).
// Falling back to a priceable trade-capable hull is what lets the demand be served at
// all; the preferred type stays the operator's choice and simply wins whenever it can
// be priced.
//
// IT CHANGES ONLY WHICH HULL IS OFFERED TO THE GUARDS — never whether a guard runs.
// The caller passes the resolved type and its OWN price and cheapest-known ask into the
// same PurchaseRequest, so the price ceiling compares like with like (a premium check
// against the preferred type's cheapest ask would be meaningless for a different hull),
// and every other guard — treasury floor and percentage, heavy cap, class ceiling,
// per-tick cap — judges the substitute exactly as it judges the preferred hull. If they then refuse the cheaper hull on economics, that refusal
// stands: this function has no power to approve anything.
//
// TRADE-SCOPED, DELIBERATELY. The explorer buys REACH — a freighter cannot warp off the
// gate network, so substituting one would silently defeat the class — and the light
// worker pool's own type is priceable. Only HullClassHeavy substitutes.
//
// SELF-CORRECTING: the preferred type is asked FIRST every tick, and
// shipyard.TradeHullPreferenceOrder lists the heavy classes ahead of the light one, so
// the moment exploration finds a heavy yard the preferred hull wins back with no
// intervention. Nothing is remembered between ticks.
//
// FAILS CLOSED: when no trade-capable type can be priced, readable stays false and the
// price guards block exactly as they do today. The substitution is logged once per
// decision, naming the preferred type and what replaced it, because an operator must
// never have to discover a changed hull type from a ship list.
func (h *RunFleetAutosizerCoordinatorHandler) resolveHullPrice(
	ctx context.Context,
	cmd *RunFleetAutosizerCoordinatorCommand,
	cfg autosizerRunConfig,
	class HullClass,
	preferred string,
) (shipType string, price, cheapest int64, yard string, readable bool) {
	if h.yardPrice == nil {
		return preferred, 0, 0, "", false
	}
	if p, c, y, ok, err := h.yardPrice.PriceFor(ctx, cmd.PlayerID, class, preferred, cfg.PreferDemandProximalYard); err == nil && ok {
		return preferred, p, c, y, true
	}
	if class != HullClassHeavy {
		return preferred, 0, 0, "", false
	}
	for _, alt := range shipyard.TradeHullPreferenceOrder {
		if alt == preferred {
			continue // already asked, and it could not be priced
		}
		p, c, y, ok, err := h.yardPrice.PriceFor(ctx, cmd.PlayerID, class, alt, cfg.PreferDemandProximalYard)
		if err != nil || !ok {
			continue
		}
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
			"Autosizer trade pool: preferred hull %s cannot be priced at any reachable yard — substituting %s @ %d at %s for this decision (the preferred type wins back automatically once a yard selling it is found)",
			preferred, alt, p, y), map[string]interface{}{
			"action": "autosizer_trade_hull_substituted", "container_id": cmd.ContainerID,
			"class": string(class), "preferred_ship_type": preferred, "ship_type": alt,
			"price": p, "yard": y,
		})
		return alt, p, c, y, true
	}
	return preferred, 0, 0, "", false
}

func decisionWord(d PurchaseDecision) string {
	if d.Approved {
		return "APPROVED"
	}
	return "BLOCKED by " + string(d.BlockedBy)
}
