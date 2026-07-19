package capacity

import (
	"context"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

// roleLightHauler is the registration role of a LIGHT hauling hull — the same "HAULER"
// tag FindIdleLightHaulers / the autosizer / the depot reserve all key light-hauler
// candidacy on. The staging count uses it to count ONLY light haulers toward the tier:
// the COMMAND frigate (role "COMMAND") is the last-resort command ship, not a light
// hauler (Admiral: "a light hauler is a light hauler"), and a probe/satellite is another
// role entirely — neither lifts the hauler tier (sp-cr2v).
const roleLightHauler = "HAULER"

// isFreeContractHauler reports whether a light hauler's dedication puts it in the FREE
// contract-fulfillment pool the hauler tier measures (sp-cr2v / sp-u5nh): undedicated —
// where the autosizer's light / contract-delivery buys land (autosizerDedicatedFleet tags
// them "") — or the "contract" fleet (the literal matches assignment.dedicatedFleetContract,
// which the domain layer cannot import). A hull already committed to a DEPOT role
// (depot-delivery / warehouse / stocker) is the depot the tier GATES, not the free pool that
// must bind first, so it is NOT counted; trade / manufacturing dedications are other ops.
func isFreeContractHauler(dedicatedFleet string) bool {
	return dedicatedFleet == "" || dedicatedFleet == "contract"
}

// countContractHaulers counts the contract op's FREE LIGHT haulers — the staging-gate input
// the planner reads as EconomicsSignals.ContractHaulerCount. A hull counts iff it is the
// LIGHT-hauler class (role "HAULER" — excludes the COMMAND frigate + probes/satellites),
// cargo-capable, AND in the free contract-fulfillment pool (undedicated or "contract").
// Deadlock-safe: below the tier the reconciler desires NOTHING (ComputeDesired), so it never
// adopts a free hauler into a depot role that would drop the tally — the pool only grows (the
// autosizer's light class buys as treasury clears the 25% guard) until it binds the tier.
func countContractHaulers(hulls []domainCapacity.HullUtilization) int {
	count := 0
	for _, hull := range hulls {
		if hull.Role != roleLightHauler {
			continue
		}
		if hull.CargoCapacity < domainCapacity.MinReuseCargoCapacity {
			continue
		}
		if isFreeContractHauler(hull.DedicatedFleet) {
			count++
		}
	}
	return count
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
		Role           string
	}
	err := s.db.WithContext(ctx).
		Table("ships").
		Select("ship_symbol, dedicated_fleet, location_symbol, container_id, cargo_capacity, role").
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
			// Role gates the hauler-tier count to the LIGHT-hauler class only
			// (excludes the COMMAND frigate) — see countContractHaulers.
			Role: row.Role,
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
