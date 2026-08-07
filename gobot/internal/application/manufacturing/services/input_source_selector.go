package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// The runtime input-buy path selects its source SUPPLY-FIRST (MODERATE+ only, ranked
// supply > activity > price), not price-first.
//
// SUPPLY IS THE LEADING INDICATOR AND PRICE IS THE LAGGING ONE. A depleted source ladders
// immediately under our own draw, so by the time the ask has climbed the money is already spent;
// every large input blowup starts at a SCARCE/LIMITED source, never at an ABUNDANT one. Never
// picking a depleted source on the normal path means the ladder a price ceiling can only chase
// cannot begin.

const (
	// defaultRescueMultiplier caps the rescue clause: when NO eligible (MODERATE+) source exists
	// and the chain is blocked on this input, a SCARCE/LIMITED source is bought ONLY if its ask is
	// within this multiple of the good's trailing median. Deliberately TIGHTER than the ladder
	// ceiling, because a rescue buy is already into a depleted market — accept it barely above
	// baseline, never a ladder. A 0/absent config resolves here (RULINGS #5).
	defaultRescueMultiplier = 1.2
)

// inputSourcingCtxKey carries the per-run sourcing config from the factory coordinator down to
// buyGood. Rides on ctx for the same singleton-executor race reason as the ceiling/reserve
// configs.
type inputSourcingCtxKey struct{}

type inputSourcingConfig struct {
	rescueMultiplier float64
	eraEndPriceFirst bool
	disabled         bool
}

// WithInputSourcing stamps the per-run supply-first sourcing config onto ctx (sp-a5j7 Phase 2).
// rescueMultiplier 0 resolves to defaultRescueMultiplier; eraEndPriceFirst flips to price-first
// (< T-6h, the era-end exception the daemon toggles at the stocker-rundown boundary);
// disabled=true is the RULINGS #5 escape hatch back to pure price-first sourcing. A command
// built directly (tests) that never stamps this leaves supply-first ON at the default rescue cap.
func WithInputSourcing(ctx context.Context, rescueMultiplier float64, eraEndPriceFirst, disabled bool) context.Context {
	return context.WithValue(ctx, inputSourcingCtxKey{}, inputSourcingConfig{
		rescueMultiplier: rescueMultiplier,
		eraEndPriceFirst: eraEndPriceFirst,
		disabled:         disabled,
	})
}

func inputSourcingConfigFromContext(ctx context.Context) inputSourcingConfig {
	if v, ok := ctx.Value(inputSourcingCtxKey{}).(inputSourcingConfig); ok {
		return v
	}
	return inputSourcingConfig{}
}

// UNIFIED GATE-FILL MODE. When a construction deliver-to-gate run is buying a node, standing order
// authorises MARGIN-BLIND gating: the gate is a finite, affordable, one-off investment, so
// per-material margin/price gating freezes the unlock to save pennies. Under gate mode the
// executor's gates relax to SOLVENCY ONLY:
//   - the input-source supply FLOOR drops to SCARCE, because a MODERATE floor permanently freezes
//     deep chains that never regenerate to MODERATE under continuous buy, with buy-vs-feed decided
//     by ACTIVITY rather than supply alone (selectInputSource);
//   - the per-tranche price ceiling and the chain-margin park are EXEMPTED (input_price_ceiling.go).
//
// A node is in gate mode iff IsUnifiedGateNode(ctx) — the single predicate, in unified_gate_fill.go
// — meaning the run delivers to a construction site. It rides ctx rather than a struct field for
// the same singleton-executor race reason as the sibling sourcing/price-ceiling/reserve configs:
// ProductionExecutor is a boot singleton shared across every concurrent factory container, so a
// struct field would race between a gate run and a profit factory. Unstamped is false.

// inputSourceMode is how selectInputSource chose the returned source — buyGood uses it to
// decide which downstream guards still apply (the eligible path faces the cross-market ceiling;
// the rescue/era-end paths were already price-validated by the selector).
type inputSourceMode int

const (
	sourceModeNone             inputSourceMode = iota // no usable source — PARK
	sourceModeEligible                                // supply-first MODERATE+ pick (normal path)
	sourceModeRescue                                  // no eligible source; validated SCARCE/LIMITED/EXCHANGE buy
	sourceModeEraEndPriceFirst                        // < T-6h era-end exception: price-first
	sourceModePriceFirstOff                           // supply-first disabled (RULINGS #5 escape hatch)
	sourceModeGateFill                                // sp-vh1s: sub-MODERATE PRODUCING buy under a lowered floor (gate run / per-good MinSupply override) — margin-blind, solvency-bounded
)

