package parkedsensing

// screen_ports.go holds the READ adapters behind the screen, the expansion
// frontier and the scan rotation: what the map looks like, what markets deal in,
// and what they are quoting. Everything here is a query — nothing in this file
// commands a ship or moves money.
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
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
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
	// the count of CHARTED yards in the OPEN era (1,219 measured live, against 1,772
	// across all eras), which is a number that grows with what the fleet has explored
	// and not with how often it ticks — and the set it feeds shrinks to nothing as
	// the reads land.
	//
	// ERA-SCOPED, and FAIL-CLOSED when the open era cannot be resolved. The
	// repository's era-AGNOSTIC ListWithTrait is deliberately not used here; see
	// OutstandingYards for why the distinction is the difference between a cheap
	// local enumeration and ~290 API failures an hour.
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
		out = append(out, waypoint.Symbol)
	}
	sort.Strings(out)
	return out, nil
}

// unchartedIn is the system's outstanding charting work: every waypoint still
// carrying the UNCHARTED trait, in whatever order the store hands them back.
//
// THE SINGLE SOURCE FOR BOTH UNCHARTED READS. Today the two callers below could
// each issue this query themselves and get identical rows, so this exists for
// what it PREVENTS rather than for what it currently does.
//
// ListUnchartedCount is the charting tour's COMPLETION SIGNAL and
// UnchartedWaypoints is its WORK LIST, and the engine treats them as two views
// of one set: the screen writes the count to uncharted_count, verdictFor will
// not durably write a system off until it reads zero, and the tour ends only
// when the list comes back empty. Narrow ONE of them — by type, by distance, by
// anything — and the tour finishes while the count never reaches zero. The
// system is then pinned PENDING forever, seedlessTargets keeps re-dispatching
// probes to it, and because only IN_SCOPE/NO_WHITELIST systems propagate the
// frontier, expansion stalls permanently behind the first system holding an
// excluded waypoint.
//
// That is not hypothetical: it is the failure the rejected skip design would
// have shipped. Routing both reads through here means a future filter lands in
// ONE place and applies to both, so the two can never disagree about WHICH
// waypoints are outstanding. They are free to disagree about order — and do.
func (p *WaypointCatalogPort) unchartedIn(ctx context.Context, system string) ([]*shared.Waypoint, error) {
	return p.waypoints.ListBySystemWithTrait(ctx, system, unchartedTrait)
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

// UnchartedWaypoints returns the system's uncharted waypoints in the order a
// charting seed should visit them: SHIPYARD-BEARING TYPES FIRST, then
// market-bearing ones, then the rest, alphabetically inside each tier.
//
// THE SET IS UNCHANGED AND THE TOUR IS STILL EXHAUSTIVE. Every uncharted
// waypoint in the system is returned, asteroids included — this reorders the
// same list the tour always walked. That is what keeps ListUnchartedCount above
// a valid completion signal without any coordination between the two: they read
// the same rows, so the count still falls to zero exactly when the tour runs
// out of stops.
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
func (p *WaypointCatalogPort) UnchartedWaypoints(ctx context.Context, system string) ([]string, error) {
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
	out := make([]string, 0, len(waypoints))
	for _, waypoint := range waypoints {
		out = append(out, waypoint.Symbol)
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
// about its neighbour: making the trait fallback conditional on any priced yard in
// the system loses every not-yet-priced yard in it — 81 of 614 charted shipyards,
// measured live.
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
// ERA-SCOPED, on the row's OWN era stamp, and the reason is a measured production bleed
// rather than tidiness.
//
// Reading the era-AGNOSTIC trait set instead — on the reasoning that a shipyard is an
// immutable physical fact so a prior-era row is still proof one is there, and that the
// worst case after a reset is UNDER-reading, discovery latency and never a wrong buy —
// gets the direction of error wrong. The first half is true; the second is not. `waypoints`
// holds 1,772 SHIPYARD rows across 862 systems and only 1,219 across 587 carry the open
// era's stamp, so an unscoped work list is mostly waypoints in universes that no
// longer exist. The API does not merely decline those, it 404s their whole SYSTEM
// ("System X1-AF2 not found"), and this pass burned ~290 such failures an hour for ten
// hours, flat and not converging, with utilisation at 88% against an 85% ceiling.
// Because the per-tick bound counts ATTEMPTS and not successes — correctly, so a
// refusing API cannot become an unbounded retry storm — every dead-era yard consumes a
// slot a live yard needs. A sweep built to find heavy shipyards spends most of its
// budget on systems that are gone.
//
// WHY THE ROW'S era_id AND NOT LEDGER MEMBERSHIP. The alternative is to intersect
// against sensing_systems for the player. It is neither sufficient nor free: measured
// live, 11 dead-era-stamped yard waypoints sit in systems that ARE in this era's ledger,
// so a ledger intersection admits exactly the class being removed, while one open-era
// yard system is absent from the ledger, so it would also delete real frontier work.
// Ledger membership is already used here, and correctly — as the frontier RANK below,
// never as a gate. This pass exists to reach yards in systems the screen has not
// reached, so promoting that rank to a gate would invert its purpose. era_id is stamped
// by GormWaypointRepository.Add at write time and so records the one fact a caller about
// to spend a call needs: which universe's API last confirmed this waypoint exists.
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

	var readSymbols []string
	if err := p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Distinct("waypoint_symbol").
		Where("player_id = ?", playerID).
		Pluck("waypoint_symbol", &readSymbols).Error; err != nil {
		return nil, fmt.Errorf("failed to read which shipyard catalogues we already hold: %w", err)
	}
	held := make(map[string]bool, len(readSymbols))
	for _, symbol := range readSymbols {
		held[symbol] = true
	}

	var watchedSystems []string
	if err := p.db.WithContext(ctx).
		Table("sensing_slots").
		Distinct("system_symbol").
		Where("player_id = ?", playerID).
		Pluck("system_symbol", &watchedSystems).Error; err != nil {
		return nil, fmt.Errorf("failed to read which systems we already watch: %w", err)
	}
	watched := make(map[string]bool, len(watchedSystems))
	for _, system := range watchedSystems {
		watched[system] = true
	}

	out := make([]appSensing.OutstandingYard, 0, len(yards))
	for _, yard := range yards {
		if yard == nil || held[yard.Symbol] || hasTrait(yard, unchartedTrait) {
			continue
		}
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
//     the memo exists to break, and it is preserved untouched.
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
// ship_ports.go already states this distinction as the intent ("the memo can tell
// 'this yard does not sell probes' from 'this yard sells probes we could not
// price', and only the first is a reason to stop asking") — it just had no way to
// express it while every reading carried prices.
//
// DIRECTION CHECK. Against the behaviour before the catalogue pass existed, this
// LOOSENS nothing: a yard with no rows was already UNREAD and already a
// candidate, and it stays one. What it does add is a genuine TIGHTENING — a
// catalogue-only reading that lists no probe at all is now positive evidence the
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
// resolved. The trait half already was, through the repository's
// ListBySystemWithTrait; the priced half read shipyard_inventory unscoped, so a
// pre-reset row could put a yard from a dead universe at the head of the
// cheapest-first ranking — the position the drain quotes from first. The whole file
// now answers under one era rule: see OutstandingYards for the measured bleed that
// made the alignment urgent and for why the row's own era stamp is the predicate.
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
	// THE CANDIDATE UNIVERSE, built PER WAYPOINT rather than per system: every
	// waypoint already carrying a probe row, plus every charted SHIPYARD-trait
	// waypoint. The union is what makes the two halves independent.
	//
	// An either/or — `if len(rows) > 0 { return priced }`, with the trait fallback
	// only when the system holds NOT ONE probe row anywhere — lets one priced
	// yard switch the fallback off for the WHOLE system, and every yard not yet
	// priced becomes invisible: not merely unranked, absent.
	// Measured live, 81 of 614 charted shipyards were lost that way, and a
	// yard nothing can see is a counter we can never buy at, because buying needs a
	// hull already standing there. The evidence a yard IS priced says nothing about
	// its neighbour, so the decision belongs to each waypoint on its own.
	traitYards, err := p.waypoints.ListBySystemWithTrait(ctx, system, shipyardTrait)
	if err != nil {
		return nil, fmt.Errorf("failed to list shipyards in %q: %w", system, err)
	}
	seen := make(map[string]bool, len(rows)+len(traitYards))
	universe := make([]string, 0, len(rows)+len(traitYards))
	// Priced first, so the cheapest-first order the query already applied survives
	// into the evidenced half below.
	for _, row := range rows {
		if seen[row.WaypointSymbol] {
			continue
		}
		seen[row.WaypointSymbol] = true
		universe = append(universe, row.WaypointSymbol)
	}
	// Then the trait yards, in symbol order, as the fallback always returned them.
	// UNCHARTED is not yet a yard — its traits are a guess until someone charts it.
	// Note the priced half above is NOT trait-filtered: a yard we have actually
	// priced is evidenced by the reading itself, and may have no waypoint row at
	// all.
	traitOnly := make([]string, 0, len(traitYards))
	for _, waypoint := range traitYards {
		if seen[waypoint.Symbol] || hasTrait(waypoint, unchartedTrait) {
			continue
		}
		seen[waypoint.Symbol] = true
		traitOnly = append(traitOnly, waypoint.Symbol)
	}
	sort.Strings(traitOnly)
	universe = append(universe, traitOnly...)

	// THE SHARED RULE DECIDES, not this query. appSensing.ProbeYardIsCandidate is
	// the exported face of readProbeStock — the same classification the buy queue's
	// skipKnownProbeless and seed staging's stagedProbeStockAccepts consult — so a
	// yard priced and found probe-less is dropped here for exactly the reason the
	// drain would refuse it, and a STALE probe-less reading degrades to "never
	// priced" and is reconsidered. Re-deriving that in SQL is what would let the
	// three engines drift.
	//
	// EVIDENCE-FIRST ORDER COMES FROM THE UNION ABOVE, not from a second sort.
	// universe is priced rows (cheapest first) followed by trait-only yards (symbol
	// order), and each half can classify only one way: a yard drawn from a priced
	// probe row is SELLS or NONE, a trait-only yard is UNREAD or NONE. So every
	// survivor of the first half is evidence and every survivor of the second is a
	// guess, and filtering IN PLACE preserves both the ranking and the
	// cheapest-first order inside it. A partition here would re-sort nothing.
	//
	// Ranking a guess last must never mean dropping it — an unpriced yard is how
	// the fleet learns where probes are sold at all.
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

// MarketGoodsPort reads the local market cache: what a market deals in, how deep
// it is, and what it is currently quoting.
//
// All three reads hit market_data and none of them touches the API. The cache is
// exactly what a parked probe's scans populate, so these reads are how the model
// closes its own loop.
type MarketGoodsPort struct{ db *gorm.DB }

// NewMarketGoodsPort wires the market cache reads.
func NewMarketGoodsPort(db *gorm.DB) *MarketGoodsPort {
	return &MarketGoodsPort{db: db}
}

// marketRow is one persisted (waypoint, good) quote.
type marketRow struct {
	GoodSymbol    string
	PurchasePrice int
	SellPrice     int
	TradeVolume   int
}

// rowsAt reads one waypoint's cached quotes.
func (p *MarketGoodsPort) rowsAt(ctx context.Context, playerID int, waypoint string) ([]marketRow, error) {
	var rows []marketRow
	err := p.db.WithContext(ctx).
		Table("market_data").
		Select("good_symbol, purchase_price, sell_price, trade_volume").
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypoint).
		Order("good_symbol ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read the market cache at %q: %w", waypoint, err)
	}
	return rows, nil
}

// GoodsAt returns the goods a market is known to deal in, and whether we know
// anything about it at all.
//
// The bool is the whole point: a market known to trade nothing returns
// (empty, true), which is an ANSWER, while a market never scanned returns
// (nil, false), which is a gap the screen fills remotely. Collapsing the two
// would either spend an API call on every barren market forever, or record a
// never-visited one as barren.
func (p *MarketGoodsPort) GoodsAt(ctx context.Context, playerID int, waypoint string) ([]string, bool, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	goods := make([]string, 0, len(rows))
	for _, row := range rows {
		goods = append(goods, row.GoodSymbol)
	}
	return goods, true, nil
}

// DepthRowsAt returns the priced rows behind a market's goods, with the
// side-neutral integer mid-price — matching MarketDepthRows, the codebase's one
// depth convention, so a market sized here and a market sized by the census can
// never disagree.
func (p *MarketGoodsPort) DepthRowsAt(ctx context.Context, playerID int, waypoint string) ([]scouting.MarketDepthRow, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, err
	}
	out := make([]scouting.MarketDepthRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, scouting.MarketDepthRow{
			System:      shared.ExtractSystemSymbol(waypoint),
			Waypoint:    waypoint,
			Good:        row.GoodSymbol,
			TradeVolume: row.TradeVolume,
			MidPrice:    (row.PurchasePrice + row.SellPrice) / 2,
		})
	}
	return out, nil
}

