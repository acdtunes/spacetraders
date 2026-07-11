package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-a5j7 (Admiral design point, iv65 completion). The D39/ADVANCED_CIRCUITRY ladder was a
// SUPPLY event: each input buy exceeded the source market's trade_volume increment, drove its
// supply toward SCARCE, and the ask repriced as the SYMPTOM — price only moves AFTER supply
// does. iv65's inputPriceCeilingParked catches the damage once the ask has laddered (the
// LAGGING backstop); this gate reads the market's own SUPPLY enum — the CAUSAL signal that
// degrades first — and refuses an input buy into a depleted market before it pushes the
// ladder another rung.
//
// It is the LEADING sibling of the LAGGING price ceiling: wired immediately ahead of it in
// buyGood, ordered supply-gate → ceiling → capital-floor. The two stay independent guards
// (RULINGS #4: guards never weaken) — a buy that clears the supply gate still faces the
// ceiling, so a stale/cross-market supply read can never wave a ladder-priced ask through.

const (
	// defaultSupplyGateParkLevel is the cached supply level at or below which an input buy
	// parks (unless the feed leg still clears at the live ask). SCARCE is terminal depletion —
	// a buy into it ladders the ask hardest. A 0/absent config resolves here: a protective
	// default that turns the GUARD on, not money movement, so a default is correct (RULINGS #5).
	defaultSupplyGateParkLevel = supplyScarce
)

// inputSupplyGateCtxKey carries the per-run supply-gate config from the factory coordinator
// down to the point of spend. It rides on ctx (not a struct field) for the SAME reason as the
// price ceiling (WithInputPriceCeiling): the ProductionExecutor is a SINGLETON shared across
// every concurrent factory container, so a struct field would race between siblings running
// different config; ctx is per-Handle and race-free.
type inputSupplyGateCtxKey struct{}

type inputSupplyGateConfig struct {
	parkLevel string // supply level at or below which to PARK (default SCARCE)
	disabled  bool
}

// WithInputSupplyGate stamps the per-run supply-gate config onto ctx (sp-a5j7). An empty
// parkLevel resolves to defaultSupplyGateParkLevel at the point of use; disabled=true is the
// RULINGS #5 emergency off switch. A command built directly (tests) that never stamps this
// leaves the gate at its default (park SCARCE), enabled — the same idiom as WithInputPriceCeiling.
func WithInputSupplyGate(ctx context.Context, parkLevel string, disabled bool) context.Context {
	return context.WithValue(ctx, inputSupplyGateCtxKey{}, inputSupplyGateConfig{parkLevel: parkLevel, disabled: disabled})
}

func inputSupplyGateConfigFromContext(ctx context.Context) inputSupplyGateConfig {
	if v, ok := ctx.Value(inputSupplyGateCtxKey{}).(inputSupplyGateConfig); ok {
		return v
	}
	return inputSupplyGateConfig{}
}

// supplyOrdinal maps a supply enum to a total order (SCARCE < LIMITED < MODERATE < HIGH <
// ABUNDANT) so a threshold comparison is a single integer test. ok==false for an unknown/empty
// level — the caller fails CLOSED on it (RULINGS #4).
func supplyOrdinal(level string) (int, bool) {
	switch level {
	case supplyScarce:
		return 0, true
	case supplyLimited:
		return 1, true
	case supplyModerate:
		return 2, true
	case supplyHigh:
		return 3, true
	case supplyAbundant:
		return 4, true
	default:
		return 0, false
	}
}

