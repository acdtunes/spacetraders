package parkedsensing

// screen_ports.go holds the READ adapters behind the screen, the expansion
// frontier and the scan rotation: what the map looks like, and which of its
// waypoints are charted, sell probes, or are worth a slot. Everything here is a
// query — nothing in this file commands a ship or moves money.
//
// Most of these reads go to the DATABASE rather than the API, and in three cases
// that is a contract rather than a preference. Each is stated at the method it
// binds; they are collected here because they share one cause: the sensing
// engine's per-tick cost must scale with the frontier it is working, never with
// the size of the map the fleet has already charted.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	domainPlayer "github.com/andrescamacho/spacetraders-go/internal/domain/player"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

const (
	// unchartedTrait marks a waypoint nobody has charted yet. It is the API's
	// own trait, and its ABSENCE is what makes every other trait on a waypoint
	// trustworthy.
	unchartedTrait = "UNCHARTED"
	// shipyardTrait is the waypoint trait behind the unpriced yard fallback.
	shipyardTrait = "SHIPYARD"
)

// Compile-time proof that each read adapter still satisfies the port the engine
// declares for it.
var (
	_ appSensing.WaypointCatalog     = (*WaypointCatalogPort)(nil)
	_ appSensing.ProbeYardCatalog    = (*WaypointCatalogPort)(nil)
	_ appSensing.UnchartedCatalog    = (*WaypointCatalogPort)(nil)
	_ appSensing.YardCatalogFrontier = (*WaypointCatalogPort)(nil)
	_ appSensing.MarketGoodsReader   = (*MarketGoodsPort)(nil)
	_ appSensing.SpreadObserver      = (*MarketGoodsPort)(nil)
	_ appSensing.RemoteMarketFetcher = (*RemoteMarketPort)(nil)
	_ appSensing.GateNeighbours      = (*GateNeighbourPort)(nil)
)

// waypointLister is the persisted waypoint catalog, narrowed to the two reads
// the sensing map needs. *persistence.GormWaypointRepository satisfies it.
type waypointLister interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
	// ListWithTraitInOpenEra is the FLEET-WIDE trait read, and the one exception to
	// this file's per-system cost rule. The free shipyard-catalogue pass is the only
	// caller: its whole job is to find the yards no system-scoped read ever reaches,
	// so it must ask about the map rather than about one system. It is bounded by
	// the count of CHARTED yards in the OPEN era, which grows with what the fleet
	// has explored and not with how often it ticks — and the set it feeds shrinks to
	// nothing as the reads land.
	//
	// ERA-SCOPED, and FAIL-CLOSED when the open era cannot be resolved. The
	// repository's era-AGNOSTIC ListWithTrait is deliberately not used here; see
	// OutstandingYards for why the distinction is the difference between a cheap
	// local enumeration and a stream of API failures against systems that no longer
	// exist.
	ListWithTraitInOpenEra(ctx context.Context, trait string) ([]*shared.Waypoint, error)
}

// WaypointCatalogPort answers "what is in this system?" from the persisted
// waypoint catalog and the shipyard inventory — never from the API.
//
// It satisfies three ports at once (the screen's WaypointCatalog, the buy
// queue's ProbeYardCatalog and expansion's UnchartedCatalog) because they are
// three views of one question, and giving them three adapters would let the
// screen's idea of a system's shipyards drift from the queue's idea of where it
// can buy.
type WaypointCatalogPort struct {
	waypoints waypointLister
	db        *gorm.DB
	playerID  int
}

// NewWaypointCatalogPort wires the map reads. playerID scopes the shipyard
// inventory, whose rows are per-player; the waypoint catalog itself is shared.
func NewWaypointCatalogPort(waypoints waypointLister, db *gorm.DB, playerID int) *WaypointCatalogPort {
	return &WaypointCatalogPort{waypoints: waypoints, db: db, playerID: playerID}
}

// ListMarketWaypoints returns the system's CHARTED waypoints carrying a
// marketplace.
//
// Charted-only is the contract the screen depends on, and the filter is explicit
// rather than implied: a waypoint's traits are unknown until somebody charts it,
// so an UNCHARTED row's trait list says nothing about whether it holds a market.
// Including one would send the screen's remote gap fill at a waypoint the API
// cannot answer for, spending a call per uncharted waypoint per screen.
func (p *WaypointCatalogPort) ListMarketWaypoints(ctx context.Context, system string) ([]string, error) {
	waypoints, err := p.waypoints.ListBySystemWithTrait(ctx, system, marketplaceTrait)
	if err != nil {
		return nil, fmt.Errorf("failed to list market waypoints in %q: %w", system, err)
	}
	out := make([]string, 0, len(waypoints))
	for _, waypoint := range waypoints {
		if hasTrait(waypoint, unchartedTrait) {
			continue
		}
		// The charted-market enumeration is one of the two places the skip's premise
		// can be refuted by live data: these rows carry real traits.
		p.reportBarrenTypeHoldingTrait(ctx, waypoint, marketplaceTrait)
		out = append(out, waypoint.Symbol)
	}
	sort.Strings(out)
	return out, nil
}

