package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// marketResolver is the narrow view of MarketLocator that GateTopology needs. Declaring it
// here (consumer-side) keeps the seam testable with a small fake instead of a full locator,
// and documents exactly which two role lookups the topology depends on.
// *MarketLocator satisfies this interface.
type marketResolver interface {
	FindExportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
	FindImportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
}

// Compile-time enforcement of the conformance claimed above. Without this line the comment is
// an unchecked assertion: a signature change to either locator method would silently make it
// false, and nothing would fail until someone tried to wire the two together.
var _ marketResolver = (*MarketLocator)(nil)

// GateTopology resolves gate-construction topology by market ROLE, never by waypoint symbol.
//
// Waypoint numbering is regenerated every era, so any symbol literal in this layer is a bug
// that survives exactly until the next era rolls. Goods are the invariant (every era's gate
// needs FAB_MATS and ADVANCED_CIRCUITRY, and the recipe graph is a game constant); locations
// are discovered from market import/export data at runtime.
type GateTopology struct {
	markets        marketResolver
	supplyChainMap map[string][]string
}

func NewGateTopology(markets marketResolver, supplyChainMap map[string][]string) *GateTopology {
	return &GateTopology{markets: markets, supplyChainMap: supplyChainMap}
}

// IsRaw reports whether good must be bought or mined rather than fabricated.
//
// THE RECIPE MAP IS CYCLIC, NOT A DAG. It closes at least this loop:
//
//	IRON_ORE -> EXPLOSIVES -> LIQUID_HYDROGEN -> MACHINERY -> IRON -> IRON_ORE
//
// and both gate materials feed into it (FAB_MATS={IRON,QUARTZ_SAND}; ADVANCED_CIRCUITRY reaches
// SILICON_CRYSTALS through ELECTRONICS/MICROPROCESSORS). staticSupplyChainDepth in
// run_construction_coordinator_budget.go has documented this correctly all along.
//
// "Has no recipe" is therefore NOT "is raw", and this method used to conflate them. Every ore and
// crystal in the game HAS a recipe entry — they are all {EXPLOSIVES} — so !hasRecipe called none
// of the actual raw materials raw, IRON_ORE included. That is the literal good that stranded a
// hauler at 80/80 in sp-b27a2. goods.IsMineableRawMaterial is the domain's curated answer to the
// question this method is actually asking, and shouldBuyGood in supply_chain_resolver.go already
// needed its own correction on top of hasRecipe for the same reason ("SILICON_CRYSTALS has a
// recipe (needs EXPLOSIVES)").
//
// The no-recipe half is KEPT rather than replaced by the curated list: a good absent from the map
// entirely is still raw. Dropping it would make every unknown or newly-added good look fabricable,
// which is the opposite failure.
//
// The curated list is package-level and deliberately not injected alongside supplyChainMap. Which
// goods are minable is a game constant, not per-instance config; a second seam would only let a
// caller construct a topology whose two halves disagree.
//
// TERMINATION. This predicate is what actually bottoms out the recursion, and it is NOT sufficient
// on its own: it cuts the loop above at IRON_ORE, but the map is data that ships with the game and
// the curated list is hand-maintained, so neither is a proof of acyclicity. A recursive walk built
// on this seam MUST still carry cycle detection, and THE FABRICATE DEPTH CAP MUST NOT BE DELETED
// ON THE ARGUMENT THAT THE RECIPE GRAPH IS AN ACYCLIC DAG (sp-4irrr) — that argument is false, and
// fabricate_depth.go's cap is doing real work, not acting as a redundant backstop.
func (t *GateTopology) IsRaw(good string) bool {
	if goods.IsMineableRawMaterial(good) {
		return true
	}
	inputs, ok := t.supplyChainMap[good]
	return !ok || len(inputs) == 0
}

// Inputs returns the recipe inputs for good, or nil when good is raw.
//
// The returned slice is a copy. supplyChainMap is shared with SupplyChainResolver, so handing
// out its backing array would let any caller that sorts or index-assigns the result corrupt
// recipe data for every other reader in the process. Recipes are 2-3 elements and this is not
// a hot path, so eliminating the hazard costs nothing.
//
// Raw goods keep returning a nil slice, not an empty one: IsRaw(g) is true exactly when
// Inputs(g) is nil, and the recursion in later phases depends on that biconditional.
//
// NOT INTERCHANGEABLE WITH goods.GetRequiredInputs. The two answer different questions and are
// kept deliberately distinct; substituting one for the other is a silent behaviour change, not a
// refactor. They differ on BOTH axes:
//
//   - Shape: this returns nil for a raw good, GetRequiredInputs returns []string{}. Both of its
//     idioms at every call site today are len()==0 and range, which are blind to the difference —
//     so a swap would go unnoticed until something compared against nil.
//   - CONTENT, which is the sharper hazard. Since sp-4irrr this method treats a curated mineable
//     raw material as raw, so Inputs("IRON_ORE") is nil while GetRequiredInputs("IRON_ORE") is
//     still {"EXPLOSIVES"}. A walk that swapped in GetRequiredInputs would descend an ore into the
//     recipe cycle and stop terminating.
//
// GetRequiredInputs is the honest reading of the raw map and is correct for its four callers,
// which ask "what does this recipe list" — a fabricate-eligibility question. This method answers
// "what must I still source", which is the recursion's question. Neither contract moves.
func (t *GateTopology) Inputs(good string) []string {
	if t.IsRaw(good) {
		return nil
	}
	inputs := t.supplyChainMap[good]
	recipe := make([]string, len(inputs))
	copy(recipe, inputs)
	return recipe
}