func (m inputSourceMode) String() string {
	switch m {
	case sourceModeEligible:
		return "eligible_supply_first"
	case sourceModeRescue:
		return "rescue"
	case sourceModeEraEndPriceFirst:
		return "era_end_price_first"
	case sourceModePriceFirstOff:
		return "supply_first_disabled"
	case sourceModeGateFill:
		return "gate_fill"
	default:
		return "none"
	}
}

// selectInputSource picks the buy source for an input good SUPPLY-FIRST (sp-a5j7 Phase 2),
// restoring the original design's intent that the runtime path bypassed. Return contract:
//   - (source, sourceModeEligible, nil): a MODERATE+ EXPORT source, supply>activity>price ranked.
//   - (source, sourceModeRescue, nil): no eligible source; the cheapest fallback (SCARCE/LIMITED
//     export or EXCHANGE) whose ask is within the rescue cap (rescueMultiplier x trailing median).
//   - (source, sourceModeEraEndPriceFirst/PriceFirstOff, nil): price-first exception paths.
//   - (nil, sourceModeNone, nil): no eligible source AND the fallback is over-cap or unpriceable —
//     PARK (a graceful, zero-spend refusal, not an error).
//   - (nil, sourceModeNone, err): the locator itself failed (surfaced to the caller).
//
// The rescue clause is fail-CLOSED: with no trailing median to validate against (no
// price-history reader, or no samples) a depleted-market buy is refused, never blind-bought.
func (e *ProductionExecutor) selectInputSource(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, inputSourceMode, error) {
	logger := common.LoggerFromContext(ctx)
	cfg := inputSourcingConfigFromContext(ctx)

	// RULINGS #5 escape hatch: supply-first disabled → pure price-first (pre-restoration behavior).
	if cfg.disabled {
		r, err := e.marketLocator.FindExportMarket(ctx, good, systemSymbol, playerID)
		if err != nil {
			return nil, sourceModeNone, err
		}
		return r, sourceModePriceFirstOff, nil
	}

	// ERA-END (< T-6h): mean-reversion has no time to work, so a cheap ask that clears margin NOW
	// beats waiting. Daemon-toggled at the same boundary as the stocker rundown.
	if cfg.eraEndPriceFirst {
		r, err := e.marketLocator.FindExportMarket(ctx, good, systemSymbol, playerID)
		if err != nil {
			return nil, sourceModeNone, err
		}
		logger.Log("INFO", fmt.Sprintf(
			"Era-end mode: sourcing %s price-first at %s (ask %d, supply %s) — supply-first suspended < T-6h (sp-a5j7)",
			good, r.WaypointSymbol, r.Price, r.Supply,
		), map[string]interface{}{
			"good": good, "market": r.WaypointSymbol, "ask": r.Price, "mode": "era_end_price_first",
		})
		return r, sourceModeEraEndPriceFirst, nil
	}

	// PRIMARY: supply-first eligible EXPORT (MODERATE+), ranked supply > activity > price — the
	// restored original design (FindExportMarketBySupplyPriority).
	eligible, err := e.marketLocator.FindExportMarketBySupplyPriority(ctx, good, systemSymbol, playerID)
	if err == nil && eligible != nil {
		return eligible, sourceModeEligible, nil
	}

	// No eligible MODERATE+ EXPORT source. Fetch the cheapest available EXPORT/EXCHANGE source
	// once — shared by the sp-vh1s sub-MODERATE activity-routing branch and the classic sp-a5j7
	// rescue path below.
	fallback, ferr := e.marketLocator.FindExportMarket(ctx, good, systemSymbol, playerID)
	if ferr != nil || fallback == nil {
		return nil, sourceModeNone, ferr // genuinely unsourceable in-system
	}

	// sp-vh1s sub-MODERATE routing. When the per-node supply floor has been LOWERED below MODERATE
	// — gate mode (SCARCE floor, Admiral §9) or an explicit per-good MinSupply override — a
	// SCARCE/LIMITED source may be a legitimate buy, decided by ACTIVITY not supply alone: feeding
	// raises a factory's output ACTIVITY, not its SUPPLY (verified era-2/3), so a source at/above
	// the lowered floor that is STILL PRODUCING is bought while a RESTRICTED (dead) one is left for
	// the tree to FEED/recurse. The default MODERATE floor (every non-gate/non-override caller)
	// returns routed=false and falls through to the byte-identical classic rescue below.
	if src, mode, routed := e.routeSubModerateSource(ctx, good, fallback); routed {
		return src, mode, nil
	}

	return e.rescueSource(ctx, good, fallback, cfg.rescueMultiplier)
}