// unchartedIn is the system's outstanding charting work: every waypoint still
// carrying the UNCHARTED trait, in whatever order the store hands them back.
//
// THE SINGLE SOURCE FOR BOTH UNCHARTED READS, and it exists for what it PREVENTS.
// ListUnchartedCount is the charting tour's COMPLETION SIGNAL and
// UnchartedStops is its WORK LIST, and the engine treats them as two views
// of one set: the screen writes the count to uncharted_count, verdictFor will
// not durably write a system off until it reads zero, and the tour ends only
// when the list comes back empty. Narrow ONE of them — by type, by distance, by
// anything — and the tour finishes while the count never reaches zero. The
// system is then pinned PENDING forever, seedlessTargets keeps re-dispatching
// probes to it, and because only IN_SCOPE/NO_WHITELIST systems propagate the
// frontier, expansion stalls permanently behind the first system holding an
// excluded waypoint. Routing both reads through here means a filter lands in ONE
// place and applies to both, so the two can never disagree about WHICH waypoints
// are outstanding. They are free to disagree about order — and do.
//
// THE BARREN-TYPE FILTER IS HERE PRECISELY BECAUSE THIS IS THE SHARED SOURCE.
// Barren types are dropped from the outstanding set, so the COUNT and the WORK
// LIST narrow together and stay two views of one set — which is what makes
// completion still mean something. A system whose only remaining waypoints are
// asteroids counts ZERO outstanding and reaches a terminal charted state instead
// of being toured indefinitely to discover nothing. In UnchartedStops alone
// the same filter would leave that system out of stops while the count sat
// non-zero forever: pinned PENDING, re-dispatched probes, frontier stalled.
//
// Most of the fleet's remaining charting work is ASTEROID, a type charted in
// bulk across two universes without one market or shipyard on it.
func (p *WaypointCatalogPort) unchartedIn(ctx context.Context, system string) ([]*shared.Waypoint, error) {
	waypoints, err := p.waypoints.ListBySystemWithTrait(ctx, system, unchartedTrait)
	if err != nil {
		return nil, err
	}
	outstanding := make([]*shared.Waypoint, 0, len(waypoints))
	for _, waypoint := range waypoints {
		if waypoint == nil || shared.ChartSkippable(waypoint.Type) {
			continue
		}
		outstanding = append(outstanding, waypoint)
	}
	return outstanding, nil
}

// reportBarrenTypeHoldingTrait is the SKIP'S OWN FALSIFIER.
//
// Skipping a type is a claim about evidence — "ASTEROID has never held a market
// or a shipyard in any charted example" — and a claim that nothing can refute is
// not evidence, it is a blacklist. One counter-example refutes this one, so the
// fleet is wired to notice it: every enumeration of a CHARTED market or yard
// passes through here, and a barren-tier waypoint carrying either trait is
// logged as an ERROR naming the waypoint.
//
// IT WORKS DESPITE THE SKIP, which is the part worth checking before trusting
// it. The obvious objection is that we stop charting asteroids and therefore
// stop learning about them — but this reads the traits of waypoints ALREADY
// charted, and those rows keep being synced whether or not we fly anywhere. A
// market appearing on any one of them surfaces here without a single new flight.
//
// It is deliberately an ERROR and not a metric: this must never fire, and if it
// does the correct response is a human reading the census again, not a counter
// ticking up on a dashboard nobody is watching.
func (p *WaypointCatalogPort) reportBarrenTypeHoldingTrait(ctx context.Context, waypoint *shared.Waypoint, trait string) {
	if waypoint == nil || !shared.ChartSkippable(waypoint.Type) {
		return
	}
	common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf(
		"Charting census REFUTED: %s is a %s carrying %s, but %s is skipped from charting on the claim that it never holds one. The skip is now wrong — re-count the census in domain/shared/charting.go before trusting any charting completion signal",
		waypoint.Symbol, waypoint.Type, trait, waypoint.Type),
		map[string]interface{}{
			"waypoint": waypoint.Symbol, "type": waypoint.Type, "trait": trait,
			"action": "chart_skip_premise_refuted",
		})
}

// ListUnchartedCount reports how many of the system's waypoints are still
// uncharted.
func (p *WaypointCatalogPort) ListUnchartedCount(ctx context.Context, system string) (int, error) {
	waypoints, err := p.unchartedIn(ctx, system)
	if err != nil {
		return 0, fmt.Errorf("failed to count uncharted waypoints in %q: %w", system, err)
	}
	return len(waypoints), nil
}

