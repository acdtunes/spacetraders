package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// ErrEmptyWhitelist is returned when ScreenSystem is called with no goods to look
// for. It is a caller bug and NOT a finding about the system, so the screen refuses
// the tick and writes nothing rather than recording a verdict it cannot justify.
var ErrEmptyWhitelist = errors.New("parked-probe screen requires a non-empty goods whitelist")

// screen.go is the whitelist screen: it judges ONE system — does it deal in
// anything we want? — and, when it does, plans the probe placements that would
// watch it. Buying and dispatching hulls happen elsewhere, off the ledger rows
// written here.
//
// The ports below are declared consumer-side and deliberately narrow. None of them
// can enumerate the fleet: the screen's cost must scale with the system being
// looked at, not with how many ships we own.

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
	// It counts exactly the set UnchartedCatalog.UnchartedStops hands a seed
	// to visit, and verdictFor requires it to read zero before writing a system
	// off durably. The two must never disagree about WHICH waypoints are
	// outstanding; they are free to disagree about the order.
	ListUnchartedCount(ctx context.Context, system string) (int, error)
	// ListProbeYards returns the system's shipyards that sell probes, cheapest
	// first. Resolving "sells probes" — priced inventory, falling back to a bare
	// SHIPYARD trait when nothing has been scanned — belongs to the adapter.
	ListProbeYards(ctx context.Context, system string) ([]string, error)
	// ListHeavyYards returns the system's shipyards that sell HEAVY hulls, cheapest
	// first. A heavy yard earns a quartermaster for the same reason a probe yard
	// does: a parked hull makes a future purchase there instant instead of
	// requiring one to fly in first (spec §6). Ranked BEHIND ListProbeYards —
	// probes are what this engine actually buys, so the probe-priced ordering
	// keeps precedence.
	ListHeavyYards(ctx context.Context, system string) ([]string, error)
	// CatalogKnown reports whether the system's waypoint LIST has ever been
	// swept — whether we know what is in it at all, as distinct from knowing
	// what those waypoints hold.
	//
	// It exists because the other three reads CANNOT tell the difference: a
	// system nobody has ever visited has no waypoint rows, so it yields no
	// markets, no uncharted waypoints and no yards — byte for byte the same
	// answer as a system charted end to end that deals in nothing we want.
	// Without this signal the screen reads absence of evidence as evidence of
	// absence and stamps NO_WHITELIST, which is DURABLE (only PENDING systems are
	// re-screened) and makes the system a propagation origin for the expansion
	// frontier, walking the mistake outward across the map.
	CatalogKnown(ctx context.Context, system string) (bool, error)
}