// MarketPrices returns the two-sided quotes a scan just persisted, for the
// spread weighting.
//
// The column names and the field names come from opposite sides of the trade, so
// the mapping is a RENAME rather than a pass-through:
//
//	Bid ← market_data.sell_price      (what the market PAYS us — its bid)
//	Ask ← market_data.purchase_price  (what the market CHARGES us — its ask)
//
// The persisted columns are named from OUR side of the trade; GoodPrice is named
// from the MARKET's. Wiring them by name inverts every quote, and the failure is
// SILENT rather than loud: RelativeSpread's guard skips each inverted good, every
// market observes a spread of zero, the fleet median collapses to its cold-start
// fallback, and every slot lands on the same prior weight. The rotation keeps
// running and reports no error — it just stops preferring the markets worth
// watching.
//
// This mapping is CORRECT and was correct all along. It is also what DETECTED
// sp-en5h7: the scanner had been persisting the two prices transposed since the
// project's first era, so this port computed ask<bid at nearly every market and
// RelativeSpread skipped the goods. An earlier version of this comment claimed
// the resulting warning was "a symptom" and that this mapping was "the fix" —
// that sent the next reader to the wrong file. The warning was the true signal
// and the bug was in the WRITER (application/ship/market_scanner.go). Do not
// re-explain a warning from here; check what the scanner persisted.
func (p *MarketGoodsPort) MarketPrices(ctx context.Context, playerID int, waypoint string) ([]appSensing.GoodPrice, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.GoodPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, appSensing.GoodPrice{
			Good: row.GoodSymbol,
			Bid:  row.SellPrice,     // renamed, not swapped — see the doc above
			Ask:  row.PurchasePrice, // renamed, not swapped — see the doc above
		})
	}
	return out, nil
}

