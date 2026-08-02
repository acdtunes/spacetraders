package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// ErrEmptyWhitelist is returned when ScreenSystem is called with no goods to
// look for. It is a caller bug — a config load that failed, or a whitelist not
// yet populated — and NOT a finding about the system, so the screen refuses the
// tick and writes nothing rather than recording a verdict it cannot justify.
var ErrEmptyWhitelist = errors.New("parked-probe screen requires a non-empty goods whitelist")

// screen.go is the whitelist screen: it judges ONE system — does it deal in
// anything we want? — and, when it does, plans the probe placements that would
// watch it. It is the decision half of the parked-probe sensing model; buying
// and dispatching hulls happen elsewhere, off the ledger rows written here.
//
// The ports below are declared consumer-side and deliberately narrow. None of
// them can enumerate the fleet: the screen's cost must scale with the system
// being looked at, not with how many ships we own.

// WaypointCatalog is the system's charted geography.
type WaypointCatalog interface {
	// ListMarketWaypoints returns the CHARTED waypoints in the system carrying a
	// marketplace. Uncharted waypoints are excluded by construction — their
	// traits are unknown until someone charts them — which is what keeps the
	// remote gap fill off waypoints that have never been visited.
	ListMarketWaypoints(ctx context.Context, system string) ([]string, error)
	// ListUnchartedCount reports how many waypoints in the system remain
	// uncharted. Non-zero means the screen has not seen the whole system yet.
	//
	// It counts exactly the set UnchartedCatalog.UnchartedWaypoints hands a seed
	// to visit — the charting tour's completion signal, which verdictFor
	// requires to read zero before it may write a system off durably. The two
	// must never disagree about WHICH waypoints are outstanding; they are free
	// to disagree about the order.
	ListUnchartedCount(ctx context.Context, system string) (int, error)
	// ListProbeYards returns the system's shipyards that sell probes, cheapest
	// first. Resolving "sells probes" — priced inventory, falling back to a bare
	// SHIPYARD trait when nothing has been scanned — belongs to the adapter.
	ListProbeYards(ctx context.Context, system string) ([]string, error)
	// ListHeavyYards returns the system's shipyards that sell HEAVY hulls, cheapest
	// first. A heavy yard earns a quartermaster for the same reason a probe yard does:
	// a parked hull makes a future purchase there instant instead of requiring one to
	// fly in first (spec §6). Used as a FALLBACK behind ListProbeYards — probes are what
	// this engine actually buys, so the probe-priced ordering keeps precedence.
	ListHeavyYards(ctx context.Context, system string) ([]string, error)
	// CatalogKnown reports whether the system's waypoint LIST has ever been
	// swept — whether we know what is in it at all, as distinct from knowing
	// what those waypoints hold.
	//
	// It exists because the other three reads CANNOT tell the difference. A
	// system nobody has ever visited has no waypoint rows, so it yields no
	// markets, no uncharted waypoints and no yards — byte for byte the same
	// answer as a system charted end to end that happens to deal in nothing we
	// want. Without this signal the screen reads absence of evidence as evidence
	// of absence and stamps NO_WHITELIST, which is DURABLE (only PENDING systems
	// are re-screened) and, worse, makes the system a propagation origin for the
	// expansion frontier — so one wrong write-off does not merely lose a system,
	// it walks the mistake outward across the map.
	CatalogKnown(ctx context.Context, system string) (bool, error)
}