// UnchartedStops returns the system's uncharted waypoints in the order a
// charting seed should visit them: SHIPYARD-BEARING TYPES FIRST, then
// market-bearing ones, then the rest, alphabetically inside each tier.
//
// THE TOUR IS NOT EXHAUSTIVE, AND THE COUNT MATCHES IT. Barren types are dropped
// by unchartedIn, so asteroids appear in neither this list nor
// ListUnchartedCount; the two remain views of ONE set, read from the same rows,
// so the count falls to zero exactly when the tour runs out of stops.
//
// This function stays a pure ORDERING over whatever unchartedIn hands it. The
// skip is deliberately NOT repeated here — a second copy of the predicate is
// exactly how the list and the count would come to disagree.
//
// The tier is the point and the alphabet is only the tie-break. A charted
// shipyard makes its system buyable, which funds local spares, which stage more
// seeds; a charted market lets a parked scanner be placed on it and start
// producing trade data while the tour continues. A flat alphabetical order
// leaves both to chance, which is how a seed comes to spend fifty hours on
// asteroids before revealing the one market in the system. Type is the only
// evidence available before the flight, because the SHIPYARD and MARKETPLACE
// traits are themselves hidden until the waypoint is charted; see
// shared.ChartPriority.
//
// The order remains totally deterministic, which is what the tour requires: a
// seed charts the head of this list and re-derives it next tick, so an unstable
// order would let it oscillate between two waypoints and never finish. Sorting
// on the pair (priority, symbol) — rather than by priority alone — is what keeps
// that true regardless of the order the rows arrive in.
// Each stop carries its COORDINATES and its own tier. A crewed system's shares are
// solved over the plane and handed back in geometric order, so the tier has to
// travel with the stop to survive that re-ordering — it cannot be read back off a
// position in this list once a partitioner has shuffled it.
func (p *WaypointCatalogPort) UnchartedStops(ctx context.Context, system string) ([]appSensing.ChartStop, error) {
	waypoints, err := p.unchartedIn(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list uncharted waypoints in %q: %w", system, err)
	}
	sort.Slice(waypoints, func(i, j int) bool {
		left, right := shared.ChartPriority(waypoints[i].Type), shared.ChartPriority(waypoints[j].Type)
		if left != right {
			return left < right
		}
		return waypoints[i].Symbol < waypoints[j].Symbol
	})
	out := make([]appSensing.ChartStop, 0, len(waypoints))
	for _, waypoint := range waypoints {
		out = append(out, appSensing.ChartStop{
			Waypoint: waypoint.Symbol,
			X:        waypoint.X,
			Y:        waypoint.Y,
			Priority: shared.ChartPriority(waypoint.Type),
		})
	}
	return out, nil
}

