package capacity

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// senseEconomics collects the GOVERN phase's capital inputs. Every read fails
// CLOSED: a failing treasury reads as 0 credits (never phantom capital), a
// failing ledger reads as 0 velocity, and a good with no known source market
// gets NO distance row (mirroring the demand miner's fail-closed drop).
// FleetHullCount is derived from the SAME hull set Utilization reports, so the
// two can never diverge.
func (s *Sensor) senseEconomics(ctx context.Context, playerID int, demand domainCapacity.DemandSignals, topology domainCapacity.TopologySignals, hullCount int) domainCapacity.EconomicsSignals {
	economics := domainCapacity.EconomicsSignals{
		TreasuryCredits:       s.liveTreasuryCredits(ctx, playerID),
		IncomeVelocityPerHour: s.incomeVelocityPerHour(ctx, playerID),
		FleetHullCount:        hullCount,
		SourceDistances:       s.sourceDistances(ctx, playerID, demand),
		StockerLoad:           stockerLoad(topology),
	}
	if hullCount > 0 {
		economics.FleetPerHullCrHr = economics.IncomeVelocityPerHour / float64(hullCount)
	}
	return economics
}

// liveTreasuryCredits reads the agent's live credit balance — the sensor's one
// live-API touch. Any failure is 0 credits: the governor must never see
// phantom capital.
func (s *Sensor) liveTreasuryCredits(ctx context.Context, playerID int) int64 {
	if s.treasury == nil {
		s.note("economics.treasury", fmt.Errorf("no treasury reader wired"))
		return 0
	}
	id, err := shared.NewPlayerID(playerID)
	if err != nil {
		s.note("economics.treasury", err)
		return 0
	}
	credits, err := s.treasury.LiveCredits(ctx, id)
	if err != nil {
		s.note("economics.treasury", err)
		return 0
	}
	return int64(credits)
}

// incomeVelocityPerHour is the net ledger sum over the trailing income window,
// normalized to credits/hour — the same trailing-window net-profit read the
// bootstrap harness's income probe uses.
func (s *Sensor) incomeVelocityPerHour(ctx context.Context, playerID int) float64 {
	cutoff := s.clock.Now().Add(-s.incomeWindow)
	var row struct{ Total float64 }
	err := s.db.WithContext(ctx).
		Table("transactions").
		Select("COALESCE(SUM(amount), 0) AS total").
		Where("player_id = ? AND timestamp > ?", playerID, cutoff).
		Scan(&row).Error
	if err != nil {
		s.note("economics.income", err)
		return 0
	}
	hours := s.incomeWindow.Hours()
	if hours <= 0 {
		return 0
	}
	return row.Total / hours
}

// sourceDistances resolves, for every good in every demand hub's mix, the
// distance from the hub to the good's nearest known source market: cheapest
// in-system market → Euclidean waypoint distance; a good sold only in OTHER
// systems → the coarse cross-system tier (gate hops dominate, not Euclidean
// units); a good sold nowhere → no row.
func (s *Sensor) sourceDistances(ctx context.Context, playerID int, demand domainCapacity.DemandSignals) []domainCapacity.GoodSourceDistance {
	if len(demand.Hubs) == 0 {
		return nil
	}
	markets := persistence.NewMarketRepository(s.db)
	coords := newCoordinateCache(s)

	var out []domainCapacity.GoodSourceDistance
	for _, hub := range demand.Hubs {
		systemSymbol := shared.ExtractSystemSymbol(hub.HubSymbol)
		for _, good := range hub.GoodMix {
			source, err := markets.FindCheapestMarketSelling(ctx, good.Good, systemSymbol, playerID)
			if err != nil {
				s.note("economics.distances", err)
				continue
			}
			if source == nil {
				if s.goodSoldAnywhere(ctx, playerID, good.Good) {
					out = append(out, domainCapacity.GoodSourceDistance{HubSymbol: hub.HubSymbol, Good: good.Good, Distance: s.crossSystemDistance})
				}
				continue
			}
			distance, ok := coords.distance(ctx, hub.HubSymbol, source.WaypointSymbol)
			if !ok {
				continue // unknown geometry: no fake denominator
			}
			out = append(out, domainCapacity.GoodSourceDistance{HubSymbol: hub.HubSymbol, Good: good.Good, Distance: distance})
		}
	}
	return out
}

// goodSoldAnywhere reports whether ANY scanned market sells the good. Scouts
// only scan systems the fleet can reach, so market_data presence doubles as
// the reachability filter.
func (s *Sensor) goodSoldAnywhere(ctx context.Context, playerID int, good string) bool {
	var count int64
	err := s.db.WithContext(ctx).
		Table("market_data").
		Where("player_id = ? AND good_symbol = ?", playerID, good).
		Count(&count).Error
	if err != nil {
		s.note("economics.distances", err)
		return false
	}
	return count > 0
}

// stockerLoad reports, per cluster hub, how many CREWED stocker slots serve it.
// LoadPct stays 0: no persisted stocker-utilization source exists yet
// (documented gap; the planner falls back to its stocker-capacity budget).
func stockerLoad(topology domainCapacity.TopologySignals) []domainCapacity.StockerLoad {
	if len(topology.Clusters) == 0 {
		return nil
	}
	out := make([]domainCapacity.StockerLoad, 0, len(topology.Clusters))
	for _, cluster := range topology.Clusters {
		crewed := 0
		for _, stocker := range cluster.Stockers {
			if stocker.ShipSymbol != "" {
				crewed++
			}
		}
		out = append(out, domainCapacity.StockerLoad{HubSymbol: cluster.HubSymbol, ActiveStockers: crewed})
	}
	return out
}

// coordinateCache memoizes waypoint coordinate reads within one Sense pass.
// Coordinates are read straight off the waypoints row by symbol (the table's
// sole primary key): like waypoint traits, coordinates are the immutable
// physical fact of the row, so the era-scope and 24h TTL gates that protect
// VOLATILE waypoint reads would only starve the distance signal (see
// GormWaypointRepository.HasWaypointTrait's reasoning).
type coordinateCache struct {
	sensor *Sensor
	known  map[string]waypointCoordinate
}

type waypointCoordinate struct {
	x, y float64
	ok   bool
}

func newCoordinateCache(sensor *Sensor) *coordinateCache {
	return &coordinateCache{sensor: sensor, known: make(map[string]waypointCoordinate)}
}

func (c *coordinateCache) distance(ctx context.Context, fromSymbol, toSymbol string) (float64, bool) {
	from := c.lookup(ctx, fromSymbol)
	to := c.lookup(ctx, toSymbol)
	if !from.ok || !to.ok {
		return 0, false
	}
	return math.Hypot(to.x-from.x, to.y-from.y), true
}

func (c *coordinateCache) lookup(ctx context.Context, symbol string) waypointCoordinate {
	if cached, hit := c.known[symbol]; hit {
		return cached
	}
	var rows []struct{ X, Y float64 }
	err := c.sensor.db.WithContext(ctx).
		Table("waypoints").
		Select("x, y").
		Where("waypoint_symbol = ?", symbol).
		Limit(1).
		Scan(&rows).Error
	coordinate := waypointCoordinate{}
	if err != nil {
		c.sensor.note("economics.distances", err)
	}
	if err == nil && len(rows) == 1 {
		coordinate = waypointCoordinate{x: rows[0].X, y: rows[0].Y, ok: true}
	}
	c.known[symbol] = coordinate
	return coordinate
}
