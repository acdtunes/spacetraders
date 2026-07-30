package parkedsensing

// surge_ports.go is the sensing surge's ONE read: which charted systems the fleet
// holds no price for (sp-zvywu Part 1).
//
// It is a SET DIFFERENCE over two local tables and nothing else — no API call, no
// per-system round trip:
//
//	charted systems (waypoints, OPEN ERA)  MINUS  systems with any market_data row
//
// THIS FILE IS THE ONE EXCEPTION TO screen_ports.go's per-system cost rule, and it
// is the same exception ListWithTraitInOpenEra already carries: the surge's whole job
// is to find the systems no system-scoped read ever reaches, so it must ask about the
// MAP rather than about one system. The cost is bounded by the count of charted
// systems in the open era (1,183 measured live) — a number that grows with what the
// fleet has explored, not with how often it ticks — and it is three grouped scalar
// reads against a local database.

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Compile-time proof this adapter still satisfies the port the surge declares.
var _ scoutingCmd.UnpricedSystemPool = (*UnpricedPoolPort)(nil)

// UnpricedPoolPort answers the surge's pool question from the persisted waypoint
// catalog and the market cache — never from the API.
type UnpricedPoolPort struct {
	db *gorm.DB
}

// NewUnpricedPoolPort wires the pool read. It takes no player: the port's method
// carries one, because the PRICED half is player-partitioned (market_data) while the
// charted half is shared.
func NewUnpricedPoolPort(db *gorm.DB) *UnpricedPoolPort {
	return &UnpricedPoolPort{db: db}
}