// ListProbeYards returns the system's shipyards that sell probes, cheapest
// first.
//
// DATABASE-ONLY, and that is load-bearing for the buy queue rather than merely
// cheap. The drain treats "no yard here" as a free skip that costs no attempt
// against its per-tick cap, on the explicit ground that the answer came from
// local rows — an implementation that reached for the API would turn every
// unbuyable placement into a live call, hardest exactly when the API is already
// degraded, which is the failure mode the attempt cap exists to prevent.
//
// Two sources, UNIONED — never either/or:
//
//  1. shipyard_inventory rows offering SHIP_PROBE, ordered by the price last
//     scanned. These are yards we have priced and can rank.
//  2. ALONGSIDE them, the system's bare SHIPYARD-trait waypoints, as UNPRICED
//     candidates. A yard nobody has scanned still sells probes; excluding it
//     would leave a never-visited shipyard permanently unbuyable, and the drain
//     prices every candidate live before it spends anyway.
//
// Source 2 is NOT conditional on source 1 being empty, and that is the whole
// point. Whether one yard is priced is evidence about THAT yard and says nothing
// about its neighbour: making the trait fallback conditional on any priced yard
// in the system loses every not-yet-priced yard in it.
//
// Membership is decided per waypoint by appSensing.ProbeYardIsCandidate, the shared
// probe-stock rule, so a yard priced and found probe-less is excluded here on the
// same reading the buy queue would refuse it on.
//
// ListHeavyYards returns the system's shipyards that sell HEAVY hulls, cheapest
// first — the quartermaster-coverage half of the heavy-trade design (spec §6): a
// probe parked at a heavy yard makes every future heavy purchase there instant
// instead of requiring a hull to fly in first.
//
// DATABASE-ONLY for the same load-bearing reason as ListProbeYards: the screen
// runs this per system on every pass, and reaching for the API would turn yard
// discovery into live calls exactly when the API is most degraded.
//
// ERA-SCOPED, and it must stay aligned with its siblings. The sibling heavy read behind the
// reservation — ShipyardInventoryRepositoryGORM's CheapestPricedYard — is era-scoped too;
// scoping only one of a pair lets them answer "which yards sell heavies" under different era
// rules, and a stale pre-reset row can then plan a quartermaster here. ALL FOUR — this read,
// CheapestPricedYard, ListProbeYards and OutstandingYards — answer under the open era only,
// and all fail closed when it cannot be resolved.
//
// Unlike ListProbeYards there is NO bare-SHIPYARD-trait fallback. An unscanned
// shipyard is already returned by ListProbeYards' fallback and therefore already
// earns a quartermaster; claiming an unscanned yard as heavy-selling as well
// would assert something we have not observed. Only PRICED heavy rows count
// here, so this list is evidence, never assumption.
func (p *WaypointCatalogPort) ListHeavyYards(ctx context.Context, system string) ([]string, error) {
	eraPredicate, eraArgs, err := persistence.OpenEraScope(ctx, p.db)
	if err != nil {
		return nil, fmt.Errorf("failed to list heavy yards in %q: %w", system, err)
	}
	var rows []struct {
		WaypointSymbol string
		PurchasePrice  int
	}
	err = p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("waypoint_symbol, purchase_price").
		Where("player_id = ? AND system_symbol = ? AND ship_type IN ?", p.playerID, system, shipyardDomain.DefaultHeavyShipTypes).
		Where("purchase_price > 0").
		Where(eraPredicate, eraArgs...).
		Order("purchase_price ASC, waypoint_symbol ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list heavy yards in %q: %w", system, err)
	}
	seen := make(map[string]bool, len(rows))
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if seen[row.WaypointSymbol] {
			continue // one waypoint may sell both heavy classes; it is still one yard
		}
		seen[row.WaypointSymbol] = true
		out = append(out, row.WaypointSymbol)
	}
	return out, nil
}

