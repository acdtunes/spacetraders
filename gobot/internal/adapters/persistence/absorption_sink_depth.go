package persistence

import (
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// SetSinkDepthScaling arms the depth-conditioned crush prior every read path applies to
// EXECUTED recovery shadows. Boot-time wiring: the shared ledger is configured before
// any container reads it, and a policy left unset keeps the uniform prior.
func (r *AbsorptionLedgerGORM) SetSinkDepthScaling(policy absorption.SinkDepthScaling) {
	r.depth = policy
}

// sinkBreadthFor resolves the listing breadth — how many goods the cached market trades
// — of every sink the given rows hold an EXECUTED shadow on, player-scoped so another
// fleet's cache can never widen ours.
//
// It returns nil, which every caller reads as "breadth unknown" and therefore as the
// uniform prior, when the policy is disarmed, when no shadow needs one, or when the
// market cache cannot be read. Disarmed costs no query at all, which is what keeps the
// unarmed path identical to the pre-refit one rather than merely equivalent.
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