// UnpricedSystems returns every charted, priceable system the player holds no
// market_data for, each carrying its charted marketplace waypoints and its open-era
// waypoint count.
//
// ERA-SCOPED THROUGH persistence.OpenEraScope, AND THAT CHOICE IS THE WHOLE SAFETY
// PROPERTY. `waypoints` carries rows from every universe the agent has ever played,
// and a dead-era system 404s the moment anything contacts it. The repository's
// openEraID + eraScopePredicate pair is deliberately NOT used: its nil path resolves
// to `era_id IS NULL`, which still ANSWERS — from pre-backfill rows — and for a
// reader whose output becomes flight plans and API calls that is the wrong direction.
// sp-l0aqy is the measured bill for getting this exact call wrong on this exact
// table: ~290 API failures an hour for ten hours while utilisation sat at 88%
// against an 85% ceiling. So an unresolvable era REFUSES, and the surge reads the
// refusal as "dispatch nothing this tick".
//
// market_data IS NOT ERA-SCOPED, and it does not need to be — but the reasoning has
// to be stated rather than assumed. The table carries no era column at all; what
// keeps it honest is that the era transition TRUNCATES the player-partitioned market
// cache, and that the difference below is taken over the era-scoped CHARTED set, so a
// priced system that is not charted in the open era simply never appears on either
// side. A stale priced row can therefore only ever exclude a system that was not a
// candidate anyway.
//
// A SYSTEM WITH NO CHARTED MARKETPLACE IS NOT IN THE POOL. It is charted and it is
// unpriced, but no probe standing anywhere in it could ever produce a price, so
// ranking it would only consume a dispatch slot a priceable system needed. That
// narrows the 1,067 charted-but-unpriced systems to the actionable subset, and it is
// what lets the surge treat every candidate it is handed as immediately dispatchable
// without a follow-up read per system.
//
// UNCHARTED WAYPOINTS ARE EXCLUDED FROM THE MARKETPLACE SET, through the same
// hasTrait check ListMarketWaypoints uses. A waypoint's traits are unknown until
// somebody charts it, so an UNCHARTED row's trait list says nothing about whether it
// holds a market — including one would send a probe to a waypoint that may have no
// market at all.
func (p *UnpricedPoolPort) UnpricedSystems(ctx context.Context, playerID int) ([]scoutingCmd.UnpricedSystem, error) {
	eraPredicate, eraArgs, err := persistence.OpenEraScope(ctx, p.db)
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate the charted-but-unpriced sensing pool: %w", err)
	}

	// THE PRICED HALF, first, because it is the cheapest and it is what the other two
	// are filtered against. market_data holds one row per (waypoint, good) and carries
	// no system column, so the system is derived from the waypoint symbol exactly as
	// every other reader of this table derives it.
	var pricedWaypoints []string
	if err := p.db.WithContext(ctx).
		Table("market_data").
		Distinct("waypoint_symbol").
		Where("player_id = ?", playerID).
		Pluck("waypoint_symbol", &pricedWaypoints).Error; err != nil {
		return nil, fmt.Errorf("failed to list the systems the fleet already holds prices for: %w", err)
	}
	priced := make(map[string]bool, len(pricedWaypoints))
	for _, waypoint := range pricedWaypoints {
		if system := shared.ExtractSystemSymbol(waypoint); system != "" {
			priced[system] = true
		}
	}

	// THE RICHNESS HALF: how many waypoint rows each charted system holds in the open
	// era. Grouped in SQL rather than counted in Go so the read stays one row per
	// system rather than one per waypoint.
	var counts []struct {
		SystemSymbol string
		Waypoints    int
	}
	if err := p.db.WithContext(ctx).
		Table("waypoints").
		Select("system_symbol, COUNT(*) AS waypoints").
		Where(eraPredicate, eraArgs...).
		Group("system_symbol").
		Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("failed to count the charted waypoints of each open-era system: %w", err)
	}
	richness := make(map[string]int, len(counts))
	for _, row := range counts {
		if priced[row.SystemSymbol] {
			continue // already priced: not a candidate, so not worth carrying
		}
		richness[row.SystemSymbol] = row.Waypoints
	}
	if len(richness) == 0 {
		return nil, nil
	}

	// THE MARKETPLACE HALF. The trait column is a JSON array stored as text, matched
	// with the same LIKE pattern every other trait read in this package uses; the
	// UNCHARTED exclusion is applied in Go through hasTrait so this read and
	// ListMarketWaypoints cannot disagree about what "charted marketplace" means.
	var marketRows []struct {
		SystemSymbol   string
		WaypointSymbol string
		Traits         string
	}
	if err := p.db.WithContext(ctx).
		Table("waypoints").
		Select("system_symbol, waypoint_symbol, traits").
		Where("traits LIKE ?", fmt.Sprintf("%%\"%s\"%%", marketplaceTrait)).
		Where(eraPredicate, eraArgs...).
		Order("system_symbol ASC, waypoint_symbol ASC").
		Scan(&marketRows).Error; err != nil {
		return nil, fmt.Errorf("failed to list the marketplaces of the open-era systems: %w", err)
	}

	// Assembled in the order the rows arrived, which is sorted by system and then by
	// waypoint — so the marketplace list of each system is deterministic, and the
	// surge's "first free marketplace waypoint" pick is reproducible tick to tick.
	markets := map[string][]string{}
	order := make([]string, 0, len(richness))
	for _, row := range marketRows {
		if _, candidate := richness[row.SystemSymbol]; !candidate {
			continue // priced, or holding no open-era waypoint rows at all
		}
		if !chartedMarketplace(row.Traits) {
			continue
		}
		if len(markets[row.SystemSymbol]) == 0 {
			order = append(order, row.SystemSymbol)
		}
		markets[row.SystemSymbol] = append(markets[row.SystemSymbol], row.WaypointSymbol)
	}

	pool := make([]scoutingCmd.UnpricedSystem, 0, len(order))
	for _, system := range order {
		pool = append(pool, scoutingCmd.UnpricedSystem{
			System:          system,
			MarketWaypoints: markets[system],
			Waypoints:       richness[system],
		})
	}
	return pool, nil
}

// chartedMarketplace reports whether a waypoints.traits column names a MARKETPLACE
// and does NOT name UNCHARTED.
//
// THE SQL `LIKE` IS ONLY A PRE-FILTER; this is the authoritative test. The column is
// a JSON array stored as text, so a substring match is a heuristic — it would admit
// any future trait whose name merely contained MARKETPLACE — and the same match
// cannot express the UNCHARTED exclusion at all without a second, order-dependent
// pattern. Decoding and asking twice through hasTrait keeps one definition of what a
// trait match means, shared with ListMarketWaypoints.
//
// AN UNDECODABLE COLUMN ANSWERS NO. It is not evidence of a charted market; it is
// evidence of nothing, and the fail-closed direction here is to leave the waypoint
// out rather than send a probe to a market that may not exist.
func chartedMarketplace(raw string) bool {
	var traits []string
	if err := json.Unmarshal([]byte(raw), &traits); err != nil {
		return false
	}
	held := &shared.Waypoint{Traits: traits}
	return hasTrait(held, marketplaceTrait) && !hasTrait(held, unchartedTrait)
}
