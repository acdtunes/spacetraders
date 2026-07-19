package capacity

import (
	"context"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

// contractHaulerFleets are the dedication tags of the contract-delivery op's cargo
// haulers — the hauler tier the reconciler's hauler-first staging gate measures
// (sp-5nd2 / sp-u5nh). "contract" is the contract-fulfillment hauler pool (the literal
// matches application/ship/commands/assignment's dedicatedFleetContract, which the domain
// layer cannot import); depot.DeliveryHullFleet is the depot delivery-hull fleet the
// reconciler dedicates an adopted worker to. FRIGATE-INCLUSIVE: any cargo-capable hull on
// these fleets counts (the command frigate on "contract" is a hauler); a LIGHT-only count
// would need a frame read (flagged in the sp-5nd2 handback).
var contractHaulerFleets = map[string]bool{
	"contract":              true,
	depot.DeliveryHullFleet: true,
}

// countContractHaulers counts the contract-delivery op's DISTINCT cargo haulers — the
// staging-gate input the planner reads as EconomicsSignals.ContractHaulerCount. Two
// sources, deduped by ship symbol: the depot delivery hulls already serving (cluster
// workers) and the cargo-capable hulls tagged to a hauler fleet (contract-fulfillment +
// adopted depot-delivery). DELIBERATELY EXCLUDES the undedicated idle reuse pool: the
// reconciler consumes it into roles each tick, so counting it would drop the tally the
// moment an idle hull is reassigned to a warehouse and thrash the gate. Deadlock-safe:
// contract-delivery hulls are bought UNDEDICATED (autosizerDedicatedFleet default), but
// staged mode has only worker gaps, so the reconciler dedicates each adopted idle hull to
// depot-delivery — where this count then sees it — before any warehouse is desired.
func countContractHaulers(hulls []domainCapacity.HullUtilization, topology domainCapacity.TopologySignals) int {
	haulers := map[string]bool{}
	for _, cluster := range topology.Clusters {
		for _, worker := range cluster.Workers {
			haulers[worker.ShipSymbol] = true
		}
	}
	for _, hull := range hulls {
		if hull.CargoCapacity < domainCapacity.MinReuseCargoCapacity {
			continue
		}
		if contractHaulerFleets[hull.DedicatedFleet] {
			haulers[hull.ShipSymbol] = true
		}
	}
	return len(haulers)
}

// senseUtilization projects the player's ships rows into per-hull utilization.
// Idle is exactly "no container is flying the hull" (ships.container_id empty),
// matching the duty-cycle sampler's Earning definition. DutyCyclePct comes from
// the in-memory KPI seam and is 0 for hulls the sampler has not observed
// (sampling starts at daemon boot — no persisted history exists).
func (s *Sensor) senseUtilization(ctx context.Context, playerID int) domainCapacity.UtilizationSignals {
	var rows []struct {
		ShipSymbol     string
		DedicatedFleet string
		LocationSymbol string
		ContainerID    *string
		CargoCapacity  int
	}
	err := s.db.WithContext(ctx).
		Table("ships").
		Select("ship_symbol, dedicated_fleet, location_symbol, container_id, cargo_capacity").
		Where("player_id = ?", playerID).
		Order("ship_symbol").
		Scan(&rows).Error
	if err != nil {
		s.note("utilization", err)
		return domainCapacity.UtilizationSignals{}
	}
	if len(rows) == 0 {
		return domainCapacity.UtilizationSignals{}
	}

	dutyPct := s.dutyCyclePctByHull()
	hulls := make([]domainCapacity.HullUtilization, 0, len(rows))
	for _, row := range rows {
		hulls = append(hulls, domainCapacity.HullUtilization{
			ShipSymbol:     row.ShipSymbol,
			DedicatedFleet: row.DedicatedFleet,
			Waypoint:       row.LocationSymbol,
			DutyCyclePct:   dutyPct[row.ShipSymbol],
			Idle:           row.ContainerID == nil || *row.ContainerID == "",
			// The reuse-first tier reassigns only into cargo-required hauling
			// roles, so the ladder + SENSE filter exclude a below-floor hull
			// (0-cargo probe/satellite) from reuse — see MinReuseCargoCapacity.
			CargoCapacity: row.CargoCapacity,
		})
	}
	return domainCapacity.UtilizationSignals{Hulls: hulls}
}

// dutyCyclePctByHull reads the duty-cycle KPI seam into a hull→pct lookup.
func (s *Sensor) dutyCyclePctByHull() map[string]float64 {
	if s.dutyCycleReport == nil {
		return nil
	}
	var report dutycycle.Report = s.dutyCycleReport()
	out := make(map[string]float64, len(report.Hulls))
	for _, hull := range report.Hulls {
		out[hull.Hull] = hull.EarningPct
	}
	return out
}