// MarketGoodsReader reads what we already know about a market, from the local
// market cache. It is consulted before the API, always.
type MarketGoodsReader interface {
	// GoodsAt returns the goods a market is known to deal in. The bool reports
	// whether the local cache holds ANY row for the waypoint.
	//
	// NOTE the two cases it does NOT distinguish: a market cached as trading
	// nothing and a market never scanned both come back (nil, false). The
	// adapter reads market_data rows, and "no rows" is the same answer either
	// way. So a false here means "the cache cannot answer", never "the cache says
	// there is nothing" — callers that need the difference must resolve it
	// elsewhere (screenMarkets falls through to the slot projection, then the
	// API).
	GoodsAt(ctx context.Context, playerID int, waypoint string) ([]string, bool, error)
	// DepthRowsAt returns the priced rows behind those goods — volume and
	// mid-price per good. Only a scanned market has them; the goods list and the
	// prices are separate reads because a market can be known without ever
	// having been priced.
	DepthRowsAt(ctx context.Context, playerID int, waypoint string) ([]scouting.MarketDepthRow, error)
}

// RemoteMarketFetcher fills a cache gap from the API. Charting-priority work:
// it reveals a charted market's goods without sending a ship there.
type RemoteMarketFetcher interface {
	FetchGoods(ctx context.Context, playerID int, system, waypoint string) ([]string, error)
}

// ExistingSlot is a placement the ledger already holds, carrying what was
// recorded when the placement was made.
//
// WhitelistGoods is a PROJECTION, not an inventory: it is the intersection of
// the waypoint's goods with the whitelist AS OF the moment the slot was written,
// so it names what we wanted there — never everything the market deals in. Empty
// or nil therefore means "no whitelisted goods are known at this waypoint",
// which is exactly what a YARD slot records, and what a market that matched
// nothing would record. screenMarkets treats that as an AUTHORITATIVE answer
// rather than a gap, so an empty projection suppresses the API call the same way
// a populated one does.
//
// That behaviour is PINNED by TestScreenSystemTreatsEmptyProjectionAsAuthoritative,
// and it is why `spacetraders sensing rescreen` re-opens VERDICTS only
// and leaves this column alone: blanking it here would not re-open the question,
// it would answer it wrongly and permanently — nothing rewrites an existing
// slot's projection (recordSlots skips waypoints already held), an emptied
// projection suppresses the very refetch that would repopulate it, and the scan
// rotation would stop observing spread at that waypoint entirely (Scanner.observe
// skips a slot with no whitelist). Re-opening never-scanned markets is therefore
// a design change against a reviewed decision, tracked as sp-ysg8h.
type ExistingSlot struct {
	Waypoint       string
	WhitelistGoods []string
	DepthCredits   int64
}

// SlotLedger is the durable side of the screen — the placement ledger the whole
// model is re-derived from after a restart.
type SlotLedger interface {
	// ExistingSlots returns the placements the system already holds, in ANY
	// state. It serves two purposes: the screen writes only the placements
	// missing from this set, and it reads back the recorded goods instead of
	// paying the API to rediscover them.
	ExistingSlots(ctx context.Context, playerID int, system string) ([]ExistingSlot, error)
	// UpsertSlotMetadata declares one placement the screen wants. On a waypoint
	// that already carries a placement it refreshes the screen's own columns
	// only — the screen plans WANTS, and must never write its empty hull over a
	// placement someone else has already filled.
	UpsertSlotMetadata(ctx context.Context, playerID int, slot SlotRecord) error
	// UpsertSystem records the screening verdict.
	UpsertSystem(ctx context.Context, playerID int, record SystemRecord) error
}

// ScreenPorts is everything ScreenSystem needs from the outside world.
type ScreenPorts struct {
	Waypoints    WaypointCatalog
	MarketGoods  MarketGoodsReader
	RemoteMarket RemoteMarketFetcher
	Ledger       SlotLedger
}

// PlannedSlot is one probe placement the screen wants.
type PlannedSlot struct {
	Waypoint, Kind string
	// WhitelistGoods are the whitelisted goods this waypoint deals in, sorted.
	// Empty for a YARD slot, which is placed for the shipyard, not the market.
	WhitelistGoods []string
	// DepthCredits is Σ trade_volume × mid-price over WhitelistGoods — a size
	// estimate used to order placements, never to decide them. Zero for a market
	// whose goods we know but whose prices we have never seen.
	DepthCredits int64
}