// TerminalFactory resolves the waypoint that EXPORTS good this era — the factory whose
// output the delivery fleet buys.
//
// This also serves the spec's RAW SOURCE role: a raw good (IsRaw) is bought from whatever
// exports it, which is the same lookup. The two roles differ in the caller's intent, not in
// the resolution, so they deliberately share one method rather than duplicating it behind a
// second name that could drift.
//
// Refuses (error, nil result) when nothing exports the good. There is deliberately no
// fallback: substituting a different waypoint is precisely how cargo ends up somewhere
// that cannot accept it.
//
// Both refusal branches are live. *MarketLocator reports a missing exporter as an error, so
// today's production path refuses through the wrap below; the nil-result branch covers the
// (nil, nil) not-found convention that this interface permits and that sibling locators in
// market_locator.go already use. Either way the seam fails closed rather than returning a
// waypoint the caller did not ask for.
func (t *GateTopology) TerminalFactory(
	ctx context.Context,
	good, systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	result, err := t.markets.FindExportMarket(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("resolving exporter for %s in %s: %w", good, systemSymbol, err)
	}
	if result == nil {
		return nil, fmt.Errorf("no market in %s exports %s", systemSymbol, good)
	}
	return result, nil
}

// FeedTarget resolves the waypoint that IMPORTS good — the only place feedstock for it may
// legally be delivered.
//
// FAILS CLOSED. When no importer can be resolved this returns an error and a nil result, so the
// caller cannot dispatch. This is the sp-b27a2 guard: that incident dispatched IRON_ORE to a
// waypoint which did not import it, and the haulers then sat at 80/80 unable to deliver OR dump
// ("Could not unload IRON_ORE to free cargo space"). Resolving by import capability makes that
// state unreachable rather than merely unlikely — the destination is derived from the good, so
// a destination that cannot accept the good is not expressible here.
//
// The role choice is the whole fix, and it is not interchangeable with TerminalFactory's. An
// exporter SELLS the good and an importer BUYS it; asking the export locator where to deliver
// returns the very kind of waypoint that stranded the fleet.
//
// Refusal reaches this method by two routes. *MarketLocator reports a missing importer as an
// error ("no market found importing X"), so today's production path refuses through the wrap
// below; the nil-result branch is a defensive guard covering the (nil, nil) not-found convention
// that this interface permits and that sibling locators in market_locator.go already use.
//
// Both routes deliberately collapse to one refusal. MarketLocator cannot presently distinguish
// "nothing imports this good" from "the market repo is down", and FeedTarget must refuse on
// either cause — dispatching feedstock during an outage is exactly as harmful as dispatching it
// to a non-importer. The wrapped cause is preserved so an operator can tell the two apart in the
// log; discriminating on it in code is out of scope and tracked separately.
func (t *GateTopology) FeedTarget(
	ctx context.Context,
	good, systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	result, err := t.markets.FindImportMarket(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("resolving importer for %s in %s: %w", good, systemSymbol, err)
	}
	if result == nil {
		return nil, fmt.Errorf("no market in %s imports %s — refusing to dispatch", systemSymbol, good)
	}
	return result, nil
}

// ValidateFeedDestination reports whether destination will take EVERY input in inputs off a hull.
//
// The fabricate path navigates a hauler to the factory that EXPORTS the good being produced, on
// the assumption that the same factory imports that good's inputs. That assumption holds within
// one chain and fails across chains — sp-b27a2: IRON_ORE (FAB_MATS chain) was carried to the
// ADVANCED_CIRCUITRY exporter, which imports nothing from that chain, and the hauler clogged at
// 80/80 unable to deliver or dump.
//
// It reads the DESTINATION'S OWN listing rather than comparing against the best-bid importer that
// FeedTarget resolves. A system can hold several markets importing the same good, and
// FindImportMarket returns only the best bid among them; a factory that legitimately imports the
// input but is not that best bid would be refused, which would park valid fabrication. The
// question here is not "where is the best place to sell this" but "will THIS destination accept
// it", and only the destination's listing answers that.
//
// The predicate is marketBuys — the same one deliverInputs applies on arrival. Sharing it is the
// point: a divergence would be its own stranding, approving a navigate that the delivery step then
// refuses.
//
// The nil handling is now shared too, and used not to be. marketBuys answered TRUE for an
// unreadable listing, on the reasoning that it governs a sell that has already arrived, where
// withholding costs a delivery and spends nothing — while this guard governs a navigate that has
// not happened, where guessing wrong strands a loaded hull at the far end of a system. sp-kdsrh
// closed that divergence: the arrival-side read fails closed as well, because "no basis to judge"
// is not a reason to offer a factory its own EXPORT, which is the one thing the filter exists to
// withhold. Both sides now refuse on an unreadable listing; the two rationales differ, the answer
// no longer does. The nil check below is kept for its NAMED error, which the log needs.
//
// Carrying nothing is not a guess either way — there is no cargo to strand, so an empty input list
// is accepted before the listing is consulted at all.
//
// Returns an error naming the first offending good, so the refusal is diagnosable from the log
// alone rather than requiring a code read.
func (t *GateTopology) ValidateFeedDestination(
	destination *market.Market,
	factoryWaypoint string,
	inputs []string,
) error {
	if len(inputs) == 0 {
		return nil
	}
	if destination == nil {
		return fmt.Errorf("cannot feed %s: its market listing is unreadable, refusing to dispatch %d input(s)", factoryWaypoint, len(inputs))
	}
	for _, input := range inputs {
		if !marketBuys(destination, input) {
			return fmt.Errorf("cannot feed %s to %s: that market does not import it", input, factoryWaypoint)
		}
	}
	return nil
}
