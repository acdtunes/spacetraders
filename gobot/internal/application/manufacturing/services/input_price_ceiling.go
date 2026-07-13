package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-iv65 (P1 money-integrity). The factory input buyer had NO price ceiling. The
// ADVANCED_CIRCUITRY chain bought ELECTRONICS+MICROPROCESSORS inputs at ~19k/u — 4x
// market, chasing its own supply ladder up as each buy repriced the source — to
// fabricate a ~7k/u output: −6.6M in 3h (−2.2M/hr), the operation's single largest
// active leak. The coordinator ChainMarginGuard (sp-2dv4) already projects the whole
// chain's live P&L BEFORE a pass, but it runs ONCE at launch (run_factory_coordinator
// Step 2.5); the ladder climbs DURING the per-tranche input-buy round, past a projection
// made when the source ask was still ~4.75k. This file adds the two guards that gap
// needs, both at the executor's actual points of spend:
//
//	inputPriceCeilingParked  — per-buy: refuse an input whose live ask exceeds the
//	                           trailing-median ask × a multiplier (default 1.5). Catches
//	                           the ladder mid-round, per tranche.
//	inputRoundMarginParked   — per-fabricate-round: refuse a chain already structurally
//	                           underwater (summed input ask > output resale bid) even when
//	                           no single input trips the ceiling.

const (
	// defaultInputPriceCeilingMultiplier is the ladder-chase ceiling from the trade
	// analyst's ruling: a factory input buy aborts when the live ask exceeds this
	// multiple of the good's trailing-median ask. A 0/absent config value resolves to
	// this at the point of use — a protective default that turns the GUARD on (not money
	// movement), so a default is correct (RULINGS #5). 1.5 = refuse to pay more than 50%
	// over the recent baseline.
	defaultInputPriceCeilingMultiplier = 1.5

	// inputPriceCeilingWindow is the trailing window over which the median-ask baseline is
	// computed. 24h so a multi-hour ladder's handful of inflated on-change samples stay a
	// MINORITY of the window (the leak laddered ~10 buys over 3h against a market a scout
	// samples far more often), keeping the median on the pre-ladder baseline.
	inputPriceCeilingWindow = 24 * time.Hour

	// inputPriceCeilingMinSamples is the fewest in-window history rows needed to trust the
	// median. Set to 1 DELIBERATELY: market_price_history records ON CHANGE, so a perfectly
	// STABLE-priced market has exactly ONE row forever — a higher bar would treat its median
	// as "unavailable" and fail CLOSED, permanently parking every stable-priced input (the
	// "guard rejects a class" fleet-killer). Below this the median is genuinely unavailable →
	// fail closed (RULINGS #4).
	inputPriceCeilingMinSamples = 1
)

// InputPriceHistoryReader supplies the trailing ask series the input price ceiling is
// checked against (sp-iv65). Narrow by design — the ceiling needs only one good's history
// at one waypoint over a window, not the full market.MarketPriceHistoryRepository. A nil
// reader disables the ceiling: the optional-port fail-OPEN contract the package's test
// fixtures rely on (they wire nothing), identical to apiClient/spendLedger. The daemon
// wires the real DB-backed GormMarketPriceHistoryRepository via SetPriceHistoryReader.
type InputPriceHistoryReader interface {
	GetPriceHistory(ctx context.Context, waypointSymbol, goodSymbol string, since time.Time, limit int) ([]*market.MarketPriceHistory, error)
}

// SetPriceHistoryReader wires the trailing-median source for the factory input price
// ceiling (sp-iv65). The daemon calls this after construction with the DB-backed price
// history repository; leaving it unset keeps the ceiling fail-open, which is exactly what
// every non-daemon caller (the package's test fixtures) wants. Injected by setter, not
// constructor, so the executor's many existing call sites stay untouched — the same idiom
// as SetSpendLedger.
func (e *ProductionExecutor) SetPriceHistoryReader(reader InputPriceHistoryReader) {
	e.priceHistory = reader
}

// inputPriceCeilingCtxKey carries the per-run ceiling config from the factory coordinator
// down to the point of spend. It rides on ctx (not a struct field) for the SAME reason as
// the working-capital reserve (WithConfiguredReserve): the ProductionExecutor is a
// SINGLETON shared across every concurrent factory container, so a struct field would race
// between sibling factories running different config; ctx is per-Handle and race-free.
type inputPriceCeilingCtxKey struct{}