// ScreenResult is the screen's decision about one system.
type ScreenResult struct {
	Verdict        string
	Slots          []PlannedSlot
	UnchartedCount int
}

// SlotRecord is a placement as handed to the ledger.
type SlotRecord struct {
	Waypoint       string
	System         string
	Kind           string
	State          string
	WhitelistGoods []string
	DepthCredits   int64
	// AssignedShip is the hull already filling this placement. The screen never
	// sets it — it plans WANTS, which by definition have no hull behind them —
	// and leaves it empty so the column is written NULL.
	//
	// The expansion engine does set it, in the one case where a placement is
	// born already filled: a finished charting seed standing itself down as a
	// PARKED SPARE. Writing that row without the hull would record a parked
	// probe the ledger cannot see, dropping it out of the probe-cap count and
	// authorising the purchase of a replacement we already own.
	AssignedShip string
}

// SystemRecord is a screening verdict as handed to the ledger.
type SystemRecord struct {
	System         string
	Verdict        string
	UnchartedCount int
	DepthCredits   int64
	// CatalogKnown records that the system's waypoint list was known AT THE TIME
	// OF THIS SCREEN. It is written back so the expansion engine can read it off
	// the row for free instead of asking the catalog again per system per tick —
	// and, more usefully, so a system the fleet swept long before this model
	// existed is recognised as known the first time it is screened, rather than
	// being sent a charting seed to rediscover what is already in the database.
	CatalogKnown bool
}

// screenedMarket is one market that survived the whitelist.
type screenedMarket struct {
	waypoint string
	goods    []string
	depth    int64
}

// ScreenSystem judges one system against the goods whitelist and plans the probe
// placements for it, recording both in the ledger.
//
// The verdict turns on what is KNOWN, not on what has been looked at:
//
//   - IN_SCOPE as soon as ONE market is known to deal in a whitelisted good,
//     even with waypoints still uncharted. Waiting for the whole system to be
//     charted would idle a probe we could already buy for a market we can
//     already see, so charting and placement run in parallel.
//   - NO_WHITELIST only when the system is charted through AND every market
//     resolved AND none of them matched. This verdict is durable and stops
//     further work, so it is never recorded on incomplete information.
//   - PENDING otherwise — undecided, come back later.
//
// Placements are planned only for an IN_SCOPE system: a probe parked in a system
// we have rejected, or not yet judged, is a hull bought for nothing.
func ScreenSystem(
	ctx context.Context,
	p ScreenPorts,
	playerID int,
	system string,
	whitelist map[string]bool,
) (ScreenResult, error) {
	// Refuse the tick outright rather than screening against nothing. Every
	// market would fail to match, the system would be stamped NO_WHITELIST, and
	// that verdict is DURABLE — only PENDING systems are ever re-screened — so a
	// whitelist that was briefly empty (a config reload, a startup race) would
	// permanently write off systems it never actually judged. Fail loud.
	if len(whitelist) == 0 {
		return ScreenResult{}, fmt.Errorf("screening %q: %w", system, ErrEmptyWhitelist)
	}

	markets, err := p.Waypoints.ListMarketWaypoints(ctx, system)
	if err != nil {
		return ScreenResult{}, fmt.Errorf("failed to list market waypoints in %q: %w", system, err)
	}
	unchartedCount, err := p.Waypoints.ListUnchartedCount(ctx, system)
	if err != nil {
		return ScreenResult{}, fmt.Errorf("failed to count uncharted waypoints in %q: %w", system, err)
	}
	catalogKnown, err := p.Waypoints.CatalogKnown(ctx, system)
	if err != nil {
		// Fail the screen rather than guess. Guessing TRUE writes off unexplored
		// systems durably; guessing FALSE would be safe here but would leave the
		// caller unable to tell a real PENDING from an unreadable one.
		return ScreenResult{}, fmt.Errorf("failed to read whether the waypoint catalog of %q is known: %w", system, err)
	}

	existing, err := existingByWaypoint(ctx, p, playerID, system)
	if err != nil {
		return ScreenResult{}, err
	}

	hits, allResolved, err := screenMarkets(ctx, p, playerID, system, markets, whitelist, existing)
	if err != nil {
		return ScreenResult{}, err
	}

	result := ScreenResult{
		Verdict:        verdictFor(len(hits) > 0, allResolved, catalogKnown, unchartedCount),
		UnchartedCount: unchartedCount,
	}
	var systemDepth int64
	for _, hit := range hits {
		systemDepth += hit.depth
	}

	if result.Verdict == VerdictInScope {
		result.Slots, err = planSlots(ctx, p, system, hits)
		if err != nil {
			return ScreenResult{}, err
		}
		if err := recordSlots(ctx, p, playerID, system, result.Slots, existing); err != nil {
			return ScreenResult{}, err
		}
	}

	if err := p.Ledger.UpsertSystem(ctx, playerID, SystemRecord{
		System:         system,
		Verdict:        result.Verdict,
		UnchartedCount: unchartedCount,
		DepthCredits:   systemDepth,
		CatalogKnown:   catalogKnown,
	}); err != nil {
		return ScreenResult{}, fmt.Errorf("failed to record screening verdict for %q: %w", system, err)
	}
	return result, nil
}

