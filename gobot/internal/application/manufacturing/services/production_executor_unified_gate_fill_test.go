package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-vh1s Part B (GATING half) — unified gate-fill runs a construction deliver-to-gate buy
// MARGIN-BLIND, bounded only by solvency (9aoc) + physical throughput (Admiral §9 sign-off
// 2026-07-14). This suite pins the two files this lane owns:
//   - input_source_selector.go: ACTIVITY-aware routing (SCARCE+producing = buy, SCARCE+RESTRICTED
//     = feed/recurse) and the per-node SCARCE supply floor (gate default + per-good MinSupply
//     override), so deep chains (SILICON/ELECTRONICS) that never regenerate to MODERATE are not
//     frozen out by the MODERATE floor.
//   - input_price_ceiling.go: the per-tranche price ceiling and the chain-margin park are EXEMPTED
//     under gate mode; byte-identical when off. The 9aoc solvency floor is NEVER exempted.
//
// gate mode is activated on the run ctx exactly as production does it (sp-vh1s Part A): the
// unified_gate_fill toggle ON *and* a construction-site DeliveryTarget — precisely what the single
// predicate IsUnifiedGateNode(ctx), which the executor's per-node gates read, keys on. The withGateMode
// helper stamps both, so gate=false stays the byte-identical unstamped profit-factory path.

// withGateMode puts the run ctx into (or leaves it out of) unified gate-fill mode the same way lane A's
// coordinators do — stamping the unified_gate_fill toggle plus a construction-site delivery target makes
// IsUnifiedGateNode(ctx) true. gate=false returns ctx untouched: the unstamped, byte-identical-to-today
// profit-factory default the OFF assertions pin.
func withGateMode(ctx context.Context, gate bool) context.Context {
	if !gate {
		return ctx
	}
	return WithDeliveryTarget(WithUnifiedGateFill(ctx, true), ConstructionSiteTarget("X1-DR-GATE"))
}

// ROUTING (analyst live taproot as fixtures, kept generic on the supply+activity signal, never
// hard-coded on the good): feeding raises output ACTIVITY not SUPPLY (verified era-2/3), so a
// SCARCE source that is STILL PRODUCING (activity != RESTRICTED, e.g. COPPER@H51 SCARCE-import but
// its EXPORT source is producing) must be BOUGHT, while a SCARCE source that is DEAD (RESTRICTED =
// nothing feeding it, e.g. ELECTRONICS@F48) must be left for the tree to FEED/recurse — NOT bought.
// Under gate mode the floor is SCARCE, so both cases reach this decision.
func TestGateFill_ActivityRoutesBuyVsFeed(t *testing.T) {
	cases := []struct {
		name      string
		activity  string
		expectBuy bool
	}{
		{"scarce_strong_is_producing_buys", activityStrong, true},
		{"scarce_growing_is_producing_buys", activityGrowing, true},
		{"scarce_weak_is_producing_buys", activityWeak, true},
		{"scarce_restricted_is_dead_feeds", activityRestricted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &multiSourceMarketRepo{sources: []srcSpec{
				{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 5000, activity: tc.activity},
			}}
			executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
			logger := &dwellCapturingLogger{}
			ctx := withGateMode(common.WithLogger(context.Background(), logger), true)

			result := produceBuy(t, executor, shipRepo, ctx)

			if tc.expectBuy {
				if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
					t.Fatalf("a SCARCE+%s (producing) source must be BOUGHT under gate mode (SCARCE floor, margin-blind), got result=%+v attempts=%d", tc.activity, result, mediator.purchaseAttempts())
				}
				if result.WaypointSymbol != "X1-DR-SCARCE" {
					t.Fatalf("expected the buy to source the SCARCE producing market, got %s", result.WaypointSymbol)
				}
				return
			}
			if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
				t.Fatalf("a SCARCE+RESTRICTED (dead) source must FEED/recurse (park, zero-spend) under gate mode, never buy, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
			}
		})
	}
}

