package services

import (
	"context"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-to2v — FABRICATION EFFICIENCY feeding policy (analyst adjustments #2, #3, #4; layered on the
// sp-vh1s throughput-paced margin-blind gate). It shapes HOW the executor feeds a fabricated node's
// inputs — sizing and ordering the per-window deliveries — without changing WHICH inputs the tree
// resolves (that is the resolver's job). The three verified mechanics it encodes:
//
//   #2 BALANCED-TO-LIMITING (the ~4x lever): feed a node's inputs in balanced proportion gated by
//      the SCARCEST (limiting) input's sourceable flow, never greedily piling onto the cheapest/
//      most-abundant one. Feeding ALL inputs balanced → +0.12 activity vs feeding SOME → +0.03.
//   #3 SATURATION-CAPPED TRANCHES: cap each per-input delivery at the saturation window
//      (~100-200u/window; Δactivity rolls off past ~200 and <25u does nothing), then move the hull
//      to the next starved node rather than dumping one node past saturation.
//   #4 TAPROOT-FIRST + FEED-RESPONSIVE-ONLY: feed the limiting input DEEPEST in the tree first (it
//      gates everything above it); and only feed goods whose OUTPUT activity actually responds to
//      feeding — ADVANCED_CIRCUITRY/SHIP_PLATING/SHIP_PARTS respond, EQUIPMENT/LAB_INSTRUMENTS/
//      FOOD/MEDICINE do NOT, so those are BUY-OR-SKIP (feeding them wastes hull-hours).
//
// Everything is parametrized (RULINGS #5) and rides ctx (per-Handle, race-free) for the SAME
// singleton-executor reason as the sibling sp-vh1s / sp-a5j7 / sp-sdyo configs: the
// ProductionExecutor is a boot singleton shared across concurrent factory containers, so per-run
// config on a struct field would race between a gate run and a profit factory. A caller that never
// stamps the policy (every pre-bead test, and every run while the fabrication_efficiency toggle is
// OFF) reads "not engaged" → the executor keeps its greedy byte-identical feeding.

const (
	// defaultFeedSaturationMaxUnits caps a single per-input delivery tranche this window. Δactivity
	// peaks at 101-200u and rolls off past 200 (wasted), so 200 is the analyst default (MEDIUM
	// confidence on the exact figure — tuned live). 0/absent resolves here (RULINGS #5).
	defaultFeedSaturationMaxUnits = 200
	// defaultFeedSaturationMinUnits is the min-effective delivery: <25u moves activity nothing, so a
	// balanced tranche is never sized below this (a dribble is wasted hull-hours). 0/absent resolves
	// here (RULINGS #5).
	defaultFeedSaturationMinUnits = 25
)

// defaultNonResponsiveFeedGoods is the analyst-verified set of OUTPUT goods whose activity does NOT
// respond to feeding (era-2/3, ample-feed controlled). A factory producing one of these gains
// nothing from being fed, so the executor BUY-OR-SKIPs it instead of burning hull-hours hauling its
// inputs. It is an EXCLUSION set, not a positive responder list: intermediates like ELECTRONICS /
// MICROPROCESSORS must stay fed (the recursion depends on them), so only the known dead-ends are
// listed and everything else is fed. Operator-overridable via WithFeedingPolicy (RULINGS #5).
var defaultNonResponsiveFeedGoods = map[string]bool{
	"EQUIPMENT":       true,
	"LAB_INSTRUMENTS": true,
	"FOOD":            true,
	"MEDICINE":        true,
}

type feedingPolicyConfig struct {
	saturationMaxUnits int
	saturationMinUnits int
	nonResponsiveGoods map[string]bool
	disabled           bool
	stamped            bool
}

type feedingPolicyCtxKey struct{}

// WithFeedingPolicy stamps the per-run fabrication-efficiency feeding policy onto ctx (sp-to2v). A 0
// saturationMaxUnits resolves to defaultFeedSaturationMaxUnits and a 0 saturationMinUnits to
// defaultFeedSaturationMinUnits at the point of use; a nil/empty nonResponsiveGoods resolves to
// defaultNonResponsiveFeedGoods, while a non-empty slice REPLACES the default (the analyst retunes
// which goods are worth feeding). disabled=true is the RULINGS #5 emergency off-switch that reverts
// the executor to greedy byte-identical feeding. Only stamped when the fabrication_efficiency toggle
// is on, so an OFF fleet reads "not engaged" and is unaffected.
func WithFeedingPolicy(ctx context.Context, saturationMaxUnits, saturationMinUnits int, nonResponsiveGoods []string, disabled bool) context.Context {
	cfg := feedingPolicyConfig{
		saturationMaxUnits: saturationMaxUnits,
		saturationMinUnits: saturationMinUnits,
		disabled:           disabled,
		stamped:            true,
	}
	if len(nonResponsiveGoods) > 0 {
		set := make(map[string]bool, len(nonResponsiveGoods))
		for _, g := range nonResponsiveGoods {
			set[g] = true
		}
		cfg.nonResponsiveGoods = set
	}
	return context.WithValue(ctx, feedingPolicyCtxKey{}, cfg)
}

// feedingPolicyEngaged reports whether the feeding policy is active for this run and, when so, the
// config with all absent/zero fields resolved to their live-by-default values. It is NOT engaged
// when unstamped (every OFF run and pre-bead test) or explicitly disabled — the greedy byte-identical
// path.
func feedingPolicyEngaged(ctx context.Context) (feedingPolicyConfig, bool) {
	cfg, _ := ctx.Value(feedingPolicyCtxKey{}).(feedingPolicyConfig)
	if !cfg.stamped || cfg.disabled {
		return feedingPolicyConfig{}, false
	}
	if cfg.saturationMaxUnits <= 0 {
		cfg.saturationMaxUnits = defaultFeedSaturationMaxUnits
	}
	if cfg.saturationMinUnits <= 0 {
		cfg.saturationMinUnits = defaultFeedSaturationMinUnits
	}
	if cfg.saturationMinUnits > cfg.saturationMaxUnits {
		cfg.saturationMinUnits = cfg.saturationMaxUnits // guard an inverted config from starving the tranche
	}
	if cfg.nonResponsiveGoods == nil {
		cfg.nonResponsiveGoods = defaultNonResponsiveFeedGoods
	}
	return cfg, true
}

// isFeedResponsive reports whether feeding a factory that PRODUCES good raises its output activity
// (#4). Keyed on the node's output good against the non-responsive exclusion set; anything not
// listed is fed.
func (c feedingPolicyConfig) isFeedResponsive(good string) bool {
	return !c.nonResponsiveGoods[good]
}

// feedCandidate pairs an input child with its sourceable-this-window availability (units). A
// negative avail means "unknown" (the source could not be sized) — excluded from the limiting
// calculation and ordered last.
type feedCandidate struct {
	child *goods.SupplyChainNode
	avail int
}

// balancedTranche is the per-input delivery cap this window (#2 fused with #3): the LIMITING
// (minimum) sourceable flow across the inputs, clamped into the saturation window
// [saturationMinUnits, saturationMaxUnits]. Every input is capped at this one balanced tranche, so
// the ample inputs are pulled down toward the scarce one's flow instead of being greedily piled on
// (the ~4x waste). With no measurable input (all unknown) there is nothing to balance to, so it
// falls back to the saturation max — a plain saturation cap, no balancing. Pure — no I/O.
func balancedTranche(cands []feedCandidate, cfg feedingPolicyConfig) int {
	limiting := -1
	for _, c := range cands {
		if c.avail < 0 {
			continue // unknown — cannot lower the limiting flow
		}
		if limiting < 0 || c.avail < limiting {
			limiting = c.avail
		}
	}
	if limiting < 0 {
		return cfg.saturationMaxUnits // nothing measurable to balance to → saturation-only
	}
	if limiting > cfg.saturationMaxUnits {
		return cfg.saturationMaxUnits // ample all round → saturate
	}
	if limiting < cfg.saturationMinUnits {
		return cfg.saturationMinUnits // below min-effective → deliver the min-effective tranche
	}
	return limiting
}

// orderTaprootFirst returns the inputs ordered TAPROOT-FIRST (#4a): the scarcest input (lowest
// avail) gates everything above it, so it is fed first; a deeper subtree breaks ties (the deepest
// limiting input is the true taproot); an un-sizeable (unknown avail) input sorts last so the hull
// feeds what it can measure first. Stable and non-mutating (copies the input slice). Pure — no I/O.
func orderTaprootFirst(cands []feedCandidate) []feedCandidate {
	ordered := make([]feedCandidate, len(cands))
	copy(ordered, cands)
	sort.SliceStable(ordered, func(i, j int) bool {
		ai, aj := ordered[i].avail, ordered[j].avail
		iUnknown, jUnknown := ai < 0, aj < 0
		if iUnknown != jUnknown {
			return !iUnknown // a measurable input sorts before an unknown one
		}
		if !iUnknown && ai != aj {
			return ai < aj // scarcer (lower avail) first — the taproot
		}
		return ordered[i].child.TotalDepth() > ordered[j].child.TotalDepth() // tie → deeper subtree first
	})
	return ordered
}

// inputFeedCapCtxKey carries the per-child balanced+saturation delivery cap from the feeding planner
// down to the point of purchase (buyGood for a leaf input, purchaseFabricatedOutput for a fabricated
// input's harvest). It rides ctx per-child (a fresh stamp for every child) so a parent's cap never
// leaks onto a grandchild — the grandchild's own feeding planner re-stamps a fresh cap for its
// subtree.
type inputFeedCapCtxKey struct{}

// WithInputFeedCap stamps the units cap for the input currently being sourced (sp-to2v). A
// non-positive cap is a no-op at the point of use.
func WithInputFeedCap(ctx context.Context, units int) context.Context {
	return context.WithValue(ctx, inputFeedCapCtxKey{}, units)
}

// inputFeedCapFromContext reports the current input's delivery cap, ok=false when none is stamped or
// it is non-positive (the buy keeps its ordinary trade-volume/hull sizing).
func inputFeedCapFromContext(ctx context.Context) (int, bool) {
	if v, ok := ctx.Value(inputFeedCapCtxKey{}).(int); ok && v > 0 {
		return v, true
	}
	return 0, false
}

// peekInputAvailability estimates how many units of an input good the executor can safely source
// this window — the supply-aware limit (SupplyLevel.CalculateSupplyAwareLimit) at its best in-system
// EXPORT/EXCHANGE source's trade volume. It is a lightweight DB read used ONLY to SIZE the balanced
// feed, never to pick the buy source (buyGood re-selects supply-first at purchase time), so a
// cheapest-export peek is a fine proxy for the limiting flow. Returns -1 ("unknown", excluded from
// the limiter) when no source can be read, so an unpriceable input never zeroes out the whole plan.
func (e *ProductionExecutor) peekInputAvailability(ctx context.Context, good, systemSymbol string, playerID int) int {
	src, err := e.marketLocator.FindExportMarket(ctx, good, systemSymbol, playerID)
	if err != nil || src == nil {
		return -1
	}
	return manufacturing.SupplyLevel(src.Supply).CalculateSupplyAwareLimit(src.TradeVolume)
}

// planBalancedFeed sizes and orders a fabricated node's input feed (sp-to2v #2/#3/#4a): it peeks
// each input's sourceable availability, computes the ONE balanced+saturation tranche cap every input
// is fed to (balancedTranche — the limiting flow clamped into the saturation window), and returns
// the children ordered taproot-first (scarcest/deepest first). The caller feeds each returned child
// with the returned cap stamped on ctx.
func (e *ProductionExecutor) planBalancedFeed(ctx context.Context, node *goods.SupplyChainNode, systemSymbol string, playerID int, cfg feedingPolicyConfig) ([]*goods.SupplyChainNode, int) {
	cands := make([]feedCandidate, 0, len(node.Children))
	for _, child := range node.Children {
		cands = append(cands, feedCandidate{child: child, avail: e.peekInputAvailability(ctx, child.Good, systemSymbol, playerID)})
	}
	tranche := balancedTranche(cands, cfg)
	ordered := orderTaprootFirst(cands)
	children := make([]*goods.SupplyChainNode, 0, len(ordered))
	for _, c := range ordered {
		children = append(children, c.child)
	}
	return children, tranche
}