// OutstandingYards lists every CHARTED shipyard whose catalogue we do not hold —
// the work list for the free, presence-less catalogue pass (appSensing.ReadYardCatalogues).
//
// It is a SET DIFFERENCE over two local reads and never touches the API, which is
// the same contract every other read on this port carries: the pass exists to
// REMOVE presence from shipyard discovery, so an enumeration that spent a call per
// candidate would reintroduce the cost it is trying to delete.
//
//   - The candidate half is the fleet-wide SHIPYARD-trait set in the OPEN ERA.
//     UNCHARTED waypoints are excluded: their traits are a guess until somebody charts
//     them, so a SHIPYARD trait on one is not yet evidence of a shipyard.
//   - The exclusion half is every waypoint already carrying a shipyard_inventory row.
//     "We hold a catalogue" is exactly "there is a row", which is what makes the pass
//     SELF-QUIESCING: a yard read once never appears here again, so the backlog drains
//     and the pass then costs one query per tick and nothing else.
//
// ERA-SCOPED, on the row's OWN era stamp.
//
// Reading the era-AGNOSTIC trait set instead — on the reasoning that a shipyard is an
// immutable physical fact so a prior-era row is still proof one is there, and that the
// worst case after a reset is UNDER-reading, discovery latency and never a wrong buy —
// gets the direction of error wrong. The first half is true; the second is not.
// `waypoints` is cumulative across resets: a row stamped by a closed era is never deleted,
// so an unscoped work list carries waypoints in universes that no longer exist. The API
// does not merely decline those, it 404s their whole SYSTEM — and that same 404 is why the
// exclusion half can never clear them. Exclusion removes only what already holds a
// shipyard_inventory row, and a waypoint whose system 404s can no longer earn one, so a
// dead-era yard this pass never reached while its era was open is residue: the live half
// of the backlog drains, that half is re-offered every tick forever. And because the
// per-tick bound counts ATTEMPTS and not successes — correctly, so a refusing API cannot
// become an unbounded retry storm — each residue row consumes a slot a live yard needs.
//
// WHY THE ROW'S era_id AND NOT LEDGER MEMBERSHIP. Intersecting against sensing_systems
// instead is neither sufficient nor free: dead-era-stamped yard waypoints sit in systems
// that ARE in this era's ledger, so a ledger intersection admits exactly the class being
// removed, while an open-era yard system absent from the ledger would have real frontier
// work deleted. Ledger membership is already used here, and correctly — as the frontier
// RANK below, never as a gate. This pass exists to reach yards in systems the screen has
// not reached, so promoting that rank to a gate would invert its purpose. era_id is
// stamped by GormWaypointRepository.Add at write time and so records the one fact a caller
// about to spend a call needs: which universe's API last confirmed this waypoint exists.
//
// FAIL-CLOSED. An unresolvable open era refuses rather than falling back to unscoped,
// because unscoped IS the bug. The pass above treats a failure to enumerate as fatal to
// itself and reports an empty backlog rather than a drained one, and the reconcile
// collects that failure without aborting the tick — so refusing costs one tick of
// discovery latency and nothing else.
//
// The immutability argument still holds where it was load-bearing: the shipyard BACKFILL
// enumerator keeps the era-agnostic read, because it intersects its result with the
// current gate-reachable frontier before anything is spent. This pass has no such
// downstream filter — it hands the enumeration straight to the API — and that is the
// whole difference between the two callers.
//
// FRONTIER RANK IS "DO WE ALREADY WATCH THIS SYSTEM", greater first: a yard in a system
// holding no sensing placement gets 1, one in a system we already watch gets 0. That is
// the ordering that matters for a FREE read, because it ranks by whether this pass is
// the ONLY route to the answer — a system we already watch will have a hull parked in it
// sooner or later, and that hull's scan reads the yard under its feet (scanner.go), while
// a system we watch nothing in has no other route at all. It is deliberately not a
// gate-hop depth: that needs a graph walk per tick, and for a read that flies nothing and
// spends nothing, distance ranks the wrong thing.
func (p *WaypointCatalogPort) OutstandingYards(ctx context.Context, playerID int) ([]appSensing.OutstandingYard, error) {
	yards, err := p.waypoints.ListWithTraitInOpenEra(ctx, shipyardTrait)
	if err != nil {
		return nil, fmt.Errorf("failed to list the charted shipyards of the open era: %w", err)
	}

	held, err := p.distinctColumnSet(ctx, playerID, "shipyard_inventory", "waypoint_symbol")
	if err != nil {
		return nil, fmt.Errorf("failed to read which shipyard catalogues we already hold: %w", err)
	}
	watched, err := p.distinctColumnSet(ctx, playerID, "sensing_slots", "system_symbol")
	if err != nil {
		return nil, fmt.Errorf("failed to read which systems we already watch: %w", err)
	}

	out := make([]appSensing.OutstandingYard, 0, len(yards))
	for _, yard := range yards {
		if yard == nil || held[yard.Symbol] || hasTrait(yard, unchartedTrait) {
			continue
		}
		// The charted-yard enumeration is the second refutation point for the charting
		// skip; a barren-tier waypoint holding a SHIPYARD breaks the census.
		p.reportBarrenTypeHoldingTrait(ctx, yard, shipyardTrait)
		system := yard.SystemSymbol
		if system == "" {
			system = shared.ExtractSystemSymbol(yard.Symbol)
		}
		frontier := 1
		if watched[system] {
			frontier = 0
		}
		out = append(out, appSensing.OutstandingYard{
			Waypoint: yard.Symbol,
			System:   system,
			Frontier: frontier,
		})
	}
	return out, nil
}

func (p *WaypointCatalogPort) distinctColumnSet(ctx context.Context, playerID int, table, column string) (map[string]bool, error) {
	var values []string
	if err := p.db.WithContext(ctx).
		Table(table).
		Distinct(column).
		Where("player_id = ?", playerID).
		Pluck(column, &values).Error; err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set, nil
}