// screenMarkets resolves every charted market's goods and keeps the ones dealing
// in something whitelisted. Three sources, cheapest first:
//
//  1. market_data, via GoodsAt — free, but only populated once a ship has been
//     present at the waypoint;
//  2. the slot row, for a waypoint we have already placed — also free, and the
//     reason a re-screen costs no API calls (see below);
//  3. the API, via FetchGoods — the only genuine gap fill, and the only one that
//     costs anything.
//
// Source 2 is what makes the slot row a CACHE. Without it a waypoint discovered
// remotely would be re-fetched on every screen of the system: market_data stays
// empty until a probe actually parks there, so GoodsAt keeps reporting a gap
// long after we have learned and durably recorded the answer. Reading the slot's
// recorded goods closes that loop. It must SUPPLY the goods, not merely suppress
// the fetch — an empty goods list would drop the waypoint out of the hit set and
// take its own slot out of the plan.
//
// The second return reports whether EVERY market resolved: a market we failed to
// read is not a market that deals in nothing, and must not harden into a
// rejection.
func screenMarkets(
	ctx context.Context,
	p ScreenPorts,
	playerID int,
	system string,
	markets []string,
	whitelist map[string]bool,
	existing map[string]ExistingSlot,
) ([]screenedMarket, bool, error) {
	sorted := append([]string(nil), markets...)
	sort.Strings(sorted)

	hits := make([]screenedMarket, 0, len(sorted))
	allResolved := true
	for _, waypoint := range sorted {
		goods, known, err := p.MarketGoods.GoodsAt(ctx, playerID, waypoint)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read known goods at %q: %w", waypoint, err)
		}

		// recorded is the slot's own memory of this waypoint, used only when
		// market_data has nothing — it carries the goods AND the depth measured
		// when the placement was made.
		//
		// What it carries is a whitelist PROJECTION (see ExistingSlot), and
		// caching a projection is sound only because the whitelist is invariant
		// within an era: under that axiom an empty projection means "nothing we
		// want is here", which a refetch would only confirm at the cost of an
		// API call. A whitelist edited MID-era breaks the axiom.
		//
		// The operator response is `spacetraders sensing rescreen`,
		// which re-opens every VERDICT so the sweep re-judges under the new list.
		// That fixes every market the cache can answer for — GoodsAt is consulted
		// FIRST, so a market any probe has scanned never reaches this branch at
		// all. What it does NOT fix is this branch: a never-scanned market is
		// still judged from its stored projection, and the rescreen cannot clear
		// that projection because the clear would be permanent (recordSlots skips
		// waypoints that already hold a slot) and self-suppressing (the test
		// below is on the slot EXISTING, not on its projection being populated, so
		// an emptied projection would stop the refetch that would repopulate it).
		// Closing that gap needs this branch and recordSlots changed together —
		// and note that the CURRENT behaviour is deliberate and PINNED by
		// TestScreenSystemTreatsEmptyProjectionAsAuthoritative, so changing it is
		// a design decision rather than a bug fix. Tracked as sp-ysg8h.
		//
		// It is also self-limiting: once a probe parks at the waypoint and scans
		// it, market_data answers GoodsAt and this branch stops governing that
		// market — for as long as market_data holds rows for it.
		var recorded *ExistingSlot
		if !known {
			if slot, ok := existing[waypoint]; ok {
				recorded = &slot
				goods = slot.WhitelistGoods
			} else {
				goods, err = p.RemoteMarket.FetchGoods(ctx, playerID, system, waypoint)
				if err != nil {
					// Opportunistic: one unreadable market leaves the system
					// undecided rather than failing the whole screen.
					allResolved = false
					continue
				}
			}
		}

		matched := matchWhitelist(goods, whitelist)
		if len(matched) == 0 {
			continue
		}

		// Depth comes from the priced rows, which only a scanned market has. A
		// market known only remotely is deliberately left at 0 — a blind prior
		// that orders it last, not a claim that it is worthless — except where
		// the slot row already carries a measured value, which is kept.
		var depth int64
		switch {
		case known:
			rows, err := p.MarketGoods.DepthRowsAt(ctx, playerID, waypoint)
			if err != nil {
				return nil, false, fmt.Errorf("failed to read market depth at %q: %w", waypoint, err)
			}
			depth = depthOf(rows, whitelist)
		case recorded != nil:
			depth = recorded.DepthCredits
		}
		hits = append(hits, screenedMarket{waypoint: waypoint, goods: matched, depth: depth})
	}
	return hits, allResolved, nil
}

