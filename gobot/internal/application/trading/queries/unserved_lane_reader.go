package queries

import (
	"context"
	"strconv"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tradeFleetTag is the dedication a hull carries when it belongs to the trade-tour pool.
const tradeFleetTag = "trade"

// ProfitableLaneCounter counts the profitable, feasible lanes ranked across the given systems,
// read-only, off the persisted market cache. Satisfied by ProfitableLaneReader.
type ProfitableLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (census LaneCensus, readable bool, err error)
}

// UnservedLaneReader surfaces the trade solver's profitable-but-unflown lane count: the number
// of profitable, feasible lanes the player's trading grounds rank BEYOND the current trade-hull
// pool. That surplus IS the capacity-short signal.
//
// ONE INSTANCE, TWO CONSUMERS. The fleet-growth coordinator's heavy demand and the sensing drain's
// wave gate both read this. It is constructed once at the composition root precisely so a second one
// is conspicuous: two readers of one quantity is how a spender and a withholder end up disagreeing.
//
// READ-ONLY: it never perturbs the trade coordinator — it consumes the same pure ranking off
// the market cache that the trade circuit itself uses.
type UnservedLaneReader struct {
	ships navigation.ShipRepository
	lanes ProfitableLaneCounter
}

// NewUnservedLaneReader wires the capacity-short signal over its two read sources.
func NewUnservedLaneReader(ships navigation.ShipRepository, lanes ProfitableLaneCounter) *UnservedLaneReader {
	return &UnservedLaneReader{ships: ships, lanes: lanes}
}

// UnservedLaneCount reports profitable lanes beyond the trade pool, floored at 0.
//
// It fails CLOSED (readable=false) on a genuine ship or market read failure: acting on a capacity
// signal nobody could read is the runaway the guard stacks exist to prevent. A READABLE zero — an
// empty cache, or no lane clearing the floor — is a genuine zero and reported as such, or a fleet
// with nothing to fly is indistinguishable from one whose sensors are down.
func (r *UnservedLaneReader) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	label := strconv.Itoa(playerID)
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, false, nil
	}
	ships, err := r.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{})
		return 0, false, err
	}
	systems := distinctShipSystems(ships)
	pool := 0
	for _, sh := range ships {
		if sh.DedicatedFleet() == tradeFleetTag {
			pool++
		}
	}
	census, readable, err := r.lanes.CountProfitableLanes(ctx, playerID, systems)
	if err != nil || !readable {
		metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{TradePool: pool, SystemsScanned: len(systems)})
		return 0, false, err
	}
	unserved := census.Profitable - pool
	if unserved < 0 {
		unserved = 0
	}
	// The census terms travel UNCHANGED: they explain the count taken on this tick, or nothing.
	metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{
		Unserved:            unserved,
		Profitable:          census.Profitable,
		TradePool:           pool,
		SystemsScanned:      len(systems),
		CrossSystem:         census.CrossSystem,
		AbsorbableUnits:     census.AbsorbableUnits,
		RejectedUnreachable: census.RejectedUnreachable,
		RejectedJumpCost:    census.RejectedJumpCost,
		Readable:            true,
	})
	return unserved, true, nil
}

// distinctShipSystems returns the distinct systems the player's hulls stand in — the trading
// grounds the lane count scans.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}
