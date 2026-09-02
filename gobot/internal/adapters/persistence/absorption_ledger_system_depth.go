package persistence

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

var _ mvt.SystemDepthReader = (*AbsorptionLedgerGORM)(nil)

// SystemDepths joins every priced market-good in the requested systems with the ledger's
// outstanding occupancy (PLANNED reservations and recovering EXECUTED residuals) on both
// sides. It is the MVT ranker's only view of "not currently being explored". Read-only.
func (r *AbsorptionLedgerGORM) SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]mvt.LaneDepth, error) {
	if len(systems) == 0 {
		return map[string][]mvt.LaneDepth{}, nil
	}
	occupancy, err := r.Outstanding(ctx, playerID)
	if err != nil {
		return nil, err
	}
	prefix := r.db.WithContext(ctx)
	for i, s := range systems {
		if i == 0 {
			prefix = prefix.Where("waypoint_symbol LIKE ?", s+"-%")
		} else {
			prefix = prefix.Or("waypoint_symbol LIKE ?", s+"-%")
		}
	}
	var rows []MarketData
	if err := r.db.WithContext(ctx).Where("player_id = ?", playerID).Where(prefix).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read market depth for %d systems: %w", len(systems), err)
	}
	wanted := make(map[string]bool, len(systems))
	for _, s := range systems {
		wanted[s] = true
	}
	out := make(map[string][]mvt.LaneDepth, len(systems))
	for _, m := range rows {
		sys := shared.ExtractSystemSymbol(m.WaypointSymbol)
		if !wanted[sys] {
			continue
		}
		listing := trading.GoodListing{
			Good: m.GoodSymbol, Waypoint: m.WaypointSymbol, TradeType: derefString(m.TradeType),
			Bid: m.SellPrice, Ask: m.PurchasePrice, Supply: derefString(m.Supply), Activity: derefString(m.Activity),
			Volume: m.TradeVolume, ObservedAt: m.LastUpdated,
		}
		buy := occupancy[absorption.LaneKey{Waypoint: m.WaypointSymbol, Good: m.GoodSymbol, Side: absorption.SideBuy}]
		sell := occupancy[absorption.LaneKey{Waypoint: m.WaypointSymbol, Good: m.GoodSymbol, Side: absorption.SideSell}]
		out[sys] = append(out[sys], mvt.LaneDepth{
			Listing:      listing,
			BuyPlanned:   buy.PlannedUnits,
			BuyResidual:  buy.RecoveringResidual,
			SellPlanned:  sell.PlannedUnits,
			SellResidual: sell.RecoveringResidual,
		})
	}
	return out, nil
}