// MarketGoodsReader reads what we already know about a market, from the local
// market cache. It is consulted before the API, always.
type MarketGoodsReader interface {
	// GoodsAt returns the goods a market is known to deal in. The bool reports
	// whether the local cache holds ANY row for the waypoint.
	//
	// It does NOT distinguish a market cached as trading nothing from a market
	// never scanned: both come back (nil, false), because the adapter reads
	// market_data rows and "no rows" is the same answer either way. A false here
	// means "the cache cannot answer", never "the cache says there is nothing" —
	// callers needing the difference must resolve it elsewhere (screenMarkets
	// falls through to the slot projection, then the API).
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
// WhitelistGoods is a PROJECTION, not an inventory: it is the intersection of the
// waypoint's goods with the whitelist AS OF the moment the slot was written, so it
// names what we wanted there — never everything the market deals in. Empty or nil
// therefore means "no whitelisted goods are known at this waypoint", which is
// exactly what a YARD slot records and what a market that matched nothing records.
// screenMarkets treats that as an AUTHORITATIVE answer rather than a gap, so an
// empty projection suppresses the API call the same way a populated one does
// (pinned by TestScreenSystemTreatsEmptyProjectionAsAuthoritative).
//
// That is why `spacetraders sensing rescreen` re-opens VERDICTS only and leaves
// this column alone: blanking it would answer the question wrongly and
// permanently, because nothing rewrites an existing slot's projection (recordSlots
// skips waypoints already held), an emptied projection suppresses the very refetch
// that would repopulate it, and the scan rotation would stop observing spread at
// that waypoint entirely (Scanner.observe skips a slot with no whitelist).
type ExistingSlot struct {
	Waypoint       string
	WhitelistGoods []string
	DepthCredits   int64
}

// SlotLedger is the durable side of the screen — the placement ledger the whole
// model is re-derived from after a restart.
type SlotLedger interface {
	// ExistingSlots returns the placements the system already holds, in ANY state.
	// The screen writes only the placements missing from this set, and reads back
	// the recorded goods instead of paying the API to rediscover them.
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
	// sets it — it plans WANTS, which by definition have no hull behind them — and
	// leaves it empty so the column is written NULL.
	//
	// The expansion engine does set it, in the one case where a placement is born
	// already filled: a finished charting seed standing itself down as a PARKED
	// SPARE. Writing that row without the hull would record a parked probe the
	// ledger cannot see, dropping it out of the probe-cap count and authorising the
	// purchase of a replacement we already own.
	AssignedShip string
}

// SystemRecord is a screening verdict as handed to the ledger.
type SystemRecord struct {
	System         string
	Verdict        string
	UnchartedCount int
	DepthCredits   int64
	// CatalogKnown records that the system's waypoint list was known AT THE TIME OF
	// THIS SCREEN. It is written back so the expansion engine reads it off the row
	// instead of asking the catalog again per system per tick, and so a system the
	// fleet swept before it held a sensing row is recognised as known the first
	// time it is screened rather than being sent a charting seed to rediscover
	// what the database already holds.
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
//   - IN_SCOPE as soon as ONE market is known to deal in a whitelisted good, even
//     with waypoints still uncharted. Waiting for the whole system to be charted
//     would idle a probe we could already buy for a market we can already see, so
//     charting and placement run in parallel.
//   - NO_WHITELIST only when the system is charted through AND every market
//     resolved AND none of them matched. This verdict is durable and stops further
//     work, so it is never recorded on incomplete information.
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
	// market would fail to match and the system would be stamped NO_WHITELIST,
	// which is DURABLE — only PENDING systems are ever re-screened — so a
	// briefly-empty whitelist would permanently write off systems it never judged.
	if len(whitelist) == 0 {
		return ScreenResult{}, fmt.Errorf("screening %q: %w", system, ErrEmptyWhitelist)
	}

	s := &screenPass{p: p, playerID: playerID, system: system, whitelist: whitelist}
	catalog, err := s.readSystemCatalog(ctx)
	if err != nil {
		return ScreenResult{}, err
	}

	s.existing, err = s.existingByWaypoint(ctx)
	if err != nil {
		return ScreenResult{}, err
	}

	hits, allResolved, err := s.screenMarkets(ctx, catalog.markets)
	if err != nil {
		return ScreenResult{}, err
	}

	result := ScreenResult{
		Verdict:        verdictFor(len(hits) > 0, allResolved, catalog.known, catalog.unchartedCount),
		UnchartedCount: catalog.unchartedCount,
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
		if err := s.recordSlots(ctx, result.Slots); err != nil {
			return ScreenResult{}, err
		}
	}

	if err := p.Ledger.UpsertSystem(ctx, playerID, SystemRecord{
		System:         system,
		Verdict:        result.Verdict,
		UnchartedCount: catalog.unchartedCount,
		DepthCredits:   systemDepth,
		CatalogKnown:   catalog.known,
	}); err != nil {
		return ScreenResult{}, fmt.Errorf("failed to record screening verdict for %q: %w", system, err)
	}
	return result, nil
}

type systemCatalog struct {
	markets        []string
	unchartedCount int
	known          bool
}

func (s *screenPass) readSystemCatalog(ctx context.Context) (systemCatalog, error) {
	markets, err := s.p.Waypoints.ListMarketWaypoints(ctx, s.system)
	if err != nil {
		return systemCatalog{}, fmt.Errorf("failed to list market waypoints in %q: %w", s.system, err)
	}
	unchartedCount, err := s.p.Waypoints.ListUnchartedCount(ctx, s.system)
	if err != nil {
		return systemCatalog{}, fmt.Errorf("failed to count uncharted waypoints in %q: %w", s.system, err)
	}
	known, err := s.p.Waypoints.CatalogKnown(ctx, s.system)
	if err != nil {
		// Fail the screen rather than guess. Guessing TRUE writes off unexplored
		// systems durably; guessing FALSE would be safe here but would leave the
		// caller unable to tell a real PENDING from an unreadable one.
		return systemCatalog{}, fmt.Errorf("failed to read whether the waypoint catalog of %q is known: %w", s.system, err)
	}
	return systemCatalog{markets: markets, unchartedCount: unchartedCount, known: known}, nil
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
// Source 2 is what makes the slot row a CACHE. market_data stays empty until a
// probe actually parks at a waypoint, so GoodsAt keeps reporting a gap long after
// a remotely-discovered market has been learned and durably recorded. The slot row
// must SUPPLY the goods, not merely suppress the fetch — an empty goods list would
// drop the waypoint out of the hit set and take its own slot out of the plan.
//
// The second return reports whether EVERY market resolved: a market we failed to
// read is not a market that deals in nothing, and must not harden into a
// rejection.
func (s *screenPass) screenMarkets(ctx context.Context, markets []string) ([]screenedMarket, bool, error) {
	sorted := append([]string(nil), markets...)
	sort.Strings(sorted)

	hits := make([]screenedMarket, 0, len(sorted))
	allResolved := true
	for _, waypoint := range sorted {
		goods, known, err := s.p.MarketGoods.GoodsAt(ctx, s.playerID, waypoint)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read known goods at %q: %w", waypoint, err)
		}

		var recorded *ExistingSlot
		if !known {
			var resolved bool
			goods, recorded, resolved = s.goodsWithoutMarketData(ctx, waypoint)
			if !resolved {
				// Opportunistic: one unreadable market leaves the system
				// undecided rather than failing the whole screen.
				allResolved = false
				continue
			}
		}

		matched := matchWhitelist(goods, s.whitelist)
		if len(matched) == 0 {
			continue
		}

		depth, err := s.marketDepth(ctx, waypoint, known, recorded)
		if err != nil {
			return nil, false, err
		}
		hits = append(hits, screenedMarket{waypoint: waypoint, goods: matched, depth: depth})
	}
	return hits, allResolved, nil
}

// goodsWithoutMarketData answers what a market deals in when market_data holds
// nothing for it: from the slot row we already wrote, or failing that from the API.
// The third return is false when the API read failed.
//
// The slot's recorded goods are a whitelist PROJECTION (see ExistingSlot), and
// caching a projection is sound only because the whitelist is invariant within an
// era: under that axiom an empty projection means "nothing we want is here", which
// a refetch would only confirm at the cost of an API call. A whitelist edited
// MID-era breaks the axiom.
//
// The operator response to that is `spacetraders sensing rescreen`, which re-opens
// every VERDICT so the sweep re-judges under the new list. It fixes every market
// the cache can answer for, because GoodsAt is consulted FIRST and a market any
// probe has scanned never reaches this branch. It does NOT fix a never-scanned
// market, which is still judged from its stored projection — see ExistingSlot for
// why the rescreen cannot simply clear that column, and note the test below is on
// the slot EXISTING rather than on its projection being populated.
//
// This branch is self-limiting: once a probe parks at the waypoint and scans it,
// market_data answers GoodsAt and stops routing that market here.
func (s *screenPass) goodsWithoutMarketData(ctx context.Context, waypoint string) ([]string, *ExistingSlot, bool) {
	if slot, ok := s.existing[waypoint]; ok {
		return slot.WhitelistGoods, &slot, true
	}
	goods, err := s.p.RemoteMarket.FetchGoods(ctx, s.playerID, s.system, waypoint)
	if err != nil {
		return nil, nil, false
	}
	return goods, nil, true
}

// marketDepth prices a market from its priced rows, which only a scanned market
// has. A market known only remotely is deliberately left at 0 — a blind prior that
// orders it last, not a claim that it is worthless — except where the slot row
// already carries a measured value, which is kept.
func (s *screenPass) marketDepth(ctx context.Context, waypoint string, known bool, recorded *ExistingSlot) (int64, error) {
	switch {
	case known:
		rows, err := s.p.MarketGoods.DepthRowsAt(ctx, s.playerID, waypoint)
		if err != nil {
			return 0, fmt.Errorf("failed to read market depth at %q: %w", waypoint, err)
		}
		return depthOf(rows, s.whitelist), nil
	case recorded != nil:
		return recorded.DepthCredits, nil
	}
	return 0, nil
}

// existingByWaypoint loads the system's current placements, keyed by waypoint.
func (s *screenPass) existingByWaypoint(ctx context.Context) (map[string]ExistingSlot, error) {
	slots, err := s.p.Ledger.ExistingSlots(ctx, s.playerID, s.system)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing sensing slots in %q: %w", s.system, err)
	}
	byWaypoint := make(map[string]ExistingSlot, len(slots))
	for _, slot := range slots {
		byWaypoint[slot.Waypoint] = slot
	}
	return byWaypoint, nil
}