// rescueSource admits the cheapest remaining source ONCE, and only within the rescue cap: reaching
// here means the chain is blocked on an input with no healthy source, but laddering a depleted
// market is worse than waiting. Fails CLOSED — with no trailing median to validate against, the
// buy is refused rather than made blind.
func (e *ProductionExecutor) rescueSource(ctx context.Context, good string, fallback *MarketLocatorResult, rescueMult float64) (*MarketLocatorResult, inputSourceMode, error) {
	logger := common.LoggerFromContext(ctx)
	if rescueMult <= 0 {
		rescueMult = defaultRescueMultiplier
	}
	median, ok := e.trailingMedianAsk(ctx, fallback.WaypointSymbol, good)
	if !ok {
		logger.Log("WARNING", fmt.Sprintf(
			"No eligible (MODERATE+) source for %s and no trailing median at %s to validate a rescue buy — parking (fail-closed, sp-a5j7)",
			good, fallback.WaypointSymbol,
		), map[string]interface{}{
			"good": good, "market": fallback.WaypointSymbol,
			"action": "factory_parked", "reason": "rescue_no_median",
		})
		return nil, sourceModeNone, nil
	}
	capPrice := int(float64(median) * rescueMult)
	if fallback.Price > capPrice {
		logger.Log("INFO", fmt.Sprintf(
			"No eligible (MODERATE+) source for %s; rescue REFUSED at %s — ask %d exceeds rescue cap %d (%.2fx trailing median %d): refusing to ladder a depleted market (sp-a5j7)",
			good, fallback.WaypointSymbol, fallback.Price, capPrice, rescueMult, median,
		), map[string]interface{}{
			"good": good, "market": fallback.WaypointSymbol, "ask": fallback.Price, "cap": capPrice, "median": median,
			"action": "factory_parked", "reason": "rescue_over_cap",
		})
		return nil, sourceModeNone, nil
	}
	logger.Log("WARNING", fmt.Sprintf(
		"No eligible (MODERATE+) source for %s; RESCUE buy at %s — ask %d within cap %d (%.2fx trailing median %d), supply %s: chain blocked, sourcing a depleted market once (sp-a5j7)",
		good, fallback.WaypointSymbol, fallback.Price, capPrice, rescueMult, median, fallback.Supply,
	), map[string]interface{}{
		"good": good, "market": fallback.WaypointSymbol, "ask": fallback.Price, "cap": capPrice, "median": median, "mode": "rescue",
	})
	return fallback, sourceModeRescue, nil
}