type inputPriceCeilingConfig struct {
	multiplier float64
	disabled   bool
}

// WithInputPriceCeiling stamps the per-run input-price-ceiling config onto ctx (sp-iv65).
// A 0 multiplier resolves to defaultInputPriceCeilingMultiplier at the point of use;
// disabled=true is the emergency off-switch (RULINGS #5). A command built directly (tests)
// that never stamps this leaves the guard at its default multiplier, enabled.
func WithInputPriceCeiling(ctx context.Context, multiplier float64, disabled bool) context.Context {
	return context.WithValue(ctx, inputPriceCeilingCtxKey{}, inputPriceCeilingConfig{multiplier: multiplier, disabled: disabled})
}

func inputPriceCeilingConfigFromContext(ctx context.Context) inputPriceCeilingConfig {
	if v, ok := ctx.Value(inputPriceCeilingCtxKey{}).(inputPriceCeilingConfig); ok {
		return v
	}
	return inputPriceCeilingConfig{}
}

// inputPriceCeilingParked reports whether a factory input buy of `good` at `waypoint` for a
// live `ask` must PARK because the ask exceeds the ladder-chase ceiling: the CROSS-MARKET
// median ask of ELIGIBLE (MODERATE+) sources × the configured multiplier (sp-iv65 origin;
// baseline hardened by sp-a5j7 Phase 2 / hzz5 X4). This is the BACKSTOP to the supply-first
// selector — it catches a chosen eligible source priced anomalously above its healthy peers,
// and stale/cross-market anomalies the selector's point-in-time supply read misses.
//
// BASELINE FIX (hzz5 X4 — the iv65 live failure): the original iv65 ceiling used this good's
// TRAILING PER-WAYPOINT median, which a ladder POISONS — a laddering source drags its own
// trailing median up behind it, so the 1.5x ceiling chases the ladder and never fires (KA42:
// ELECTRONICS laddered 8,973→12,976/u with ZERO parks because the 24h KA42 median was
// self-inflated). The baseline is now the median ask of ELIGIBLE sources CROSS-MARKET
// (EligibleSourceMedianAsk): a source that ladders degrades out of MODERATE+ supply and
// therefore out of this median, so it is structurally un-poisonable.
//
// Fail CLOSED (PARK) when the eligible-median read itself fails — a guard whose job is
// refusing to overpay must not let a buy through blind (RULINGS #4). When NO eligible source
// exists (count==0) the buy is on the selector's rescue path, already price-validated by the
// 1.2x rescue cap; this cross-market ceiling does not apply there, so it fails OPEN and defers.
//
// The park logs ONE INFO with good/ask/median/ceiling in the message TEXT (the container-log
// renderer drops metadata, sp-iqyq) — a routine protective decline. The blind fail-closed park
// logs WARNING (an operational fault). No executor-side dedup: buyGood is one call per good per
// pass and the container-log sink content-dedups at 60s.
func (e *ProductionExecutor) inputPriceCeilingParked(ctx context.Context, waypoint, good, systemSymbol string, playerID, ask int) bool {
	logger := common.LoggerFromContext(ctx)

	cfg := inputPriceCeilingConfigFromContext(ctx)
	if cfg.disabled {
		return false
	}

	multiplier := cfg.multiplier
	if multiplier <= 0 {
		multiplier = defaultInputPriceCeilingMultiplier
	}

	// sp-sdyo: a per-good override tunes THIS good's ladder ceiling only — the surgical knob for
	// buying a stuck bottleneck past the global 1.5x while every other good keeps the global
	// multiplier (a non-overridden good's ceiling is byte-identical to today). The override is
	// HARD-CAPPED at MaxPriceCeilingMultiplier inside PriceCeilingMultFor so a fat-finger can only
	// LOOSEN, never DISABLE, the ceiling (RULINGS #4). This raises the per-tranche ladder ceiling
	// ONLY: the structural inputRoundMarginParked round-gate and the sp-9aoc solvency floor read
	// nothing from the override and still park an underwater round / a treasury breach — the
	// sp-iv65 bleed stays prevented for overridden and non-overridden goods alike.
	multiplier = goodGatingOverridesFromContext(ctx).PriceCeilingMultFor(good, multiplier)

	median, count, err := e.marketLocator.EligibleSourceMedianAsk(ctx, good, systemSymbol, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Could not read the eligible-source cross-market median for %s (system %s) for the input price ceiling — parking input buy (fail-closed): %v",
			good, systemSymbol, err,
		), map[string]interface{}{
			"good": good, "market": waypoint, "action": "factory_parked", "reason": "price_ceiling_unreadable", "error": err.Error(),
		})
		return true
	}
	if count < inputPriceCeilingMinSamples {
		// No eligible (MODERATE+) source: the buy is on the selector's rescue path, already
		// gated by the 1.2x rescue cap. The cross-market ceiling has no healthy peer set to
		// price against here, so it defers rather than double-parking a validated rescue buy.
		return false
	}

	ceiling := int(float64(median) * multiplier)
	if ask > ceiling {
		logger.Log("INFO", fmt.Sprintf(
			"Parked input purchase of %s at %s — ask %d exceeds price ceiling %d (%.2fx eligible cross-market median %d over %d source(s)): ladder-chase refused, poison-proof baseline (sp-a5j7)",
			good, waypoint, ask, ceiling, multiplier, median, count,
		), map[string]interface{}{
			"good": good, "market": waypoint, "ask": ask, "median": median, "ceiling": ceiling, "multiplier": multiplier, "eligible_sources": count,
			"action": "factory_parked", "reason": "price_ceiling",
		})
		return true
	}
	return false
}