// existingByWaypoint loads the system's current placements, keyed by waypoint.
func existingByWaypoint(ctx context.Context, p ScreenPorts, playerID int, system string) (map[string]ExistingSlot, error) {
	slots, err := p.Ledger.ExistingSlots(ctx, playerID, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing sensing slots in %q: %w", system, err)
	}
	byWaypoint := make(map[string]ExistingSlot, len(slots))
	for _, slot := range slots {
		byWaypoint[slot.Waypoint] = slot
	}
	return byWaypoint, nil
}

// verdictFor applies the three-way rule. Rejection is the only DURABLE verdict,
// so it demands three separate proofs that we actually looked: the system's
// waypoint list is known at all, nothing in it is still uncharted, and every
// market in it resolved. Any one missing leaves the system PENDING.
//
// catalogKnown is the proof that is easiest to omit and worst to get wrong. An
// unswept system produces the same empty readings as a thoroughly-examined
// barren one, and NO_WHITELIST systems are propagation origins for the expansion
// frontier — so writing one off on absent evidence does not just lose that
// system, it seeds the frontier from a place we never looked at and carries the
// mistake outward.
func verdictFor(hasMatch, allResolved, catalogKnown bool, unchartedCount int) string {
	switch {
	case hasMatch:
		return VerdictInScope
	case catalogKnown && unchartedCount == 0 && allResolved:
		return VerdictNoWhitelist
	default:
		return VerdictPending
	}
}

