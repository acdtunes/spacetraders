package persistence

import (
	"context"
	"fmt"
)

// PruneContradictedEdges deletes every stored edge for systemSymbol that the server's
// authoritative connection set does NOT contain, and returns how many were removed.
//
// It exists because the API hands us the truth in the one place we were discarding it: a
// jump refused with code 4255 ("… is not connected to the current location") carries
// data.connections — the complete, first-hand gate set of the system the hull is really
// standing on. Formatting that into an error string and dropping it is what let a phantom
// edge be replanned into the identical impossible jump on the next tick.
//
// The contract is REMOVAL-ONLY, and that is what makes it strictly non-loosening (RULINGS #4):
//   - an edge the server's set contradicts is deleted;
//   - an edge the server's set NAMES but we do not hold is NOT created. It carries no verified
//     build state, so inserting it could authorise a route the fetch path (which probes
//     construction and fails closed) would refuse. It may only enter through that path.
//   - nothing is ever marked built, fresh, or readable.
//
// Deleting an edge can only shrink the routable graph, so this makes routing more accurate
// and can never admit a jump that would otherwise be refused.
//
// Two guards keep it honest at the boundaries:
//   - an EMPTY authoritative set is no evidence at all and deletes nothing. The jump-gate
//     endpoint is known to return incomplete/empty reads (sp-hguq3, sp-dmxy5); acting on one
//     here would erase a whole system's topology on a transient blip.
//   - the negative-result BACKOFF MARKER (connected_system = "") is not an edge — it records
//     the UNREADABLE ORIGIN gate, which no connection set will ever list. Excluding it is what
//     stops a reconcile from silently clearing the sp-ikx1 backoff and re-opening the 400-storm.
//
// Era-scoped exactly like every other read/write here, so a dead era's rows are never touched.
func (r *GormGateEdgeRepository) PruneContradictedEdges(ctx context.Context, systemSymbol string, authoritativeConnections []string) (int, error) {
	if systemSymbol == "" || len(authoritativeConnections) == 0 {
		return 0, nil
	}

	predicate, args := eraScopePredicate(r.openEraID(ctx))
	result := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		// Real edges only — the backoff marker is not a connection and must survive.
		Where("connected_system <> ?", unreadableMarker).
		Where("gate_waypoint NOT IN ?", authoritativeConnections).
		Where(predicate, args...).
		Delete(&GateEdgeModel{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to prune contradicted gate edges for %s: %w", systemSymbol, result.Error)
	}
	return int(result.RowsAffected), nil
}