// inputRoundMarginParked reports whether a fabrication chain rooted at `node` must PARK
// before its input-buy round because it is structurally underwater: the summed live ask of
// its direct inputs already exceeds what its output resells for, so fabricating loses money
// every cycle regardless of the per-buy ceiling (sp-iv65 fix-shape, 2nd half). This is the
// executor-level, live-at-buy-time re-check of the coordinator ChainMarginGuard's (sp-2dv4)
// negative-margin verdict — that guard projects ONCE at launch; prices move during a pass.
//
//   - output bid = the good's live in-system resale sink (FindImportMarket, the same call
//     the bp6f #3 harvest guard and ChainMarginGuard price against).
//   - input asks = each direct child's live source ask (FindExportMarket).
//
// sum(child asks) > sink bid → PARK. The caller scopes this to !inputsOnly resale runs.
//
// Fails OPEN (proceed) on any UNPRICEABLE stage: a missing sink or child source is not a
// negative-margin signal, and a root resale chain with no sink is already fail-closed-parked
// upstream by ChainMarginGuard at launch. Failing closed here would over-park intermediate
// feeds (delivered to a parent factory, never resold), the "guard rejects a class"
// fleet-killer. The narrow, money-safe job of THIS gate is the priceable-but-underwater case.
func (e *ProductionExecutor) inputRoundMarginParked(ctx context.Context, node *goods.SupplyChainNode, systemSymbol string, playerID int) bool {
	logger := common.LoggerFromContext(ctx)

	sink, err := e.marketLocator.FindImportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil || sink == nil {
		return false // unpriceable sink → proceed (root no-sink is parked upstream; don't over-park intermediates)
	}

	totalAsk := 0
	stages := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		src, serr := e.marketLocator.FindExportMarket(ctx, child.Good, systemSymbol, playerID)
		if serr != nil || src == nil {
			return false // a child we can't price → can't assess the round; let the per-buy guards handle it
		}
		totalAsk += src.Price
		stages = append(stages, fmt.Sprintf("%s ask%d", child.Good, src.Price))
	}

	if totalAsk > sink.Price {
		logger.Log("WARNING", fmt.Sprintf(
			"Parked fabrication of %s — summed input ask %d exceeds output resale bid %d at %s: fabricating loses money every cycle, refusing the input round (sp-iv65). inputs[%s]",
			node.Good, totalAsk, sink.Price, sink.WaypointSymbol, strings.Join(stages, "; "),
		), map[string]interface{}{
			"good": node.Good, "sink": sink.WaypointSymbol, "sink_bid": sink.Price, "input_ask_sum": totalAsk,
			"action": "factory_parked", "reason": "negative_input_margin",
		})
		return true
	}
	return false
}

// medianInt returns the median of a non-empty int slice; the caller guarantees len >= 1
// (inputPriceCeilingMinSamples). An even count averages the two middle values, matching the
// persistence package's float median (history_repository.go) so both compute the same figure.
func medianInt(values []int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}