// marketFetchAPI is the one API verb the remote gap fill needs.
// *api.SpaceTradersClient satisfies it.
type marketFetchAPI interface {
	GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error)
}

// scanDebiter charges a market read to the fleet's one market-scan budget.
//
// It has no "may I" counterpart on purpose — see FetchGoods for why this read is
// metered but never deniable. *ship.ScanBudget satisfies it.
type scanDebiter interface {
	Debit(playerID int, waypoint string)
}

// RemoteMarketPort fills a market-cache gap from the API — the screen's only
// genuine spend, and the reason a charted-but-never-visited market can be judged
// without sending a hull to it.
type RemoteMarketPort struct {
	client marketFetchAPI
	tokens playerTokenReader
	budget scanDebiter
}

// NewRemoteMarketPort wires the remote gap fill.
func NewRemoteMarketPort(client marketFetchAPI, tokens playerTokenReader) *RemoteMarketPort {
	return &RemoteMarketPort{client: client, tokens: tokens}
}

// SetScanBudget wires the fleet market-scan budget this port charges its reads to
// (sp-ntgfj). Nil leaves the read unmetered, which is a test-only shape: the
// composition root always wires it.
func (p *RemoteMarketPort) SetScanBudget(b scanDebiter) {
	if b == nil {
		return
	}
	p.budget = b
}

