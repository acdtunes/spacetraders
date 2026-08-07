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

// Factory input-buy price guards, both at the executor's actual points of spend. The coordinator
// ChainMarginGuard projects a chain's P&L ONCE at launch; a supply ladder climbs DURING the
// per-tranche buy round, past that projection, so these two close the gap:
//
//	inputPriceCeilingParked  — per-buy: refuse an input whose live ask exceeds a median-ask
//	                           baseline × a multiplier. Catches the ladder mid-round, per tranche.
//	inputRoundMarginParked   — per-fabricate-round: refuse a chain already structurally
//	                           underwater (summed input ask > output resale bid) even when
//	                           no single input trips the ceiling.

const (
	// defaultInputPriceCeilingMultiplier is the ladder-chase ceiling: a factory input buy aborts
	// when the live ask exceeds this multiple of the good's median-ask baseline. A 0/absent config
	// value resolves to it at the point of use — a protective default that turns the GUARD on, not
	// money movement, so a default is correct (RULINGS #5).
	defaultInputPriceCeilingMultiplier = 1.5

	// inputPriceCeilingWindow is the trailing window the median-ask baseline is computed over. It
	// is wide enough that a multi-hour ladder's inflated on-change samples stay a MINORITY of the
	// window, keeping the median on the pre-ladder baseline.
	inputPriceCeilingWindow = 24 * time.Hour

	// inputPriceCeilingMinSamples is the fewest in-window history rows needed to trust the median.
	// Set to 1 DELIBERATELY: market_price_history records ON CHANGE, so a perfectly STABLE-priced
	// market has exactly ONE row forever — a higher bar would treat its median as "unavailable" and
	// fail CLOSED, permanently parking every stable-priced input, a guard that rejects a whole
	// class. Below this the median is genuinely unavailable -> fail closed (RULINGS #4).
	inputPriceCeilingMinSamples = 1
)

// InputPriceHistoryReader supplies the trailing ask series a rescue buy is validated against.
// Narrow by design — the check needs one good's history at one waypoint over a window, not the
// full market.MarketPriceHistoryRepository.
//
// A NIL READER FAILS CLOSED, NOT OPEN. There is no fail-open branch anywhere on this path:
// trailingMedianAsk returns (0, false) for a nil reader exactly as it does for no samples, and its
// only caller (rescueSource) parks the buy on false. Unwired, the effect is not "the ceiling is
// disabled" — it is "every rescue buy is refused forever". Do not delete the nil check expecting
// graceful degradation; that trades a park for a nil dereference on the request path.
//
// Leaving it unset is the fixture path only. The daemon wires the DB-backed price-history
// repository via SetPriceHistoryReader, pinned by executor_guard_wiring_test.go.
type InputPriceHistoryReader interface {
	GetPriceHistory(ctx context.Context, waypointSymbol, goodSymbol string, since time.Time, limit int) ([]*market.MarketPriceHistory, error)
}

// SetPriceHistoryReader wires the trailing-median source for the rescue-buy validator. The daemon
// always calls it; leaving it unset FAILS CLOSED (every rescue buy parks), not open — see
// InputPriceHistoryReader. Injected by setter rather than constructor so the executor's existing
// call sites stay untouched, the same idiom as SetSpendLedger.
func (e *ProductionExecutor) SetPriceHistoryReader(reader InputPriceHistoryReader) {
	e.priceHistory = reader
}

// inputPriceCeilingCtxKey carries the per-run ceiling config from the factory coordinator
// down to the point of spend. It rides on ctx (not a struct field) because the
// ProductionExecutor is a SINGLETON shared across every concurrent factory container, so a
// struct field would race between sibling factories running different config; ctx is
// per-Handle and race-free.
type inputPriceCeilingCtxKey struct{}

type inputPriceCeilingConfig struct {
	multiplier float64
	disabled   bool
}

// WithInputPriceCeiling stamps the per-run input-price-ceiling config onto ctx. A 0 multiplier
// resolves to defaultInputPriceCeilingMultiplier at the point of use; disabled=true is the
// emergency off-switch (RULINGS #5). A caller that never stamps this leaves the guard at its
// default multiplier, enabled.
func WithInputPriceCeiling(ctx context.Context, multiplier float64, disabled bool) context.Context {
	return context.WithValue(ctx, inputPriceCeilingCtxKey{}, inputPriceCeilingConfig{multiplier: multiplier, disabled: disabled})
}

func inputPriceCeilingConfigFromContext(ctx context.Context) inputPriceCeilingConfig {
	if v, ok := ctx.Value(inputPriceCeilingCtxKey{}).(inputPriceCeilingConfig); ok {
		return v
	}
	return inputPriceCeilingConfig{}
}