// LastListingScan reports what the stored shipyard inventory says about ONE
// yard's probe stock, and when that reading was taken.
//
// DATABASE-ONLY, like every other read on this port: it exists to REMOVE an API
// call, so it must never make one. The row set is written by the sensing quote
// path (ProbePurchasePort.persistListings) and by the shipyard scanner; both go
// through the same ReplaceScan, so a waypoint's rows are always a complete
// snapshot of one reading rather than an accumulation across several.
//
// THE READING IS INTERPRETED AGAINST WHAT KIND OF READING IT WAS, and getting
// that wrong is a fleet-killer rather than a mis-report. A stored row set is one
// of two things:
//
//   - a PRICED reading, taken with a hull at the counter: the `ships` array came
//     back, so at least one row carries a price. Here a probe row at price 0 does
//     NOT count as selling a probe — ShipTypeAvailability documents price 0 as
//     "listed, but carried no priced listing at scan time", the drain's quote
//     refuses such a yard for exactly that reason, and counting it would keep
//     re-quoting a counter we already know cannot price the hull. That is the loop
//     the memo exists to break.
//   - a CATALOGUE-ONLY reading, taken with no hull anywhere: `shipTypes` came back
//     and `ships` did not, so EVERY row is price 0 by construction. Reading an
//     unpriced probe row as "sells no probe" here is simply false — the yard sells
//     probes, we have not priced them — and the consequence is not cosmetic: it
//     classifies the yard probeStockNone, ProbeYardIsCandidate drops it out of
//     ListProbeYards, and a counter the fleet could have bought its next probe at
//     becomes invisible for six hours. The free catalogue pass writes exactly this
//     shape at every yard it reads, so without this branch turning the pass on
//     would have SHRUNK the probe-yard universe it was built to grow.
//
// The two are told apart by the reading itself, with no schema change and no
// flag: a reading that priced ANYTHING is a priced reading. persistListings in
// ship_ports.go states the same distinction as the intent ("the memo can tell
// 'this yard does not sell probes' from 'this yard sells probes we could not
// price', and only the first is a reason to stop asking").
//
// A catalogue-only reading that lists no probe at all is positive evidence the
// yard sells none, so the drain stops paying live quotes there.
//
// known=false when the waypoint has no rows at all, which the caller must read as
// "ask once" and never as "no probe".
//
// DELIBERATELY NOT ERA-SCOPED, and it is the one read in this file that is not. The
// distinction is which QUESTION the read answers. The yard reads build a candidate
// UNIVERSE and hand it to something that spends — so a dead-era row there manufactures
// work, and scoping them strictly REMOVES
// candidates. This read is a per-waypoint stock MEMO consulted by ProbeYardIsCandidate,
// and its rows are the reason a yard is refused: era-scoping it would turn a yard we
// priced and found probe-less into a yard we have never priced, ADMITTING it to the buy
// queue and paying live quotes at a counter we already know cannot sell us a probe.
// That direction loosens a guard the drain spends against (RULINGS #4 — money guards may
// only get stricter), so it is left alone. The two choices point the same way: both keep
// the spending path narrower, not wider.
//
// It is also harmless in combination. A yard only reaches this read by surviving the
// era-scoped universe above, so a dead-era row can no longer ADD a yard; all it can do is
// exclude one, which is the fail-closed direction.
func (p *WaypointCatalogPort) LastListingScan(ctx context.Context, playerID int, waypoint string) (bool, time.Time, bool, error) {
	var rows []struct {
		ShipType      string
		PurchasePrice int
		LastScanned   time.Time
	}
	err := p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("ship_type, purchase_price, last_scanned").
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypoint).
		Scan(&rows).Error
	if err != nil {
		return false, time.Time{}, false, fmt.Errorf("failed to read stored listings for %q: %w", waypoint, err)
	}
	if len(rows) == 0 {
		return false, time.Time{}, false, nil
	}
	priced, pricedProbe, listedProbe := false, false, false
	var scannedAt time.Time
	for _, row := range rows {
		if row.PurchasePrice > 0 {
			priced = true
		}
		if row.ShipType == probeShipType {
			listedProbe = true
			if row.PurchasePrice > 0 {
				pricedProbe = true
			}
		}
		if row.LastScanned.After(scannedAt) {
			scannedAt = row.LastScanned
		}
	}
	// A priced reading is judged on its price; an unpriced one is all the evidence
	// there is, and it says the yard lists the hull.
	sellsProbe := pricedProbe || (!priced && listedProbe)
	return sellsProbe, scannedAt, true, nil
}