// routeSubModerateSource applies the sp-vh1s sub-MODERATE floor + ACTIVITY routing to an already
// fetched fallback source. routed=false when the per-node floor is still MODERATE (the default,
// every non-gate/non-override caller) or the source sits below the lowered floor — the caller then
// falls through to the byte-identical classic sp-a5j7 rescue path. routed=true has decided:
//   - (fallback, sourceModeGateFill): a PRODUCING source (activity != RESTRICTED) at/above the
//     lowered floor — buy it margin-blind. This mode is not sourceModeEligible, so the per-tranche
//     cross-market price ceiling (buyGood) does not gate it; the 9aoc solvency floor still does.
//   - (nil, sourceModeNone): a RESTRICTED (dead — nothing feeding it) source at/above the floor —
//     PARK so the tree FEEDS it (recurse), rather than buying a factory that is not producing.
//
// The ACTIVITY split is the analyst's verified refinement: feeding raises a factory's output
// ACTIVITY, not its SUPPLY, so supply alone conflates "SCARCE but producing" (buy) with "SCARCE and
// dead" (feed) and drives needless deeper recursion. RESTRICTED is the sole dead signal; WEAK /
// GROWING / STRONG and an absent activity (EXCHANGE raws) are all producing → buyable.
func (e *ProductionExecutor) routeSubModerateSource(ctx context.Context, good string, fallback *MarketLocatorResult) (*MarketLocatorResult, inputSourceMode, bool) {
	floor := e.perNodeSupplyFloor(ctx, good)
	if floor.Order() >= manufacturing.SupplyLevelModerate.Order() {
		return nil, sourceModeNone, false // default/raised floor — the classic paths own it
	}
	if manufacturing.ParseSupplyLevel(fallback.Supply).Order() < floor.Order() {
		return nil, sourceModeNone, false // below even the lowered floor — classic rescue (cap-gated)
	}

	logger := common.LoggerFromContext(ctx)
	if fallback.Activity == activityRestricted {
		logger.Log("INFO", fmt.Sprintf(
			"Gate-fill routing: %s source at %s is below the MODERATE floor and RESTRICTED (dead — nothing feeding it); FEEDING it instead (park this buy, recurse), not buying a non-producing factory (sp-vh1s)",
			good, fallback.WaypointSymbol,
		), map[string]interface{}{
			"good": good, "market": fallback.WaypointSymbol, "supply": fallback.Supply, "activity": fallback.Activity,
			"action": "factory_parked", "reason": "gate_feed_dead_source",
		})
		return nil, sourceModeNone, true
	}
	logger.Log("INFO", fmt.Sprintf(
		"Gate-fill routing: buying %s at %s (supply %s, activity %s) below the MODERATE floor — margin-blind, bounded on solvency, not per-material margin (sp-vh1s)",
		good, fallback.WaypointSymbol, fallback.Supply, fallback.Activity,
	), map[string]interface{}{
		"good": good, "market": fallback.WaypointSymbol, "supply": fallback.Supply, "activity": fallback.Activity,
		"ask": fallback.Price, "mode": "gate_fill",
	})
	return fallback, sourceModeGateFill, true
}

// perNodeSupplyFloor resolves the EXPORT sourcing floor for THIS node's good (sp-vh1s).
//
// OFF gate mode (a resale-sink / profit-factory run) it returns MODERATE unconditionally: the
// per-good MinSupply override (which already threads to the selector via the coordinators'
// WithGoodGatingOverrides) MUST NOT change runtime sourcing outside a gate run. The override still
// drives the construction planner / task-activator floor as before (those consumers are unchanged);
// factory-mode runtime gating is a DEFERRED follow-up (Admiral §9), so it deliberately does nothing
// here off-gate.
//
// IN gate mode the floor drops to SCARCE (Admiral §9 — a MODERATE floor permanently freezes deep
// chains like SILICON/ELECTRONICS that never regenerate to MODERATE under continuous buy), then the
// per-good sp-sdyo MinSupply override wins for this good, so a COPPER override gates the COPPER
// node's buy inside an ADV tree independently of every other node (it can, e.g., raise COPPER back
// to MODERATE as a pacing knob without touching its siblings).
func (e *ProductionExecutor) perNodeSupplyFloor(ctx context.Context, good string) manufacturing.SupplyLevel {
	if !IsUnifiedGateNode(ctx) {
		return manufacturing.SupplyLevelModerate
	}
	return manufacturing.ParseSupplyLevel(
		goodGatingOverridesFromContext(ctx).MinSupplyFor(good, manufacturing.SupplyLevelScarce.String()),
	)
}

// trailingMedianAsk returns the trailing-window median ASK (purchase_price — what WE PAY) for a
// good at a waypoint from the price-history reader, or ok=false when the reader is unwired, errors,
// or has no samples in the window. Extracted so the rescue cap and any history-based check share
// one median source.
//
// ALL THREE ok=false CASES FAIL CLOSED, IDENTICALLY. There is no fail-open nil-reader branch: every
// branch below returns (0, false) and the caller parks on all of them, so an unwired reader is
// indistinguishable in the log from a market with no history.
func (e *ProductionExecutor) trailingMedianAsk(ctx context.Context, waypoint, good string) (int, bool) {
	if e.priceHistory == nil {
		return 0, false
	}
	since := e.clock.Now().Add(-inputPriceCeilingWindow)
	history, err := e.priceHistory.GetPriceHistory(ctx, waypoint, good, since, 0)
	if err != nil || len(history) < inputPriceCeilingMinSamples {
		return 0, false
	}
	asks := make([]int, 0, len(history))
	for _, h := range history {
		asks = append(asks, h.PurchasePrice())
	}
	return medianInt(asks), true
}