// SCARCE floor doesn't block a deep chain: the ADV freeze was a MODERATE floor permanently
// excluding SILICON/ELECTRONICS (which never regenerate to MODERATE under continuous buy). Gate
// mode floors at SCARCE, so a SCARCE-but-producing raw at the bottom of a deep chain is sourced
// instead of parked — the exact unfreeze. (Off gate mode, the same SCARCE-only good with no
// trailing median fail-closed parks, per sp-a5j7 — pinned by the existing source-selection suite.)
func TestGateFill_ScarceFloorDoesNotFreezeDeepChainRaw(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SILICON-SRC", supply: supplyScarce, ask: 1200, activity: activityGrowing},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	ctx := withGateMode(common.WithLogger(context.Background(), &dwellCapturingLogger{}), true)

	result := produceBuy(t, executor, shipRepo, ctx)

	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("gate mode's SCARCE floor must SOURCE a scarce-but-producing deep-chain raw (unfreeze), not park it at a MODERATE floor, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// PER-NODE MinSupply override is keyed by the CURRENT node's good (sp-sdyo threaded per node): a
// COPPER override gates the COPPER node's buy inside a gate-mode ADV tree, independently of any
// other good. Here the good's only source is SCARCE+producing, so the gate's SCARCE floor buys it
// by default. A per-good MinSupply=MODERATE override on THAT good raises ITS floor back to MODERATE
// (the pacing knob) — no MODERATE+ source exists and there is no trailing median, so it fail-closed
// PARKS; an override keyed on a DIFFERENT good leaves the target at the gate SCARCE default and
// still buys. This both proves per-good keying and pins the "MODERATE freezes a deep chain" hazard.
func TestGateFill_MinSupplyOverrideIsPerNode(t *testing.T) {
	cases := []struct {
		name         string
		overrideGood string
		expectBuy    bool
	}{
		{"override_on_other_good_leaves_target_at_gate_scarce_floor", "SOME_OTHER_GOOD", true},
		{"moderate_override_on_target_good_raises_its_floor_and_parks", dockRaceGood, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &multiSourceMarketRepo{sources: []srcSpec{
				{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 5000, activity: activityStrong},
			}}
			// No price-history reader → once a good is raised back to the MODERATE floor with no
			// eligible source, the classic rescue path fail-closed parks it.
			executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
			overrides := manufacturing.GoodGatingOverrides{tc.overrideGood: {MinSupply: supplyModerate}}
			ctx := withGateMode(WithGoodGatingOverrides(common.WithLogger(context.Background(), &dwellCapturingLogger{}), overrides), true)

			result := produceBuy(t, executor, shipRepo, ctx)

			if tc.expectBuy {
				if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
					t.Fatalf("an override keyed on a DIFFERENT good must leave the target at the gate SCARCE floor and buy the producing SCARCE source, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
				}
				return
			}
			if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
				t.Fatalf("a per-good MinSupply=MODERATE override on the target good must raise ITS floor (no MODERATE+ source, no median → fail-closed PARK), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
			}
		})
	}
}

// PRICE CEILING is EXEMPT under gate mode, ENFORCED byte-identical off it. The KA42 poisoning
// shape (a top-supply pick priced 12976 well above its ~4800 healthy peers, cross-market ceiling
// 7200): non-gate PARKS the ladder (unchanged from the sp-a5j7/hzz5 backstop); gate mode is
// margin-blind (Admiral §9) and PROCEEDS. Same fixture, gate flag flipped — the only difference is
// the exemption.
func TestGateFill_PriceCeilingExemptOnlyInGateMode(t *testing.T) {
	cases := []struct {
		name      string
		gate      bool
		expectBuy bool
	}{
		{"gate_mode_exempts_ceiling_and_buys_over_median", true, true},
		{"non_gate_enforces_ceiling_and_parks_over_median", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &multiSourceMarketRepo{sources: []srcSpec{
				{waypoint: "X1-DR-KA42", supply: supplyAbundant, ask: 12976}, // top-supply pick, priced like a ladder
				{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
				{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
			}}
			executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
			ctx := withGateMode(common.WithLogger(context.Background(), &dwellCapturingLogger{}), tc.gate)

			result := produceBuy(t, executor, shipRepo, ctx)

			if tc.expectBuy {
				if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
					t.Fatalf("gate mode is MARGIN-BLIND (Admiral §9): the 12976 pick over the 7200 cross-market ceiling must PROCEED, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
				}
				return
			}
			if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
				t.Fatalf("non-gate must ENFORCE the ceiling byte-identically: the 12976 pick over the 7200 ceiling must PARK, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
			}
		})
	}
}

// CHAIN-MARGIN park is EXEMPT under gate mode, ENFORCED byte-identical off it. A structurally
// underwater round (summed input ask 37700 >> output resale bid 7500) parks the input round today;
// gate mode drops that local-margin park (Admiral §9 — the gate is a fixed affordable investment,
// not a per-cycle profit factory). Tested directly on inputRoundMarginParked, matching the existing
// chain-margin-gate suite's style for this guard.
func TestGateFill_ChainMarginParkExemptOnlyInGateMode(t *testing.T) {
	cases := []struct {
		name       string
		gate       bool
		expectPark bool
	}{
		{"non_gate_parks_underwater_round", false, true},
		{"gate_mode_exempts_underwater_round", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
			executor, _, _ := newChainGateExecutor(t, repo)
			ctx := withGateMode(common.WithLogger(context.Background(), &dwellCapturingLogger{}), tc.gate)

			parked := executor.inputRoundMarginParked(ctx, cgChain(), cgSystem, 1)

			if parked != tc.expectPark {
				t.Fatalf("inputRoundMarginParked gate=%v: expected park=%v, got %v (gate mode drops the chain-margin park per Admiral §9; non-gate keeps it byte-identical)", tc.gate, tc.expectPark, parked)
			}
		})
	}
}

// SOLVENCY (9aoc) is NEVER exempted: margin-blind is not solvency-blind. With gate mode stamped, a
// buy that would drop live treasury below the working-capital reserve STILL parks fail-closed —
// the treasury floor stays hard exactly as it is off gate mode (mirrors the sp-sdyo override
// guardrail, here proving gate mode does not weaken the floor either).
func TestGateFill_SolvencyFloorStillParksUnderGateMode(t *testing.T) {
	// 40000 - 100 (the harness's fixed input-buy cost) = 39900 < 50000 reserve -> breach.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 40000})
	logger := &dwellCapturingLogger{}
	ctx := withGateMode(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-VH1S"), logger), true)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a breaching buy under gate mode must park gracefully, not error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("gate mode is margin-blind but NOT solvency-blind (9aoc preserved): a treasury breach must still PARK, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("expected the solvency-floor park WARNING even under gate mode, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}
