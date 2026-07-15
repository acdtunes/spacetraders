package capacity

import (
	"context"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

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
	}
	err := s.db.WithContext(ctx).
		Table("ships").
		Select("ship_symbol, dedicated_fleet, location_symbol, container_id").
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