// inputPriceCeilingParked reports whether a factory input buy of `good` at `waypoint` for a live
// `ask` must PARK because the ask exceeds the ladder-chase ceiling: the CROSS-MARKET median ask of
// ELIGIBLE (MODERATE+) sources × the configured multiplier. This is the BACKSTOP to the
// supply-first selector — it catches a chosen eligible source priced anomalously above its healthy
// peers, and cross-market anomalies the selector's point-in-time supply read misses.
//
// THE BASELINE MUST BE CROSS-MARKET, NOT THIS WAYPOINT'S TRAILING MEDIAN. A ladder POISONS a
// per-waypoint median: the laddering source drags its own trailing median up behind it, so the
// ceiling chases the ladder and never fires. The median ask of ELIGIBLE sources cross-market
// (EligibleSourceMedianAsk) is structurally un-poisonable instead — a source that ladders degrades
// out of MODERATE+ supply and therefore out of the median.
//
// Fail CLOSED (PARK) when the eligible-median read itself fails — a guard whose job is refusing to
// overpay must not let a buy through blind (RULINGS #4). When NO eligible source exists (count==0)
// the buy is on the selector's rescue path, already price-validated by the rescue cap; this
// cross-market ceiling does not apply there, so it fails OPEN and defers.
//
// The park logs ONE INFO with good/ask/median/ceiling in the message TEXT (the container-log
// renderer drops metadata) — a routine protective decline. The blind fail-closed park logs WARNING,
// an operational fault. No executor-side dedup: buyGood is one call per good per pass and the
// container-log sink content-dedups.
func (e *ProductionExecutor) inputPriceCeilingParked(ctx context.Context, waypoint, good, systemSymbol string, playerID, ask int) bool {
	logger := common.LoggerFromContext(ctx)

	// Gate-fill runs are MARGIN-BLIND by standing order. The per-tranche ladder-chase ceiling is a
	// LOCAL per-material margin optimization the gate deliberately drops: the gate is a finite,
	// affordable, one-off investment, so margin-gating it stalls the unlock to save pennies. Spend
	// is bounded instead by the solvency floor in buyGood, which this bypass does not touch.
	if IsUnifiedGateNode(ctx) {
		return false
	}

	cfg := inputPriceCeilingConfigFromContext(ctx)
	if cfg.disabled {
		return false
	}

	multiplier := cfg.multiplier
	if multiplier <= 0 {
		multiplier = defaultInputPriceCeilingMultiplier
	}

	// A per-good override tunes THIS good's ladder ceiling only — the surgical knob for buying a
	// stuck bottleneck while every other good keeps the global multiplier. It is HARD-CAPPED at
	// MaxPriceCeilingMultiplier inside PriceCeilingMultFor so a fat-finger can only LOOSEN, never
	// DISABLE, the ceiling (RULINGS #4). It raises the per-tranche ladder ceiling ONLY: the
	// structural inputRoundMarginParked round-gate and the solvency floor read nothing from the
	// override and still park an underwater round or a treasury breach.
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
		// No eligible (MODERATE+) source to price against, so the ceiling DEFERS rather than parks.
		// That is safe only for SELECTOR-ROUTED buys (buyGood -> resolveInputSource ->
		// selectInputSource): reaching here means the selector found no MODERATE+ source and
		// returned a rescue pick it had ALREADY validated against the rescue cap, so deferring
		// avoids double-parking a buy that was priced once.
		//
		// PINNED-SOURCE buys (BuyAtTerminalFactory) break that implication — they never run the
		// selector, so nothing upstream applied a rescue cap, and a deferral here leaves the buy
		// with NO price validation at all, bounded only by the working-capital floor and the
		// concurrent-spend cap, which are solvency guards rather than price guards.
		//
		// SCOPE THAT PRECISELY, because the obvious reading of it is wrong. At the default buy
		// floor the delivery fleet's BuyPolicy pauses a material before the leg would buy at a
		// below-MODERATE factory, so "the terminal factory drained below MODERATE" does not reach
		// here; that opens only if an operator tunes --buy-floor beneath MODERATE. The genuinely
		// reachable window is MID-FILL DEPLETION: our own tranches push the factory below MODERATE
		// inside a single fillFromSource loop, dropping count to 0 for the remaining tranches after
		// the policy already ruled on a pre-buy reading. It is bounded by gateMaxTranchesPerStop,
		// by hull capacity, and by the working-capital floor.
		//
		// ACCEPTED, not overlooked — the delivery fleet's spec makes price deliberately not a gate
		// for it. But do not read this deferral as a price backstop for a pinned buy: there is none.
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

// inputRoundMarginParked reports whether a fabrication chain rooted at `node` must PARK before its
// input-buy round because it is structurally underwater: the summed live ask of its direct inputs
// already exceeds what its output resells for, so fabricating loses money every cycle regardless of
// the per-buy ceiling. This is the executor-level, live-at-buy-time re-check of the coordinator
// ChainMarginGuard's verdict — that guard projects ONCE at launch, and prices move during a pass.
//
//   - output bid = the good's live in-system resale sink (FindImportMarket, the same call the
//     harvest guard and ChainMarginGuard price against).
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

	// Gate-fill drops the chain-margin round-park too, on the same margin-blind rationale as the
	// per-tranche ceiling: a gate is a fixed one-off investment, not a per-cycle profit factory, so
	// a structurally-underwater LOCAL round is acceptable when it fills the gate. The solvency
	// floor in buyGood still bounds spend.
	if IsUnifiedGateNode(ctx) {
		return false
	}

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