// inputSupplyGateParked reports whether a factory input buy of `good` at `marketResult` must
// PARK because the market's cached supply state is at or below the configured park level
// (default SCARCE), UNLESS the feed leg still clears at the live ask (sp-a5j7). Supply is the
// CAUSAL signal the price ceiling's ask baseline only reflects after the fact — so this is the
// LEADING gate.
//
// Behavior by cached supply:
//   - at/below parkLevel (default SCARCE): PARK, UNLESS feedLegClears (the "chain margin still
//     clears at the live ask" exception) — then proceed, so a still-profitable buy is not
//     over-parked.
//   - LIMITED (above the default park floor): WARN and proceed (the spec's "LIMITED = warn").
//   - MODERATE / HIGH / ABUNDANT: proceed silently.
//   - unreadable/empty: fail CLOSED (PARK) — a guard blind to its own signal must not spend
//     (RULINGS #4). Safe from the "guard rejects a class" fleet-killer: a good only reaches
//     here via FindExportMarket (SellPrice>0), and a priced good always carries supply from the
//     same detailed scan (the API returns price+supply together), so an empty level here is a
//     data anomaly, never the common case.
//
// Fail OPEN (proceed) only when disabled — the RULINGS #5 off switch. The park logs at INFO (a
// routine protective decline, like the ceiling) with good/supply/ask in the message TEXT (the
// container-log renderer drops metadata, sp-iqyq); the unreadable fail-closed logs WARNING (a
// blind guard is an operational fault, not a routine decline). No executor-side dedup: like
// every sibling park it logs per-park and relies on the container-log sink's 60s content-dedup
// — buyGood is one call per good per pass, and once parked the ask stabilizes so repeats collapse.
func (e *ProductionExecutor) inputSupplyGateParked(ctx context.Context, marketResult *MarketLocatorResult, good, systemSymbol string, playerID int) bool {
	logger := common.LoggerFromContext(ctx)

	cfg := inputSupplyGateConfigFromContext(ctx)
	if cfg.disabled {
		return false
	}

	supply := marketResult.Supply
	supplyOrd, ok := supplyOrdinal(supply)
	if !ok {
		logger.Log("WARNING", fmt.Sprintf(
			"Could not read supply state for %s at %s (%q) for the input supply gate — parking input buy (fail-closed) (sp-a5j7)",
			good, marketResult.WaypointSymbol, supply,
		), map[string]interface{}{
			"good": good, "market": marketResult.WaypointSymbol, "supply": supply,
			"action": "factory_parked", "reason": "supply_gate_unreadable",
		})
		return true
	}

	parkLevel := cfg.parkLevel
	parkOrd, ok := supplyOrdinal(parkLevel)
	if !ok {
		parkLevel = defaultSupplyGateParkLevel
		parkOrd, _ = supplyOrdinal(defaultSupplyGateParkLevel)
	}

	if supplyOrd <= parkOrd {
		// Depleted supply. The exception: if the feed leg still clears at the live ask — the
		// importer will still pay at least what we pay — the chain margin holds despite the
		// degraded state, so proceed (don't over-park a still-profitable chain).
		if e.feedLegClears(ctx, good, marketResult.Price, systemSymbol, playerID) {
			logger.Log("INFO", fmt.Sprintf(
				"Input buy of %s at %s proceeds despite %s supply — the feed leg still clears at the live ask %d (supply-gate margin exception, sp-a5j7)",
				good, marketResult.WaypointSymbol, supply, marketResult.Price,
			), map[string]interface{}{
				"good": good, "market": marketResult.WaypointSymbol, "supply": supply, "ask": marketResult.Price,
				"action": "supply_gate_margin_clear",
			})
			return false
		}
		logger.Log("INFO", fmt.Sprintf(
			"Parked input purchase of %s at %s — supply is %s (at/below park level %s) and the feed leg is underwater at the live ask %d: refusing to ladder a depleted market, supply is the causal signal (sp-a5j7)",
			good, marketResult.WaypointSymbol, supply, parkLevel, marketResult.Price,
		), map[string]interface{}{
			"good": good, "market": marketResult.WaypointSymbol, "supply": supply, "park_level": parkLevel, "ask": marketResult.Price,
			"action": "factory_parked", "reason": "supply_gate",
		})
		return true
	}

	if supply == supplyLimited {
		logger.Log("WARNING", fmt.Sprintf(
			"Input buy of %s at %s proceeds on LIMITED supply — one state above the %s park floor; watching for further depletion (sp-a5j7)",
			good, marketResult.WaypointSymbol, parkLevel,
		), map[string]interface{}{
			"good": good, "market": marketResult.WaypointSymbol, "supply": supply,
			"action": "supply_gate_warn",
		})
		return false
	}

	return false
}

// feedLegClears reports whether buying `good` at liveAsk still delivers non-negatively into
// its best importer — feed-leg P&L (importBid − liveAsk) >= 0, the SAME per-unit idiom
// ChainMarginGuard.branchPL prices a feed leg with. This is the supply gate's "chain margin
// still clears at the live ask" exception.
//
// Fails to clear (returns false → the depleted-supply park stands) when the delivery is
// UNPRICEABLE: PARK is the supply gate's default and the exception is granted only on positive
// proof that the leg clears (the spec's "unless..."). Narrowly scoped to already-degraded
// markets, so failing the exception closed never becomes a broad class rejection — unlike
// inputRoundMarginParked, which runs for EVERY round and therefore fails open to avoid
// over-parking healthy chains.
func (e *ProductionExecutor) feedLegClears(ctx context.Context, good string, liveAsk int, systemSymbol string, playerID int) bool {
	sink, err := e.marketLocator.FindImportMarket(ctx, good, systemSymbol, playerID)
	if err != nil || sink == nil {
		return false
	}
	return sink.Price >= liveAsk
}
