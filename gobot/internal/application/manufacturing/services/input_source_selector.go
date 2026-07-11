package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-a5j7 Phase 2 (wedx restoration + hzz5 X4). The runtime input-buy path (buyGood) chose its
// source PRICE-FIRST via FindExportMarket, ignoring the supply/activity data it even logged.
// The original SupplyChainResolver design — alive but bypassed at market_locator.go:254
// FindExportMarketBySupplyPriority — ranked SUPPLY-FIRST (MODERATE+ only, supply > activity >
// price). The trade analyst ruled (sp-wedx) and the full-factory review confirmed (sp-hzz5)
// that supply-first is the correct PRIMARY policy: every input blowup this era (parts -220k,
// the micro chase, electronics -891k, the -6.6M furnace) began at a SCARCE/LIMITED source;
// zero from ABUNDANT/HIGH. Supply is the LEADING indicator (SCARCE regenerates ~194min
// half-life — it ladders immediately under draw); price is the LAGGING signal (by the time the
// ask ladders, the money is spent). selectInputSource restores the design: never pick a
// depleted source on the normal path, so the ladder the iv65 ceiling failed to catch cannot
// even begin.

const (
	// defaultRescueMultiplier caps the rescue clause (wedx (a)): when NO eligible (MODERATE+)
	// source exists and the chain is blocked on this input, a SCARCE/LIMITED source is bought
	// ONLY if its ask is within this multiple of the good's trailing median. Tighter than the
	// iv65 ceiling's 1.5x because a rescue buy is already into a depleted market — accept it
	// only barely above baseline, never a ladder. A 0/absent config resolves here (RULINGS #5).
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

	// Price-first exception — ERA-END mode (< T-6h): mean-reversion has no time to work, so a
	// cheap ask that clears margin NOW beats waiting (wedx (3)(i)). Daemon-toggled at the same
	// boundary as the stocker rundown.
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

	// No eligible MODERATE+ EXPORT source. RESCUE / EXCHANGE fallback: being in buyGood for a
	// required input with no healthy source means the chain is blocked on it (wedx rescue
	// precondition). Fall to the cheapest available source (price-first) but buy ONLY within the
	// rescue cap; otherwise PARK rather than ladder a depleted market.
	fallback, ferr := e.marketLocator.FindExportMarket(ctx, good, systemSymbol, playerID)
	if ferr != nil || fallback == nil {
		return nil, sourceModeNone, ferr // genuinely unsourceable in-system
	}

	rescueMult := cfg.rescueMultiplier
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

// trailingMedianAsk returns the trailing-window median SELL price (ask) for a good at a waypoint
// from the price-history reader, or ok=false when the reader is unwired, errors, or has no
// samples in the window. Extracted so the rescue cap and any history-based check share one
// median source with identical fail-open (nil reader) / fail-closed (no samples) semantics.
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
		asks = append(asks, h.SellPrice())
	}
	return medianInt(asks), true
}
