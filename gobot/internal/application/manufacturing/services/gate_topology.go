package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// marketResolver is the narrow view of MarketLocator that GateTopology needs. Declaring it
// here (consumer-side) keeps the seam testable with a small fake instead of a full locator,
// and documents exactly which two role lookups the topology depends on.
// *MarketLocator satisfies this interface.
type marketResolver interface {
	FindExportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
	FindImportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*MarketLocatorResult, error)
	// TradeGoodAt reads a RESOLVED waypoint's own listing for a good. Distinct from the two
	// searches above, which pick a waypoint by role: this one is the only way to ask what a
	// specific already-resolved factory says about a specific good.
	TradeGoodAt(ctx context.Context, waypointSymbol, good string, playerID int) (*market.TradeGood, error)
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
// and both gate materials feed into it.
//
// "Has no recipe" is therefore NOT "is raw". Every ore and crystal in the game HAS a recipe entry
// — they are all {EXPLOSIVES} — so a !hasRecipe test calls none of the actual raw materials raw,
// and a walk keyed on it descends an ore into the cycle above. goods.IsMineableRawMaterial is the
// domain's curated answer to the question this method actually asks.
//
// The no-recipe half is KEPT rather than replaced by the curated list: a good absent from the map
// entirely is still raw. Dropping it would make every unknown or newly-added good look fabricable,
// which is the opposite failure.
//
// The curated list is package-level and deliberately not injected alongside supplyChainMap. Which
// goods are minable is a game constant, not per-instance config; a second seam would only let a
// caller construct a topology whose two halves disagree.
//
// TERMINATION. This predicate is what bottoms out the recursion, and it is NOT sufficient on its
// own: it cuts the loop above at the ore, but the map is data that ships with the game and the
// curated list is hand-maintained, so neither is a proof of acyclicity. A recursive walk built on
// this seam MUST still carry cycle detection, and THE FABRICATE DEPTH CAP MUST NOT BE DELETED ON
// THE ARGUMENT THAT THE RECIPE GRAPH IS AN ACYCLIC DAG — that argument is false, and
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
// NOT INTERCHANGEABLE WITH goods.GetRequiredInputs. Substituting one for the other is a silent
// behaviour change, not a refactor; they differ on BOTH axes:
//
//   - Shape: this returns nil for a raw good, GetRequiredInputs returns []string{}. The len()==0
//     and range idioms are blind to that, so a swap goes unnoticed until something compares to nil.
//   - CONTENT, the sharper hazard. This method treats a curated mineable raw material as raw, so
//     Inputs("IRON_ORE") is nil while GetRequiredInputs("IRON_ORE") is still {"EXPLOSIVES"}. A walk
//     that swapped in GetRequiredInputs would descend an ore into the recipe cycle and never
//     terminate.
//
// GetRequiredInputs is the honest reading of the raw map and answers "what does this recipe list",
// a fabricate-eligibility question. This method answers "what must I still source", which is the
// recursion's question. Neither contract moves.
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
// Both refusal branches are live. *MarketLocator reports a missing exporter as an error; the
// nil-result branch covers the (nil, nil) not-found convention this interface permits and that
// sibling locators in market_locator.go use. Either way the seam fails closed rather than
// returning a waypoint the caller did not ask for.
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
// caller cannot dispatch. Resolving by IMPORT capability makes a hull stranded full at a waypoint
// that cannot accept its cargo unreachable rather than merely unlikely: the destination is derived
// from the good, so a destination that cannot take the good is not expressible here.
//
// The role choice is the whole guarantee and is NOT interchangeable with TerminalFactory's. An
// exporter SELLS the good and an importer BUYS it; asking the export locator where to deliver
// returns exactly the kind of waypoint that strands a loaded hull.
//
// Refusal reaches this method by two routes: *MarketLocator reports a missing importer as an
// error, and the nil-result branch guards the (nil, nil) not-found convention this interface
// permits. Both deliberately collapse to one refusal — MarketLocator cannot distinguish "nothing
// imports this good" from "the market repo is down", and dispatching feedstock during an outage is
// as harmful as dispatching it to a non-importer. The wrapped cause is preserved so an operator
// can tell the two apart in the log.
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
// one chain and FAILS ACROSS CHAINS, where the hull arrives full at a factory that imports nothing
// it carries and can neither deliver nor dump.
//
// It reads the DESTINATION'S OWN listing rather than comparing against the best-bid importer that
// FeedTarget resolves. A system can hold several markets importing the same good and
// FindImportMarket returns only the best bid among them, so a factory that legitimately imports
// the input but is not that best bid would be refused and valid fabrication parked. The question
// is not "where is the best place to sell this" but "will THIS destination accept it".
//
// The predicate is marketBuys — the same one deliverInputs applies on arrival. Sharing it is the
// point: a divergence would be its own stranding, approving a navigate the delivery step refuses.
//
// BOTH SIDES FAIL CLOSED ON AN UNREADABLE LISTING, for different reasons that must not be allowed
// to diverge again: here, guessing wrong strands a loaded hull at the far end of a system; on
// arrival, "no basis to judge" is not a reason to offer a factory its own EXPORT, which is the one
// thing the filter exists to withhold. The nil check below is kept for its NAMED error, which the
// log needs.
//
// Carrying nothing is not a guess either way — there is no cargo to strand, so an empty input list
// is accepted before the listing is consulted at all. The error names the first offending good, so
// a refusal is diagnosable from the log alone.
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