// ERA-SCOPED on BOTH halves of the union, and fail-closed when the open era cannot be
// resolved. Unscoped, a pre-reset row could put a yard from a dead universe at the head
// of the cheapest-first ranking — the position the drain quotes from first. The whole
// file answers under one era rule; see OutstandingYards for why the row's own era stamp
// is the predicate.
func (p *WaypointCatalogPort) ListProbeYards(ctx context.Context, system string) ([]string, error) {
	eraPredicate, eraArgs, err := persistence.OpenEraScope(ctx, p.db)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	var rows []struct {
		WaypointSymbol string
		PurchasePrice  int
	}
	err = p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("waypoint_symbol, purchase_price").
		Where("player_id = ? AND system_symbol = ? AND ship_type = ?", p.playerID, system, probeShipType).
		Where(eraPredicate, eraArgs...).
		Order("purchase_price ASC, waypoint_symbol ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	traitYards, err := p.waypoints.ListBySystemWithTrait(ctx, system, shipyardTrait)
	if err != nil {
		return nil, fmt.Errorf("failed to list shipyards in %q: %w", system, err)
	}
	// A UNION, not an either/or. Falling back to traits only when the system holds
	// NOT ONE probe row lets a single priced yard hide every unpriced one, and a
	// yard nothing can see is a counter we can never buy at.
	universe := make([]string, 0, len(rows)+len(traitYards))
	seen := make(map[string]bool, len(rows)+len(traitYards))
	// Priced first, so the cheapest-first order the query already applied survives
	// into the evidenced half below.
	for _, row := range rows {
		if seen[row.WaypointSymbol] {
			continue
		}
		seen[row.WaypointSymbol] = true
		universe = append(universe, row.WaypointSymbol)
	}
	universe = append(universe, chartedTraitYards(traitYards, seen)...)

	return p.probeStockCandidates(ctx, universe)
}

// ProbeAsks returns every yard the fleet holds a PRICED probe reading for, with the
// price and the moment it was taken — the snapshot the buy queue ranks counters
// against and derives its walk-away ceiling from (appSensing.ProbeAskReader).
//
// FLEET-WIDE AND UNFILTERED BY DISTANCE, which is the whole point: the caller's two
// questions — "which counters can reach this placement, and what do they charge" and
// "what is the cheapest ask anywhere" — are questions about the SET, and answering
// the second from a system-scoped query would measure the multiple against a
// neighbourhood rather than against the fleet. One read serves both, so they cannot
// disagree.
//
// PRICED ROWS ONLY. An unpriced row is a catalogue-only reading, taken with no hull
// at the counter, and says the yard SELLS a probe rather than what it charges;
// returning it as a zero would rank every never-visited yard as free. Those yards
// still reach the drain — ListProbeYards' trait fallback carries them, and the buy
// queue offers them behind every priced counter.
//
// ERA-SCOPED and FAIL-CLOSED on an unresolvable era, like every other yard read on
// this port. A pre-reset row here is worse than absent in both directions at once: it
// could rank a dead-universe counter at the head of the queue, and — because the
// cheapest ask is what the walk-away is a multiple of — one stale-cheap row would
// drag the ceiling down and hold live placements the fleet can well afford.
//
// DATABASE-ONLY, never a fetch-through. The drain calls this on every tick that has a
// placement to fill.
func (p *WaypointCatalogPort) ProbeAsks(ctx context.Context, playerID int) ([]appSensing.ProbeAsk, error) {
	eraPredicate, eraArgs, err := persistence.OpenEraScope(ctx, p.db)
	if err != nil {
		return nil, fmt.Errorf("failed to read the stored probe asks: %w", err)
	}
	var rows []struct {
		WaypointSymbol string
		SystemSymbol   string
		PurchasePrice  int
		LastScanned    time.Time
	}
	err = p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("waypoint_symbol, system_symbol, purchase_price, last_scanned").
		Where("player_id = ? AND ship_type = ?", playerID, probeShipType).
		Where("purchase_price > 0").
		Where(eraPredicate, eraArgs...).
		Order("waypoint_symbol ASC, last_scanned DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read the stored probe asks: %w", err)
	}
	out := make([]appSensing.ProbeAsk, 0, len(rows))
	for _, row := range rows {
		out = append(out, appSensing.ProbeAsk{
			Yard:      row.WaypointSymbol,
			System:    row.SystemSymbol,
			Price:     int64(row.PurchasePrice),
			ScannedAt: row.LastScanned,
		})
	}
	return out, nil
}

// chartedTraitYards is the fallback half of the candidate universe, in symbol order.
// UNCHARTED is not yet a yard — its traits are a guess until someone charts it. The
// priced half is deliberately NOT trait-filtered: a yard we have actually priced is
// evidenced by the reading itself, and may have no waypoint row at all.
func chartedTraitYards(traitYards []*shared.Waypoint, seen map[string]bool) []string {
	out := make([]string, 0, len(traitYards))
	for _, waypoint := range traitYards {
		if seen[waypoint.Symbol] || hasTrait(waypoint, unchartedTrait) {
			continue
		}
		seen[waypoint.Symbol] = true
		out = append(out, waypoint.Symbol)
	}
	sort.Strings(out)
	return out
}

// probeStockCandidates drops the yards the drain itself would refuse.
//
// THE SHARED RULE DECIDES, not this query. appSensing.ProbeYardIsCandidate is
// the exported face of readProbeStock — the same classification the buy queue's
// skipKnownProbeless and seed staging's probeStock.acceptsStaging consult — so a
// yard priced and found probe-less is dropped here for exactly the reason the
// drain would refuse it, and a STALE probe-less reading degrades to "never
// priced" and is reconsidered. Re-deriving that in SQL is what would let the
// three engines drift.
//
// EVIDENCE-FIRST ORDER COMES FROM THE UNION, not from a second sort. universe is
// priced rows (cheapest first) followed by trait-only yards (symbol order), and
// each half can classify only one way: a yard drawn from a priced probe row is
// SELLS or NONE, a trait-only yard is UNREAD or NONE. So every survivor of the
// first half is evidence and every survivor of the second is a guess, and filtering
// IN PLACE preserves both the ranking and the cheapest-first order inside it. A
// partition here would re-sort nothing.
//
// Ranking a guess last must never mean dropping it — an unpriced yard is how the
// fleet learns where probes are sold at all.
func (p *WaypointCatalogPort) probeStockCandidates(ctx context.Context, universe []string) ([]string, error) {
	now := time.Now()
	out := make([]string, 0, len(universe))
	for _, yard := range universe {
		candidate, err := appSensing.ProbeYardIsCandidate(ctx, p, p.playerID, yard, now)
		if err != nil {
			return nil, fmt.Errorf("failed to classify probe stock at %q: %w", yard, err)
		}
		if !candidate {
			continue
		}
		out = append(out, yard)
	}
	return out, nil
}

