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

	"gorm.io/gorm"

	domainPlayer "github.com/andrescamacho/spacetraders-go/internal/domain/player"

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
// producing trade data while the tour continues. The old flat alphabetical order
// left both to chance, which is how a seed comes to spend fifty hours on
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
// Two sources, in order:
//
//  1. shipyard_inventory rows offering SHIP_PROBE, ordered by the price last
//     scanned. These are yards we have priced and can rank.
//  2. failing that, the system's bare SHIPYARD-trait waypoints, as UNPRICED
//     candidates. A yard nobody has scanned still sells probes; excluding it
//     would leave a system with exactly one, never-visited shipyard permanently
//     unbuyable, and the drain prices every candidate live before it spends
//     anyway.
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
// NOT ERA-SCOPED, and that is a KNOWN INCONSISTENCY rather than a decision (sp-fwk8z T3 review
// Minor 4). The sibling heavy read behind the reservation — ShipyardInventoryRepositoryGORM's
// CheapestPricedYard — IS era-scoped, so the two answer "which yards sell heavies" under different
// era rules and a stale pre-reset row can still plan a quartermaster here. It is left unaligned
// deliberately: ListProbeYards beside it is equally unscoped, so era-scoping only THIS read would
// trade a cross-package inconsistency for a worse one inside this file. The pair should be aligned
// together, which is a change to this file's local convention and belongs in its own pass.
//
// Unlike ListProbeYards there is NO bare-SHIPYARD-trait fallback. An unscanned
// shipyard is already returned by ListProbeYards' fallback and therefore already
// earns a quartermaster; claiming an unscanned yard as heavy-selling as well
// would assert something we have not observed. Only PRICED heavy rows count
// here, so this list is evidence, never assumption.
func (p *WaypointCatalogPort) ListHeavyYards(ctx context.Context, system string) ([]string, error) {
	var rows []struct {
		WaypointSymbol string
		PurchasePrice  int
	}
	err := p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("waypoint_symbol, purchase_price").
		Where("player_id = ? AND system_symbol = ? AND ship_type IN ?", p.playerID, system, shipyardDomain.DefaultHeavyShipTypes).
		Where("purchase_price > 0").
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

func (p *WaypointCatalogPort) ListProbeYards(ctx context.Context, system string) ([]string, error) {
	var rows []struct {
		WaypointSymbol string
		PurchasePrice  int
	}
	err := p.db.WithContext(ctx).
		Table("shipyard_inventory").
		Select("waypoint_symbol, purchase_price").
		Where("player_id = ? AND system_symbol = ? AND ship_type = ?", p.playerID, system, probeShipType).
		Order("purchase_price ASC, waypoint_symbol ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	if len(rows) > 0 {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.WaypointSymbol)
		}
		return out, nil
	}

	waypoints, err := p.waypoints.ListBySystemWithTrait(ctx, system, shipyardTrait)
	if err != nil {
		return nil, fmt.Errorf("failed to list shipyards in %q: %w", system, err)
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
// THE COLUMN MAPPING IS CROSSED, AND MUST STAY CROSSED:
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
// watching. The scanner logs a warning when it sees inverted quotes, but a
// warning is a symptom; this mapping is the fix.
func (p *MarketGoodsPort) MarketPrices(ctx context.Context, playerID int, waypoint string) ([]appSensing.GoodPrice, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.GoodPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, appSensing.GoodPrice{
			Good: row.GoodSymbol,
			Bid:  row.SellPrice,     // crossed on purpose — see the doc above
			Ask:  row.PurchasePrice, // crossed on purpose — see the doc above
		})
	}
	return out, nil
}

// marketFetchAPI is the one API verb the remote gap fill needs.
// *api.SpaceTradersClient satisfies it.
type marketFetchAPI interface {
	GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error)
}

// RemoteMarketPort fills a market-cache gap from the API — the screen's only
// genuine spend, and the reason a charted-but-never-visited market can be judged
// without sending a hull to it.
type RemoteMarketPort struct {
	client marketFetchAPI
	tokens playerTokenReader
}

// NewRemoteMarketPort wires the remote gap fill.
func NewRemoteMarketPort(client marketFetchAPI, tokens playerTokenReader) *RemoteMarketPort {
	return &RemoteMarketPort{client: client, tokens: tokens}
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
// stored-distance walk follows. An edge marked under-construction is impassable
// and excluded; an edge set flagged stale is dropped WHOLE, because a system's
// edges are written in one replace under a single timestamp and one stale row
// condemns the set.
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
		if edge.Stale {
			return nil, nil
		}
		if edge.ConnectedSystem == "" || edge.UnderConstruction {
			continue
		}
		out = append(out, edge.ConnectedSystem)
	}
	sort.Strings(out)
	return out, nil
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
	// sp-0eufi: this failure used to read "player N has no recorded headquarters" and surface four
	// layers up as a sensing CUTOVER refusal, which aborts the entire reconcile — screen, reaper,
	// adoption, drain, placements and expansion — every 30 seconds. Nothing in that chain named the
	// key, said which of the many things sensing reads was missing, or hinted at how it gets
	// populated, so the visible symptom ("0 parked, screened 0, bought 0, expansion +0 discovered")
	// looked like an idle engine rather than a broken one. The error now carries all three.
	headquarters, ok := domainPlayer.HeadquartersFrom(metadata)
	if !ok {
		return "", fmt.Errorf(
			"player %d has no %s in players.metadata, so the home system is unknown and sensing cannot cut over: %s",
			playerID, domainPlayer.MetadataKeyHeadquarters, domainPlayer.MissingHeadquartersHint)
	}
	return shared.ExtractSystemSymbol(headquarters), nil
}
