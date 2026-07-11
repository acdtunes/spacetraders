package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// C1 (sp-64je) — planner-visible stock, consumer side. The buy-side mirror of
// tour_deposit_candidates.go: factory output stocked in a warehouse at a recorded
// cost basis is offered to the tour solver as a zero-ask-at-basis WITHDRAWAL source
// (a StockSource), so the solver can draw the good from stock at basis instead of
// buying our own output at the laddered market ask. A good with no recorded basis
// (contract/gas stock, or nothing deposited yet) is not offered — fail closed, the
// tour buys at market unchanged.

// StockSourceReader reads warehouse stock and its recorded cost basis. Satisfied
// by *storage.InMemoryStorageCoordinator (GetCostBasis is not on the narrower
// domain StorageCoordinator interface, so callers type-assert to this).
type StockSourceReader interface {
	GetTotalCargoAvailable(operationID, goodSymbol string) int
	GetCostBasis(operationID, goodSymbol string) (int, bool)
}

// StockReservationReader reports outstanding cross-tour reservations of warehouse
// stock so BuildStockSources can net them out of the offered units (C1 reservation,
// sp-64je). nil (the default) means no netting — the physical TryReserveCargo at
// withdrawal is the execution-time backstop.
type StockReservationReader interface {
	OutstandingStock(waypoint, good string) int
}

// BuildStockSources assembles the planner-visible-stock withdrawal offers from the
// running warehouse operations inside the tour graph. Each (storage waypoint, good)
// with a KNOWN cost basis and available units becomes one StockSource priced at
// basis, its units net of any outstanding cross-tour reservations. Co-located
// warehouse ops that stock the same good are aggregated (summed units, units-weighted
// basis), mirroring the deposit path's additive-capacity handling (sp-5q2c).
func BuildStockSources(
	ctx context.Context,
	warehouseFinder WarehouseOperationFinder,
	reader StockSourceReader,
	reservations StockReservationReader,
	allowedSystems []string,
	playerID int,
) []routing.TourStockSource {
	if warehouseFinder == nil || reader == nil {
		return nil
	}
	ops, err := warehouseFinder.FindRunning(ctx, playerID)
	if err != nil {
		return nil
	}

	type stockKey struct{ waypoint, good string }
	agg := map[stockKey]*routing.TourStockSource{}
	order := make([]stockKey, 0)

	for _, op := range RunningWarehousesInGraph(ops, allowedSystems) {
		for _, good := range op.SupportedGoods() {
			basis, known := reader.GetCostBasis(op.ID(), good)
			if !known {
				continue // no recorded basis — not a withdrawal source (fail closed)
			}
			units := reader.GetTotalCargoAvailable(op.ID(), good)
			if units <= 0 {
				continue
			}
			k := stockKey{op.WaypointSymbol(), good}
			if existing, ok := agg[k]; ok {
				total := existing.UnitsAvailable + units
				existing.UnitAsk = (existing.UnitAsk*existing.UnitsAvailable + basis*units) / total
				existing.UnitsAvailable = total
			} else {
				agg[k] = &routing.TourStockSource{
					Good:            good,
					UnitsAvailable:  units,
					UnitAsk:         basis,
					StorageWaypoint: op.WaypointSymbol(),
					StorageSystem:   shared.ExtractSystemSymbol(op.WaypointSymbol()),
				}
				order = append(order, k)
			}
		}
	}

	sources := make([]routing.TourStockSource, 0, len(order))
	for _, k := range order {
		s := *agg[k]
		if reservations != nil {
			// Net outstanding cross-tour reservations so parallel tours don't
			// double-claim the same units (mirrors assembleAbsorption netting).
			s.UnitsAvailable -= reservations.OutstandingStock(s.StorageWaypoint, s.Good)
		}
		if s.UnitsAvailable <= 0 {
			continue
		}
		sources = append(sources, s)
	}
	return sources
}