// FetchGoods returns what a market DEALS IN, read remotely.
//
// It reads the goods CATALOGUE, not the priced rows. A market GET made with no
// ship at the waypoint returns the imports/exports/exchange symbol arrays but an
// EMPTY tradeGoods list, so a caller reading prices would see every unvisited
// market as trading nothing — and the screen would record that as a durable
// rejection. The catalogue survives a presence-less GET, which is exactly the
// call being made here.
func (p *RemoteMarketPort) FetchGoods(ctx context.Context, playerID int, system, waypoint string) ([]string, error) {
	token, err := playerToken(ctx, p.tokens, playerID)
	if err != nil {
		return nil, err
	}
	// Charged to the fleet market-scan budget, but never gated by it.
	// There is no store to serve this from — filling the gap is the point — and a
	// declined catalogue read makes the screen record a durable rejection of a
	// market it never managed to look at, which costs more than the request saves.
	// The spend is bounded by the rate of CHARTING rather than by the size of the
	// map, so admitting it does not put the fixed-budget invariant at risk; metering
	// it keeps the budget the honest total, since the allowance it consumes is
	// allowance discretionary scanning then cannot.
	if p.budget != nil {
		p.budget.Debit(playerID, waypoint)
	}

	market, err := p.client.GetMarket(sensingCtx(ctx), system, waypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the market at %q: %w", waypoint, err)
	}
	if market == nil {
		return nil, fmt.Errorf("the market at %q returned no data", waypoint)
	}
	return market.TradedGoodSymbols, nil
}