// ImportSupply reports the supply level at which factoryWaypoint IMPORTS good — how short that
// specific factory is of that specific input.
//
// THIS IS A THIRD QUANTITY, and the two nearby ones are both the wrong one. For a feed step
// input -> output:
//
//   - TerminalFactory(input).Supply is the SOURCE market's EXPORT supply of the input — how much
//     the market we buy FROM has. Ranking on it is perverse: it prefers an input precisely BECAUSE
//     its source is scarce, which is backwards on both counts.
//   - TerminalFactory(output).Supply is the destination's EXPORT supply of its OWN output. That is
//     the ABUNDANT fail-safe's subject, and it answers "does this factory need feeding at all",
//     never "which of its inputs is it shortest of".
//
// Only the destination's own IMPORT listing answers the third question, and no searching locator
// can reach it: FindImportMarket returns the system's BEST importer of the good, which is a
// different waypoint whenever this factory is not it.
//
// IMPORT ONLY. An EXPORT listing means the factory makes the good rather than consuming it, where
// a high supply means the opposite of need; EXCHANGE means it merely trades it, where supply is
// not a statement of need at all. Reading either as "how short it is" inverts or invents the
// signal, so both are reported unknown and the caller falls back to its existing order.
//
// UNKNOWN IS A REAL ANSWER, not a failure to be papered over, and the boolean is what keeps it
// honest. An unscanned market, an absent listing and a null supply are all "no basis to rank",
// which must leave the caller's existing order untouched — never sort to the front (prioritising
// exactly the factories we cannot see) nor to the back (starving them). A guard that rejects on
// ABSENCE of evidence deadlocks precisely when the fleet is coldest and nothing has been scanned.
func (t *GateTopology) ImportSupply(ctx context.Context, factoryWaypoint, good string, playerID int) (string, bool) {
	if factoryWaypoint == "" || good == "" {
		return "", false
	}
	listing, err := t.markets.TradeGoodAt(ctx, factoryWaypoint, good, playerID)
	if err != nil || listing == nil {
		return "", false
	}
	if listing.TradeType() != market.TradeTypeImport {
		return "", false
	}
	supply := listing.Supply()
	if supply == nil || !shared.IsValidSupply(*supply) {
		return "", false
	}
	return *supply, true
}

// SourceSupplyAcceptable reports whether good's supply at an ALREADY-RESOLVED construction buy
// source is STILL acceptable, through the SAME TradeGoodAt lookup ImportSupply uses above but
// unrestricted to IMPORT listings, since a buy source is typically an EXPORT.
func (t *GateTopology) SourceSupplyAcceptable(ctx context.Context, waypointSymbol, good string, playerID int) bool {
	supply := supplyModerate
	if listing, err := t.markets.TradeGoodAt(ctx, waypointSymbol, good, playerID); err == nil && listing != nil {
		supply = supplyOrModerate(listing)
	}
	return acceptableSourceSupply(supply, goods.IsMineableRawMaterial(good))
}
