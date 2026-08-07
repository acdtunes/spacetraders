package services

import (
	"context"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// The FABRICATION EFFICIENCY feeding policy shapes HOW the executor feeds a fabricated node's
// inputs — sizing and ordering the per-window deliveries — without changing WHICH inputs the tree
// resolves, which is the resolver's job. Three mechanics:
//
//	BALANCED-TO-LIMITING: feed a node's inputs in balanced proportion gated by the SCARCEST
//	(limiting) input's sourceable flow, never greedily piling onto the cheapest or most abundant
//	one. Activity responds to feeding ALL inputs, far more than to feeding some of them harder.
//
//	SATURATION-CAPPED TRANCHES: cap each per-input delivery at the saturation window, then move
//	the hull to the next starved node rather than dumping one node past saturation, where extra
//	units move activity nothing.
//
//	TAPROOT-FIRST + FEED-RESPONSIVE-ONLY: feed the limiting input DEEPEST in the tree first, since
//	it gates everything above it; and only feed goods whose OUTPUT activity actually responds to
//	feeding — the rest are BUY-OR-SKIP, because hauling their inputs buys nothing.
//
// This is the executor's SOLE feeding path: there is no alternative greedy mode and no per-run
// coefficient override, so nothing here can race on ctx.

const (
	// defaultFeedSaturationMaxUnits caps a single per-input delivery tranche this window. Activity
	// gain rolls off past the saturation window, so units beyond it are wasted hull-hours.
	// 0/absent resolves here (RULINGS #5).
	defaultFeedSaturationMaxUnits = 200
	// defaultFeedSaturationMinUnits is the min-effective delivery: below it a delivery moves
	// activity nothing, so a balanced tranche is never sized smaller. 0/absent resolves here
	// (RULINGS #5).
	defaultFeedSaturationMinUnits = 25
)

// defaultNonResponsiveFeedGoods is the set of OUTPUT goods whose activity does NOT respond to
// feeding. A factory producing one of these gains nothing from being fed, so the executor
// BUY-OR-SKIPs it instead of burning hull-hours hauling its inputs. It is an EXCLUSION set, not a
// positive responder list: intermediates such as ELECTRONICS/MICROPROCESSORS must stay fed because
// the recursion depends on them, so only known dead-ends are listed and everything else is fed.
// There is no per-run operator override.
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
}

// defaultFeedingPolicy is the SOLE feeding policy the executor runs: the analyst-tuned
// saturation window [defaultFeedSaturationMinUnits, defaultFeedSaturationMaxUnits] and the verified
// non-responsive OUTPUT-good exclusion set. It replaces the deleted fabrication_efficiency toggle +
// WithFeedingPolicy/feedingPolicyEngaged plumbing — balanced feeding was LIVE-on with exactly these
// default coefficients, so unconditionally engaging them here is byte-identical to that live path.
func defaultFeedingPolicy() feedingPolicyConfig {
	return feedingPolicyConfig{
		saturationMaxUnits: defaultFeedSaturationMaxUnits,
		saturationMinUnits: defaultFeedSaturationMinUnits,
		nonResponsiveGoods: defaultNonResponsiveFeedGoods,
	}
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