// CatalogKnown reports whether the system's waypoint LIST has ever been swept.
//
// MONOTONE ONCE SWEPT, and this is the single most dangerous method in the file
// to get subtly wrong. Two facts combine:
//
//   - the screen records this value on the sensing_systems row, through a column
//     list that WRITES it — so an adapter answering false for a system that has
//     in fact been swept does not merely mis-report, it NULLS the stamp; and
//   - the value gates the durable NO_WHITELIST verdict, and a NO_WHITELIST
//     system is a propagation origin for the expansion frontier.
//
// So a stamp-only read that flickered false for one screen would erase its own
// evidence and re-open the system for a charting seed it does not need — and a
// value that flickered the other way writes off a system nobody ever looked at
// and walks that mistake outward across the map.
//
// The read is therefore a disjunction of two independent proofs, either of which
// is sufficient and NEITHER of which can be un-learned: the ledger's explicit
// sweep stamp, or the plain existence of waypoint rows for the system. The second
// is what recognises a system the fleet swept long before this model existed.
func (p *WaypointCatalogPort) CatalogKnown(ctx context.Context, system string) (bool, error) {
	var stamped int64
	err := p.db.WithContext(ctx).
		Table("sensing_systems").
		Where("player_id = ? AND system_symbol = ? AND catalog_synced_at IS NOT NULL", p.playerID, system).
		Count(&stamped).Error
	if err != nil {
		return false, fmt.Errorf("failed to read the catalog sweep stamp for %q: %w", system, err)
	}
	if stamped > 0 {
		return true, nil
	}

	waypoints, err := p.waypoints.ListBySystem(ctx, system)
	if err != nil {
		return false, fmt.Errorf("failed to read the waypoint catalog of %q: %w", system, err)
	}
	return len(waypoints) > 0, nil
}

// hasTrait reports whether a waypoint carries a trait, case-insensitively.
func hasTrait(waypoint *shared.Waypoint, trait string) bool {
	if waypoint == nil {
		return false
	}
	for _, held := range waypoint.Traits {
		if strings.EqualFold(held, trait) {
			return true
		}
	}
	return false
}

// HomeSystemPort resolves the player's headquarters system from the players
// table.
//
// DATABASE-ONLY by contract. Its one consumer is the cutover, which uses the
// answer to decide which single scout post survives a bulk retirement of the
// rest — so a read that could fail on a network hiccup would make an
// irreversible delete depend on the API being up. An unreadable home system
// refuses the cutover outright rather than proceeding without it.
type HomeSystemPort struct {
	db *gorm.DB
}

// NewHomeSystemPort wires the headquarters read.
func NewHomeSystemPort(db *gorm.DB) *HomeSystemPort {
	return &HomeSystemPort{db: db}
}

// HomeSystem returns the system the player's headquarters waypoint sits in.
func (p *HomeSystemPort) HomeSystem(ctx context.Context, playerID int) (string, error) {
	var raw string
	err := p.db.WithContext(ctx).
		Table("players").
		Select("metadata").
		Where("id = ?", playerID).
		Scan(&raw).Error
	if err != nil {
		return "", fmt.Errorf("failed to read player %d: %w", playerID, err)
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("player %d has no stored metadata, so the home system is unknown", playerID)
	}

	var metadata map[string]interface{}
	if uerr := json.Unmarshal([]byte(raw), &metadata); uerr != nil {
		return "", fmt.Errorf("failed to decode player %d metadata: %w", playerID, uerr)
	}
	// This failure surfaces four layers up as a sensing CUTOVER refusal, which aborts the entire
	// reconcile — screen, reaper, adoption, drain, placements and expansion — on every tick, and
	// its visible symptom reads as an idle engine rather than a broken one. Nothing else in that
	// chain names the key, says which of the many things sensing reads is missing, or hints at
	// how it gets populated, so this message must carry all three.
	headquarters, ok := domainPlayer.HeadquartersFrom(metadata)
	if !ok {
		return "", fmt.Errorf(
			"player %d has no %s in players.metadata, so the home system is unknown and sensing cannot cut over: %s",
			playerID, domainPlayer.MetadataKeyHeadquarters, domainPlayer.MissingHeadquartersHint)
	}
	return shared.ExtractSystemSymbol(headquarters), nil
}
