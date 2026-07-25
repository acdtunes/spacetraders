package persistence

import (
	"context"
	"fmt"
)

// RecordedBuiltGate reports whether gateWaypoint is ALREADY recorded as finished
// building, by a stored edge that is era-scoped and inside its freshness window.
//
// It answers only the MONOTONE direction. A jump gate goes under-construction →
// complete and never back, so a recorded-built verdict cannot go wrong within the
// era; every other case — under construction, stale, unknown, dead era, or a
// backoff marker — answers false, which sends the caller to the live probe. That
// keeps the fail-closed contract intact: this read can suppress a redundant
// confirmation, never manufacture a permissive one.
//
// The freshness bound is the same healthy-edge window Edges() applies to the same
// row, so serving the verdict from here adds no staleness the routing cache does
// not already carry.
func (r *GormGateEdgeRepository) RecordedBuiltGate(ctx context.Context, gateWaypoint string) (bool, error) {
	if gateWaypoint == "" {
		return false, nil
	}

	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where("gate_waypoint = ?", gateWaypoint).
		// Marker rows are not edges and carry no construction verdict: their
		// under_construction is a column default, not an observation.
		Where("connected_system <> ?", unreadableMarker).
		Where("under_construction = ?", false).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return false, fmt.Errorf("failed to read recorded gate construction state for %s: %w", gateWaypoint, err)
	}

	for _, m := range models {
		if !r.rowStale(m) {
			return true, nil
		}
	}
	return false, nil
}
