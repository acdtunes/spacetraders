package api

import (
	"context"
	"log"
)

// SYSTEM-level divergence only: a hull changing waypoint inside one system is ordinary
// traffic, but a changed SYSTEM is a lost cross-system write, and the planner resolves
// gates and plots BFS hops per system — a wrong system makes every hop impossible.

// shipPosition is where a hull is, or was believed to be.
type shipPosition struct {
	system   string
	waypoint string
}

// PositionReanchor is one discovery that our durable position for a hull was wrong: the
// system we BELIEVED it was in, and the system the server says it is actually in.
type PositionReanchor struct {
	ShipSymbol       string
	PlayerID         int
	BelievedSystem   string
	ActualSystem     string
	BelievedWaypoint string
	ActualWaypoint   string
}

// PositionReanchorObserver is the write-only seam the repository publishes a re-anchor on.
// It returns nothing: this is OBSERVATION and no decision may ever branch on it
// (RULINGS #4), so there is no value in the language for a decision path to read.
// Satisfied structurally, so the adapter never depends on the captain or metrics packages.
type PositionReanchorObserver interface {
	ShipPositionReanchored(ctx context.Context, reanchor PositionReanchor)
}

// SetPositionReanchorObserver installs the observer a corrected position is published on.
// Setter injection, mirroring SetArrivalScheduler: a nil observer (a minimal boot, a test,
// a wiring that has not run yet) silently disables publication rather than panicking, and
// the WARN log below still fires regardless.
func (r *ShipRepository) SetPositionReanchorObserver(observer PositionReanchorObserver) {
	r.positionReanchors = observer
}

// reportPositionReanchor is called after a sync has written a position that CONTRADICTS
// the row it replaced. It is best-effort on every surface: an alarm that panics or errors
// the sync it is reporting on is worse than the silence it exists to break.
func (r *ShipRepository) reportPositionReanchor(ctx context.Context, reanchor PositionReanchor) {
	log.Printf(
		"WARNING: POSITION RE-ANCHOR: %s was recorded in %s (%s) but the server reports it in %s (%s) — a completed move was never persisted; every tick since planned from the wrong system",
		reanchor.ShipSymbol, reanchor.BelievedSystem, reanchor.BelievedWaypoint,
		reanchor.ActualSystem, reanchor.ActualWaypoint,
	)
	if r.positionReanchors == nil {
		return
	}
	r.positionReanchors.ShipPositionReanchored(ctx, reanchor)
}

// divergedPosition reports whether a freshly-synced position contradicts the row it is
// about to replace, and describes the contradiction.
//
// It answers NO for a hull with no previous row (nothing was believed, so nothing was
// wrong) and for a row that recorded no system at all (a partially-written row is not
// evidence of a lost move). Both are fail-quiet by intent: this is an alarm, and an alarm
// that fires on absence of information is one nobody reads.
func divergedPosition(hadRow bool, believed, actual shipPosition) (PositionReanchor, bool) {
	if !hadRow || believed.system == "" || actual.system == "" {
		return PositionReanchor{}, false
	}
	if believed.system == actual.system {
		return PositionReanchor{}, false
	}
	return PositionReanchor{
		BelievedSystem:   believed.system,
		BelievedWaypoint: believed.waypoint,
		ActualSystem:     actual.system,
		ActualWaypoint:   actual.waypoint,
	}, true
}
