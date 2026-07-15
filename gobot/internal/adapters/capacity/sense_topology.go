package capacity

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// senseTopology projects the persisted contract depots into cluster states:
// one cluster per depot, hub = the depot's anchor (first) warehouse waypoint.
// Warehouse buffers are EVENT-SOURCED — Σ warehouse_stockings − Σ
// warehouse_withdrawals per (waypoint, good) — because a stationary depot
// hull's cargo sync goes stale (see WarehouseStockingModel's doc). Per-good
// caps come from the ACTIVE warehouse containers' target_units config.
func (s *Sensor) senseTopology(ctx context.Context, playerID int) domainCapacity.TopologySignals {
	depots, err := persistence.NewGormContractDepotRepository(s.db, playerID).List(ctx)
	if err != nil {
		s.note("topology", err)
		return domainCapacity.TopologySignals{}
	}
	if len(depots) == 0 {
		return domainCapacity.TopologySignals{}
	}

	buffers, err := s.loadWarehouseBuffers(ctx, playerID)
	if err != nil {
		s.note("topology.buffers", err)
		buffers = nil
	}
	caps, err := s.loadWarehouseCaps(ctx, playerID)
	if err != nil {
		s.note("topology.caps", err)
		caps = nil
	}

	sort.Slice(depots, func(i, j int) bool { return depots[i].ID() < depots[j].ID() })
	clusters := make([]domainCapacity.ClusterState, 0, len(depots))
	for _, d := range depots {
		warehouses := d.Warehouses()
		cluster := domainCapacity.ClusterState{
			HubSymbol: warehouses[0].Waypoint, // the depot's anchor (>=1 invariant)
			Stockers:  stockerStates(d.Stockers()),
			Workers:   workerStates(d.DeliveryHulls()),
		}
		for _, wh := range warehouses {
			cluster.Warehouses = append(cluster.Warehouses, domainCapacity.WarehouseState{
				ShipSymbol: wh.ShipSymbol,
				Waypoint:   wh.Waypoint,
				Buffer:     bufferedStock(buffers[wh.Waypoint]),
				GoodCaps:   matchWarehouseCaps(caps, wh),
			})
		}
		clusters = append(clusters, cluster)
	}
	return domainCapacity.TopologySignals{Clusters: clusters}
}

// loadWarehouseBuffers nets stockings against withdrawals per (waypoint, good).
func (s *Sensor) loadWarehouseBuffers(ctx context.Context, playerID int) (map[string]map[string]int, error) {
	type sumRow struct {
		Waypoint string
		Good     string
		Units    int
	}
	var stocked []sumRow
	if err := s.db.WithContext(ctx).
		Table("warehouse_stockings").
		Select("warehouse_waypoint AS waypoint, good, SUM(units) AS units").
		Where("player_id = ?", playerID).
		Group("warehouse_waypoint, good").
		Scan(&stocked).Error; err != nil {
		return nil, err
	}
	var withdrawn []sumRow
	if err := s.db.WithContext(ctx).
		Table("warehouse_withdrawals").
		Select("waypoint, good, SUM(units) AS units").
		Where("player_id = ?", playerID).
		Group("waypoint, good").
		Scan(&withdrawn).Error; err != nil {
		return nil, err
	}

	buffers := make(map[string]map[string]int)
	for _, row := range stocked {
		if buffers[row.Waypoint] == nil {
			buffers[row.Waypoint] = make(map[string]int)
		}
		buffers[row.Waypoint][row.Good] += row.Units
	}
	for _, row := range withdrawn {
		if buffers[row.Waypoint] == nil {
			continue // a withdrawal with no matching stocking nets negative → dropped below
		}
		buffers[row.Waypoint][row.Good] -= row.Units
	}
	return buffers, nil
}

// bufferedStock renders one waypoint's net fill as a sorted, positive-only list.
func bufferedStock(fill map[string]int) []domainCapacity.BufferedStock {
	if len(fill) == 0 {
		return nil
	}
	out := make([]domainCapacity.BufferedStock, 0, len(fill))
	for good, units := range fill {
		if units <= 0 {
			continue
		}
		out = append(out, domainCapacity.BufferedStock{Good: good, Units: units})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Good < out[j].Good })
	if len(out) == 0 {
		return nil
	}
	return out
}

// warehouseCapConfig is one ACTIVE warehouse container's cap configuration.
type warehouseCapConfig struct {
	shipSymbol  string
	waypoint    string
	targetUnits map[string]int
}

// loadWarehouseCaps reads the per-good caps configured on the player's active
// (PENDING/RUNNING) warehouse containers, ordered by container ID for
// deterministic matching.
func (s *Sensor) loadWarehouseCaps(ctx context.Context, playerID int) ([]warehouseCapConfig, error) {
	var rows []struct {
		ID     string
		Config string
	}
	if err := s.db.WithContext(ctx).
		Table("containers").
		Select("id, config").
		// The domain constant ("WAREHOUSE") — never a hand-typed literal: SQLite
		// compares TEXT case-sensitively, so a casing drift here matches zero
		// production rows and degrades silently.
		Where("player_id = ? AND container_type = ? AND status IN ?", playerID,
			string(container.ContainerTypeWarehouse),
			[]string{string(container.ContainerStatusPending), string(container.ContainerStatusRunning)}).
		Order("id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	configs := make([]warehouseCapConfig, 0, len(rows))
	for _, row := range rows {
		var cfg struct {
			ShipSymbol     string         `json:"ship_symbol"`
			WaypointSymbol string         `json:"waypoint_symbol"`
			TargetUnits    map[string]int `json:"target_units"`
		}
		if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
			s.note("topology.caps."+row.ID, err)
			continue
		}
		if len(cfg.TargetUnits) == 0 {
			continue
		}
		configs = append(configs, warehouseCapConfig{shipSymbol: cfg.ShipSymbol, waypoint: cfg.WaypointSymbol, targetUnits: cfg.TargetUnits})
	}
	return configs, nil
}

// matchWarehouseCaps resolves a depot warehouse slot to its container caps:
// crewed slots match by ship symbol, uncrewed (or unmatched) slots fall back to
// the waypoint.
func matchWarehouseCaps(configs []warehouseCapConfig, warehouse depot.Element) map[string]int {
	if warehouse.ShipSymbol != "" {
		for _, cfg := range configs {
			if cfg.shipSymbol == warehouse.ShipSymbol {
				return cfg.targetUnits
			}
		}
	}
	for _, cfg := range configs {
		if cfg.waypoint == warehouse.Waypoint {
			return cfg.targetUnits
		}
	}
	return nil
}

func stockerStates(elements []depot.Element) []domainCapacity.StockerState {
	if len(elements) == 0 {
		return nil
	}
	out := make([]domainCapacity.StockerState, 0, len(elements))
	for _, e := range elements {
		out = append(out, domainCapacity.StockerState{ShipSymbol: e.ShipSymbol, Waypoint: e.Waypoint})
	}
	return out
}

func workerStates(elements []depot.Element) []domainCapacity.WorkerState {
	if len(elements) == 0 {
		return nil
	}
	out := make([]domainCapacity.WorkerState, 0, len(elements))
	for _, e := range elements {
		out = append(out, domainCapacity.WorkerState{ShipSymbol: e.ShipSymbol, Waypoint: e.Waypoint})
	}
	return out
}