// gateEdgeReader is the stored jump-gate adjacency, narrowed to the one
// per-system read. *persistence.GormGateEdgeRepository satisfies it.
type gateEdgeReader interface {
	Edges(ctx context.Context, systemSymbol string) ([]domainSystem.GateEdge, bool, error)
	// AllEdges returns every era-scoped system's edges at once, keyed by system, with a
	// condemned or unread system simply ABSENT — presence is the `ok` Edges returns.
	AllEdges(ctx context.Context) (map[string][]domainSystem.GateEdge, error)
}

// GateNeighbourPort reads which systems are one jump from a given one.
//
// PER-SYSTEM, never the whole-map Adjacency read. Expansion asks this of every
// judged system on every tick, so a whole-map query would make one tick's
// topology cost scale with everything the fleet has ever charted — and it would
// grow fastest exactly as the frontier succeeds.
//
// A PURE STORE read by contract: a system missing from the store, or whose edge
// set is stale, is simply not expanded through. Wiring a fetch-through resolver
// here would spend the API budget on topology precisely where topology is least
// cached.
type GateNeighbourPort struct{ edges gateEdgeReader }

// NewGateNeighbourPort wires the stored gate adjacency.
func NewGateNeighbourPort(edges gateEdgeReader) *GateNeighbourPort {
	return &GateNeighbourPort{edges: edges}
}