// verdictFor applies the three-way rule. Rejection is the only DURABLE verdict, so
// it demands three separate proofs that we actually looked: the system's waypoint
// list is known at all, nothing in it is still uncharted, and every market in it
// resolved. Any one missing leaves the system PENDING.
//
// catalogKnown is the proof easiest to omit and worst to get wrong: an unswept
// system produces the same empty readings as a thoroughly-examined barren one, and
// NO_WHITELIST systems are propagation origins for the expansion frontier, so a
// write-off on absent evidence seeds the frontier from a place we never looked at.
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
	// probe-selling yard.
	//
	// `placed` is CARRIED THROUGH this loop, not just read from it: it keeps the
	// one-slot-per-waypoint invariant when a waypoint appears twice in the yard
	// list, which is why the loop cannot be collapsed into an append and what makes
	// concatenating the two yard lists safe.
	//
	// CONTRACT — probe presence at a yard is WAYPOINT-wise, never KIND-wise. When a
	// probe-selling yard is also a whitelisted market the MARKET slot wins (a YARD
	// slot would have to drop the goods list, losing the reason the waypoint is
	// worth watching at all), so the yard ends up covered by a slot whose kind is
	// MARKET. A consumer asking "do we have a probe at this yard?" MUST therefore
	// match on waypoint + PARKED and ignore slot_kind entirely — filtering for
	// kind == YARD would miss the probe standing right there and buy a second one.
	//
	// PRECEDENCE, NOT EXCLUSION. Heavy-selling yards earn a quartermaster too (spec
	// §6), and the heavy list is appended BEHIND the probe list so the probe-priced
	// cheapest-first ordering keeps its precedence. It is nonetheless CONSULTED
	// ALWAYS: reading it only when the system offers no probe yard at all would turn
	// "probes come first" from an ordering claim into an exclusion, and a system
	// whose only probe yard also sells heavy hulls would never have its other heavy
	// yards seen.
	//
	// The joined list is built into a FRESH slice rather than appended onto the one
	// the port handed back: a port's return is the adapter's own buffer, and
	// appending into its spare capacity would write through into whatever the
	// adapter still holds, surfacing as a corrupted yard list rather than a build
	// failure.
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
// recorded values too, and that is DELIBERATE:
//
//   - Goods do not go stale in any way that matters. What a market DEALS IN is
//     stable; it is the prices that move, and no part of this row holds prices.
//   - Depth only orders placements that have not been filled yet, so once a probe
//     is bought for a slot the ordering has already done its work.
//   - A slot carrying the blind 0 prior resolves itself without this path. Depth
//     needs prices and prices need presence, so the number can only improve once a
//     probe is parked there; by then its scans have populated market_data, GoodsAt
//     reports the waypoint as known, and screenMarkets computes depth FRESH
//     without consulting the frozen row.
//
// A metadata-refresh path here could therefore not improve on what the live path
// already produces. If that changes, the write to reach for is UpsertSlotMetadata,
// which refreshes goods and depth and leaves the placement's state and hull to the
// writers that own them.
//
// Re-screening is NOT confined to systems without placements: a PENDING system can
// already hold slots when it is re-screened, because a seed reading writes WANTED
// slots directly while the system's row still says PENDING (the seed records no
// verdict; the next screen does). Re-screening is also BATCHED — the coordinator
// sweeps a bounded number of PENDING systems per reconcile tick — and that is the
// live path this skip and the slot cache are both built for: the seed-measured
// goods flow back through the cache and let the verdict reach IN_SCOPE with no API
// call. Once a system is IN_SCOPE it is never re-screened.
func (s *screenPass) recordSlots(ctx context.Context, slots []PlannedSlot) error {
	for _, slot := range slots {
		if _, held := s.existing[slot.Waypoint]; held {
			continue
		}
		if err := s.p.Ledger.UpsertSlotMetadata(ctx, s.playerID, SlotRecord{
			Waypoint:       slot.Waypoint,
			System:         s.system,
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

// existing is filled once the ledger has been read and is constant after.
type screenPass struct {
	p         ScreenPorts
	playerID  int
	system    string
	whitelist map[string]bool
	existing  map[string]ExistingSlot
}
