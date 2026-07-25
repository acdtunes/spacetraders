package persistence

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// StoredGateWaypoint returns the gate WAYPOINT to jump to when leaving fromSystem for
// toSystem, taken from the stored, era-scoped, still-fresh edge the router already
// recorded for that pair. ok is false on a miss, a stale row, or a dead era, which
// sends the caller to the live read.
//
// The value is a symbol translation over static topology: the edge row was written from
// the origin gate's own connections list, so it is the same string the live jump-gate
// read returns. The lookup is DIRECTED — an edge is stored per (system, connected
// system), and the reverse direction is a different row with a different gate.
func (r *GormGateEdgeRepository) StoredGateWaypoint(ctx context.Context, fromSystem, toSystem string) (string, bool, error) {
	// An empty destination is what a negative-result BACKOFF MARKER stores in
	// connected_system, and a marker's gate_waypoint is the UNREADABLE ORIGIN gate —
	// never somewhere to jump to. Rejecting the empty destination here is what keeps
	// those rows unmatchable by this lookup.
	if fromSystem == "" || toSystem == "" {
		return "", false, nil
	}

	var model GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Where("system_symbol = ? AND connected_system = ?", fromSystem, toSystem).
		Where(predicate, args...).
		First(&model).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to read stored gate waypoint for %s->%s: %w", fromSystem, toSystem, err)
	}

	if r.rowStale(model) || model.GateWaypoint == "" {
		return "", false, nil
	}
	return model.GateWaypoint, true, nil
}