// Neighbours returns the system's stored gate neighbours.
//
// A miss returns no neighbours and no error: expansion propagating through an
// unknown system is speculative work, and refusing to guess is the same rule the
// stored-distance walk follows. A genuinely old set never arrives here at all — the
// store condemns it and reports a miss.
//
// Every unusable edge is EXCLUDED FROM THE ANSWER, never allowed to erase the answer.
// An under-construction edge is impassable (a jump into an unbuilt gate fails at hop
// time) and a stale edge's build state is past verification, so both are skipped — but
// only that edge, not its siblings.
//
// Dropping the set WHOLE on any stale row is what walled off the map. Its reasoning was
// that a system's edges are written in one replace under a single timestamp, so one stale
// row condemns all — true of age, false of the SHORTER window an under-construction row is
// chased on. That row is stale on a 2h schedule while its built siblings stay perfectly
// current, so every system holding one still-building exit reported ZERO neighbours every
// 2h, became a WALL in every BFS, and made routing refuse provably-existing routes: 173 of
// 1,168 live systems, freezing 266 probes that were bought, assigned and never dispatched.
// PassableGraph returns the whole topology in one read, applying EXACTLY the filter Neighbours
// applies per system — an under-construction or per-row-stale edge is not passable — and taking
// the map's key set as the mapped set, which is exactly the `ok` Neighbours discards.
//
// Sharing the two rules with the per-system path rather than restating them is the point: a
// second copy of "passable" would drift the first time one of them learned a new exclusion, and
// the copy that got missed would be the one deciding where hulls can be sent.
func (p *GateNeighbourPort) PassableGraph(ctx context.Context) (appSensing.GateGraph, error) {
	all, err := p.edges.AllEdges(ctx)
	if err != nil {
		return appSensing.GateGraph{}, fmt.Errorf("failed to read the gate graph: %w", err)
	}
	graph := appSensing.GateGraph{
		Passable: make(map[string][]string, len(all)),
		Mapped:   make(map[string]bool, len(all)),
	}
	for symbol, edges := range all {
		// Present in AllEdges means the system's adjacency HAS been read and is not condemned,
		// which is the whole of "mapped" — including a system whose every exit is impassable.
		graph.Mapped[symbol] = true
		out := make([]string, 0, len(edges))
		for _, edge := range edges {
			if edge.ConnectedSystem == "" || edge.UnderConstruction || edge.Stale {
				continue
			}
			out = append(out, edge.ConnectedSystem)
		}
		sort.Strings(out)
		graph.Passable[symbol] = out
	}
	return graph, nil
}

func (p *GateNeighbourPort) Neighbours(ctx context.Context, system string) ([]string, error) {
	edges, ok, err := p.edges.Edges(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to read the gate neighbours of %q: %w", system, err)
	}
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(edges))
	for _, edge := range edges {
		if edge.ConnectedSystem == "" || edge.UnderConstruction || edge.Stale {
			continue
		}
		out = append(out, edge.ConnectedSystem)
	}
	sort.Strings(out)
	return out, nil
}

// Mapped reports whether we hold ANY stored gate adjacency for this system.
//
// IT READS THE `ok` THE EDGE STORE ALREADY RETURNS and Neighbours discards — the same round trip,
// answering the different question. Neighbours filters under-construction and stale edges out of its
// ANSWER while the rows remain, so "Neighbours returned nothing" cannot distinguish a system whose
// exits are all under construction (fully mapped: we know where it connects and simply cannot pass)
// from one we have never read at all. Only the second can add systems to the ledger when charted.
//
// A read failure PROPAGATES rather than defaulting. Read as mapped it would demote genuine frontier
// territory to the back of the seed queue and the fleet would quietly stop growing; read as unmapped
// it would promote every ordinary target at once.
func (p *GateNeighbourPort) Mapped(ctx context.Context, system string) (bool, error) {
	_, ok, err := p.edges.Edges(ctx, system)
	if err != nil {
		return false, fmt.Errorf("failed to read whether the gate of %q has been mapped: %w", system, err)
	}
	return ok, nil
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
	// reconcile — screen, reaper, adoption, drain, placements and expansion — every 30 seconds,
	// and its visible symptom ("0 parked, screened 0, bought 0, expansion +0 discovered") looks
	// like an idle engine rather than a broken one. Nothing else in that chain names the key,
	// says which of the many things sensing reads is missing, or hints at how it gets populated,
	// so this message must carry all three.
	headquarters, ok := domainPlayer.HeadquartersFrom(metadata)
	if !ok {
		return "", fmt.Errorf(
			"player %d has no %s in players.metadata, so the home system is unknown and sensing cannot cut over: %s",
			playerID, domainPlayer.MetadataKeyHeadquarters, domainPlayer.MissingHeadquartersHint)
	}
	return shared.ExtractSystemSymbol(headquarters), nil
}
