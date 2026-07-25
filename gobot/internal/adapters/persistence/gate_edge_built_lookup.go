package persistence

import (
	"context"
	"fmt"
	"time"
)

// RecordedBuiltGate reports whether gateWaypoint was ALREADY OBSERVED to have finished
// building, by an era-scoped stored edge.
//
// It answers only the MONOTONE direction. A jump gate goes under-construction →
// complete and never back, so a finished build is a PERMANENT fact and age cannot
// invalidate it. This is deliberately NOT bounded by the edge freshness window: that
// window is the "is this edge set current" signal, and it is the very condition that
// triggers a refresh — bounding the verdict by it would reject exactly the rows whose
// refresh the verdict exists to spare, leaving the probe to suppress only reads nobody
// was going to make. Era scoping is what contains the universe reset, where gates do
// go back to unbuilt.
//
// Every other case answers false and sends the caller to the live probe: still under
// construction (the mutable verdict), unknown, dead era, or a backoff marker.
//
// The one age-like check that remains is OBSERVATION, not freshness. A row with no
// parseable synced_at was never probed — its under_construction is the column default
// (false), and the migration that introduced the column blanks synced_at precisely so
// such rows are re-probed before routing trusts them. Serving those would route a hull
// through a gate whose build state was never read, so they are refused.
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
		if constructionStateWasObserved(m) {
			return true, nil
		}
	}
	return false, nil
}

// constructionStateWasObserved reports whether a row's under_construction value came
// from a real probe rather than the column default. A probe failure is recorded as
// under-construction (fail closed), so an observed false is always a live read that saw
// the gate finished. The timestamp is used only as evidence THAT the row was written by
// a probe — never how recently, since a finished build does not expire.
func constructionStateWasObserved(m GateEdgeModel) bool {
	if m.SyncedAt == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339, m.SyncedAt)
	return err == nil
}
