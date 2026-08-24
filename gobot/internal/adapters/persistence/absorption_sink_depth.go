package persistence

import (
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// SetSinkDepthScaling replaces the depth-conditioned crush prior every read path applies to
// EXECUTED recovery shadows. Boot-time wiring: the shared ledger takes the operator's resolved
// policy before any container reads it, and a ledger left alone runs the shipped fit.
func (r *AbsorptionLedgerGORM) SetSinkDepthScaling(policy absorption.SinkDepthScaling) {
	r.depth = policy
}

// sinkBreadthFor resolves the listing breadth — how many goods the cached market trades — of every
// sink the given rows hold an EXECUTED shadow on.
//
// It returns nil, which every caller reads as "breadth unknown" and therefore as the uniform prior,
// when the prior is disabled, when no shadow needs one, or when the market cache cannot be read. A
// disabled prior costs no query at all.
func (r *AbsorptionLedgerGORM) sinkBreadthFor(db *gorm.DB, playerID int, rows []MarketAbsorptionLedgerModel) map[string]int {
	if !r.depth.Enabled {
		return nil
	}
	seen := map[string]struct{}{}
	waypoints := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].State != absorptionStateExecuted {
			continue
		}
		if _, dup := seen[rows[i].Waypoint]; dup {
			continue
		}
		seen[rows[i].Waypoint] = struct{}{}
		waypoints = append(waypoints, rows[i].Waypoint)
	}
	if len(waypoints) == 0 {
		return nil
	}
	return marketListingBreadth(db, playerID, waypoints)
}

// marketListingBreadth counts the goods each named market lists in ONE grouped read, player-scoped
// so another fleet's cache can never widen ours. An uncached waypoint is ABSENT from the result and
// a read error returns nil: absence is "breadth unknown", which every caller keeps at the uniform
// prior rather than discounting.
func marketListingBreadth(db *gorm.DB, playerID int, waypoints []string) map[string]int {
	var counted []struct {
		WaypointSymbol string
		Listings       int
	}
	if err := db.Model(&MarketData{}).
		Select("waypoint_symbol, COUNT(*) AS listings").
		Where("player_id = ? AND waypoint_symbol IN ?", playerID, waypoints).
		Group("waypoint_symbol").
		Scan(&counted).Error; err != nil {
		return nil
	}
	breadth := make(map[string]int, len(counted))
	for _, row := range counted {
		breadth[row.WaypointSymbol] = row.Listings
	}
	return breadth
}
