package commands

import (
	"context"
	"fmt"
	"strconv"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
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

// HeavyYardReader reports the heavy yard the purchase path would TARGET this tick, and its ask.
//
// It is deliberately NOT "the cheapest known heavy price" any more. The buy targets the
// NEAREST reachable yard, so reserving the cheapest ask on the map under-reserves whenever the two
// differ: treasury tops out below what the nearer yard asks and the purchase never clears. The
// reservation must track the yard we will actually buy at.
//
// The same read backs the sensing buy-floor, through ONE shared implementation, so the two
// spenders can never disagree about which yard they are saving toward.
type HeavyYardReader interface {
	HeavyTarget(ctx context.Context, playerID int) (HeavyTargetYard, error)
}

// HeavyTargetYard is the heavy purchase target as the reservation sees it.
//
// CapabilityOpen and Priced are SEPARATE on purpose. CapabilityOpen is availability — a known yard
// sells a heavy hull type, PRICED OR NOT — because a shipyard prices its hulls only while a ship
// stands there, so a yard discovered without presence is known and unpriceable at the same time.
// Priced is whether a money guard can act on it. The gap between them is exactly the state the
// heavy-yard pricing errand resolves.
type HeavyTargetYard struct {
	CapabilityOpen bool
	Priced         bool
	WaypointSymbol string
	PurchasePrice  int64
}

// APIUtilizationReader reads the sustained request-utilization percent. readable=false ⇒ the
// API-util guard fails CLOSED: an unreadable/absent utilization surface holds concurrency growth.
type APIUtilizationReader interface {
	UtilizationPct(ctx context.Context) (pct float64, readable bool, err error)
}

// The buy port's shapes are shared with the contract scaler and live in domain/hullbuy; these
// aliases keep the coordinator's internals reading as one package (see hull_classes.go).
type (
	YardPriceReader = hullbuy.YardPriceReader
	BuyOrder        = hullbuy.BuyOrder
	BuyResult       = hullbuy.BuyResult
)

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
	// RecordHeavyReserve reports the per-tick heavy-trade facts: the credits actually WITHHELD,
	// the ask being saved TOWARD, the tag-independent owned-heavy census, and the cap in force.
	// Emitted EVERY tick, whatever happens — a reserve recorded only when something changes
	// cannot answer "is the fleet saving for a heavy, or stuck?".
	//
	// RESERVE AND TARGET ARE BOTH REQUIRED, and the gap between them is the point (sp-zg71k).
	// Since the hold became treasury-bounded, reserve=0 has TWO meanings — "nothing to save for"
	// and "saving toward an ask this era is nowhere near" — and target is the only thing that
	// tells them apart. Publishing the hold alone would make the bound invisible in every gauge.
	RecordHeavyReserve(playerID string, reserve, target int64, owned, cap int)
	// ObserveHeavyPricePremium reports what one heavy purchase paid above the cheapest KNOWN
	// yard ask, in percent — the measured cost of buying at the cheapest yard WITH PRESENCE.
	ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64)
	// RecordSizingEnabled publishes the sizing_enabled master switch as read THIS TICK. Emitted
	// every tick on both the active and the paused path, because a paused coordinator that stopped
	// emitting would be indistinguishable from a dead one — the operator needs a series that says
	// "deliberately off", not a gap.
	RecordSizingEnabled(playerID string, enabled bool)
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
	// heavyTarget is the ask the fleet is SAVING TOWARD for the next heavy, derived ONCE per
	// tick by common.HeavyReserve. It is NOT a credit count any guard may subtract — see
	// common.HeavyReserveTarget — and it is carried here only so the tick can publish what it
	// is saving toward beside what it actually held.
	heavyTarget common.HeavyReserveTarget
	// heavyReserve is the CREDITS ACTUALLY WITHHELD toward heavyTarget at this tick's treasury,
	// resolved ONCE by HeavyReserveTarget.HoldAt so every class in the tick judges the same
	// number. Bounded by the treasury (sp-zg71k): an ask the fleet is nowhere near reaching
	// withholds nothing rather than pushing every non-heavy class's spendable balance negative.
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
	// The hold is resolved AFTER the treasury read, because it is a judgement about the balance
	// we actually have rather than about the yard (sp-zg71k). An UNREADABLE treasury withholds
	// NOTHING — the same direction every other blind input takes here, and it authorises nothing:
	// guardAffordability refuses every buy outright on an unreadable balance (RULINGS #4), so the
	// released reserve reaches no spender.
	if in.treasuryOK {
		in.heavyReserve = in.heavyTarget.HoldAt(in.treasury)
	}
	if h.apiUtil != nil {
		if u, ok, err := h.apiUtil.UtilizationPct(ctx); err == nil {
			in.apiUtil, in.apiOK = u, ok
		}
	}
	return in
}

// classGuardConfig resolves the per-class guard knobs from the run config. There is no class
// ceiling here: a class is bounded by its demand, its affordability and its price cap.
func classGuardConfig(class HullClass, cfg autosizerRunConfig) (shipType string, maxPrice int64, treasuryPct int) {
	switch class {
	case HullClassLight:
		// Lights are protected by the treasury-floor guard; the percentage ceiling is a big-ticket
		// dial, not a worker-pool one.
		return cfg.ShipTypeLights, cfg.MaxPriceLights, 0
	case HullClassHeavy:
		return cfg.ShipTypeHeavies, cfg.MaxPriceHeavies, cfg.HeavyTreasuryPctPerPurchase
	default:
		// HullClassContractDelivery has no guard config here — the dedicated contract scaler owns
		// that capacity. An unhandled class yields no buy config.
		return "", 0, 0
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
	shipType, maxPrice, treasuryPct := classGuardConfig(class, cfg)

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

		// The heavy-hull cap — the only count-based bound on any class. guardHeavyCap is
		// heavy-scoped and passes for every other class.
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
// TRADE-SCOPED, DELIBERATELY. Only HullClassHeavy substitutes: it is the only pool whose
// preferred hull no reachable yard sells, which is the stall this exists to clear. Widening
// it would let a pool quietly stop being made of the hull the operator configured.
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