// planSlots turns the surviving markets into placements: one MARKET slot each,
// plus a YARD slot for EVERY shipyard in the system that no market slot already
// covers.
func planSlots(ctx context.Context, p ScreenPorts, system string, hits []screenedMarket) ([]PlannedSlot, error) {
	slots := make([]PlannedSlot, 0, len(hits)+1)
	placed := make(map[string]bool, len(hits))
	for _, hit := range hits {
		slots = append(slots, PlannedSlot{
			Waypoint:       hit.waypoint,
			Kind:           SlotKindMarket,
			WhitelistGoods: hit.goods,
			DepthCredits:   hit.depth,
		})
		placed[hit.waypoint] = true
	}

	probeYards, err := p.Waypoints.ListProbeYards(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	// EVERY yard the system offers, and only where nothing is placed there
	// already: the ledger holds one slot per waypoint, so a second slot on the
	// same waypoint would overwrite the first rather than place another probe.
	//
	// EVERY yard rather than the cheapest one, because buying ANY hull requires a
	// ship of ours already standing at that shipyard. A yard we never place at is
	// a counter we can never buy from, however cheap it is — and seed probes, the
	// hulls this engine explores with, can only be ordered at a staffed
	// probe-selling yard. Slotting only the first leaves every other shipyard in the
	// system permanently unbuyable, which is what capped exploration.
	//
	// `placed` is CARRIED THROUGH this loop, not just read from it: it is what
	// keeps the one-slot-per-waypoint invariant when a waypoint appears twice in
	// the yard list, and it is why the loop cannot be collapsed into an append.
	//
	// CONTRACT — probe presence at a yard is WAYPOINT-wise, never KIND-wise.
	// When a probe-selling yard is also a whitelisted market, the MARKET slot
	// wins (a YARD slot would have to drop the goods list, losing the reason the
	// waypoint is worth watching at all), so the yard ends up covered by a slot
	// whose kind is MARKET. A consumer asking "do we have a probe at this yard?"
	// MUST therefore match on waypoint + PARKED and ignore slot_kind entirely —
	// filtering for kind == YARD would miss the probe standing right there and
	// buy a second one for the same waypoint.
	// Heavy-selling yards earn a quartermaster too (spec §6): a parked probe makes a
	// future HEAVY purchase there instant instead of requiring a hull to fly in first.
	//
	// PRECEDENCE, NOT EXCLUSION. The heavy list is appended BEHIND the probe list, so
	// the probe-priced cheapest-first ordering above keeps its precedence untouched —
	// probes are what this engine actually buys, and a probe yard is still the first
	// placement the system offers. The heavy list is nonetheless
	// CONSULTED ALWAYS. Reading it only when the system offers no probe yard
	// at all turns "probes come first" from an ordering
	// claim into an exclusion, so a system that sells both never
	// looks. Measured live: X1-QR78-AE4F sells probes AND heavy freighters, so
	// X1-QR78 never consulted its heavy list, and its SECOND heavy yard X1-QR78-FE8C
	// — which sells no probe — was invisible as a heavy yard entirely. The engine was
	// hunting a SHIP_HEAVY_FREIGHTER at the time.
	//
	// The `placed` map below is what makes the concatenation safe: a waypoint selling
	// both classes appears in both lists and still claims exactly one slot, in its
	// probe-priced position.
	//
	// The joined list is built into a FRESH slice rather than appended onto the one
	// the port handed back. A port's return is the adapter's own buffer, and appending
	// into its spare capacity would write through into whatever the adapter still
	// holds — a defect that only appears once an adapter starts reusing its slice, and
	// then appears as a corrupted yard list rather than as a build failure.
	heavyYards, err := p.Waypoints.ListHeavyYards(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list heavy yards in %q: %w", system, err)
	}
	yards := make([]string, 0, len(probeYards)+len(heavyYards))
	yards = append(yards, probeYards...)
	yards = append(yards, heavyYards...)
	for _, yard := range yards {
		if placed[yard] {
			continue
		}
		slots = append(slots, PlannedSlot{Waypoint: yard, Kind: SlotKindYard})
		placed[yard] = true
	}
	return slots, nil
}

// recordSlots writes the placements the ledger does not have yet. Waypoints that
// already carry a slot are left ALONE: their row holds the live state and the
// hull filling it, and rewriting one back to WANTED would drop that hull out of
// the probe-cap count and authorise buying a replacement we already own.
//
// The skip is total — it freezes whitelist_goods and depth_credits at their
// recorded values too, and that is DELIBERATE, not an oversight:
//
//   - Goods do not go stale in any way that matters. What a market DEALS IN is
//     stable; it is the prices that move, and no part of this row holds prices.
//   - Depth only orders placements that have not been filled yet. Once a probe
//     is bought for a slot, the ordering has already done its work, so a fresher
//     number would change nothing.
//   - The one case where a refreshed depth would be interesting — a slot
//     carrying the blind 0 prior, because we learned of it without prices —
//     resolves itself without this path. Depth needs prices and prices need
//     presence, so the number can only improve once a probe is actually parked
//     there; and by then its scans have populated market_data, GoodsAt reports
//     the waypoint as known, and screenMarkets computes depth FRESH from those
//     prices without ever consulting the frozen row. The recorded value is read
//     only while no prices exist — precisely when there is nothing better to
//     record.
//
// So a metadata-refresh path here would be code that cannot improve on what the
// live path already produces. If that ever changes, the write to reach for is
// UpsertSlotMetadata, which refreshes goods and depth and leaves the placement's
// state and hull to the writers that own them (sp-wgjb7). Dropping the skip below
// would therefore no longer corrupt a filled placement — it would just spend
// writes re-asserting what the row already says.
//
// Note that re-screening is NOT confined to systems without placements: a
// PENDING system can already hold slots when it is re-screened, because a seed
// reading writes WANTED slots directly while the system's row still says PENDING
// (the seed records no verdict; the next screen does). Re-screening is BATCHED —
// the coordinator sweeps at most five PENDING systems per reconcile tick, so a
// given system is screened whenever it makes the batch, not on every tick.
// That is the live path this skip and the slot cache are both built for — the
// seed-measured goods flow back through the cache and let the verdict reach
// IN_SCOPE with no API call. Once a system is IN_SCOPE it is never re-screened.
func recordSlots(
	ctx context.Context,
	p ScreenPorts,
	playerID int,
	system string,
	slots []PlannedSlot,
	existing map[string]ExistingSlot,
) error {
	for _, slot := range slots {
		if _, held := existing[slot.Waypoint]; held {
			continue
		}
		if err := p.Ledger.UpsertSlotMetadata(ctx, playerID, SlotRecord{
			Waypoint:       slot.Waypoint,
			System:         system,
			Kind:           slot.Kind,
			State:          SlotStateWanted,
			WhitelistGoods: slot.WhitelistGoods,
			DepthCredits:   slot.DepthCredits,
		}); err != nil {
			return fmt.Errorf("failed to record sensing slot at %q: %w", slot.Waypoint, err)
		}
	}
	return nil
}

// matchWhitelist returns the whitelisted goods among goods, deduped and sorted.
// An empty whitelist matches NOTHING — "we want nothing" must never be read as
// "we want everything" and slot an entire system.
func matchWhitelist(goods []string, whitelist map[string]bool) []string {
	if len(whitelist) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(goods))
	matched := make([]string, 0, len(goods))
	for _, good := range goods {
		if !whitelist[good] || seen[good] {
			continue
		}
		seen[good] = true
		matched = append(matched, good)
	}
	if len(matched) == 0 {
		return nil
	}
	sort.Strings(matched)
	return matched
}

// depthOf sums trade_volume × mid-price over the whitelisted goods, matching the
// sensing depth convention in domain/scouting. Non-positive volumes or prices
// contribute nothing: garbage fails closed rather than inflating a placement's
// apparent size.
func depthOf(rows []scouting.MarketDepthRow, whitelist map[string]bool) int64 {
	var depth int64
	for _, row := range rows {
		if !whitelist[row.Good] || row.TradeVolume <= 0 || row.MidPrice <= 0 {
			continue
		}
		depth += int64(row.TradeVolume) * int64(row.MidPrice)
	}
	return depth
}
