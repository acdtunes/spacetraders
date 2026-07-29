package api

import (
	"context"
	"log"
)

// ship_position_reanchor.go makes the fleet's worst silent failure audible: discovering
// that the position we had DURABLY RECORDED for a hull was wrong.
//
// Every re-anchor in this codebase runs through SyncShipFromAPI — the navigate 4204/4214
// reconciles, the cargo and delivery resyncs, the liquidation hold read, the phantom-
// position adoption, `ship refresh`, the post-purchase sync, the trade-route gate
// re-confirm. All of them GET the server's truth and write it through. None of them ever
// asked the one question that matters afterwards: was what we replaced actually WRONG?
//
// That question went unasked while a hull sat in X1-KC84 with a row naming X1-GF41. The
// row was corrected several times over by routine syncs and nothing said a word, so the
// only evidence the fleet ever produced was the downstream symptom — a jump the API kept
// refusing as not-connected (4255) — hours later and pointing at the router rather than
// at the lost write. A correction is EVIDENCE, and evidence discarded is how a write-loss
// survives a hunt.
//
// SYSTEM-level divergence only, deliberately. A hull changing waypoint inside one system
// is ordinary traffic and every mover already records it; a hull changing SYSTEM behind
// our back is a lost cross-system write, and it is the divergence that actually breaks
// routing — the planner resolves gates and plots BFS hops per system, so a wrong system
// makes every downstream step confidently impossible. Narrow keeps the signal worth
// reading.

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
// the sync it is reporting on is worse than the silence it was added to break.
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
func divergedPosition(hadRow bool, believedSystem, believedWaypoint, actualSystem, actualWaypoint string) (PositionReanchor, bool) {
	if !hadRow || believedSystem == "" || actualSystem == "" {
		return PositionReanchor{}, false
	}
	if believedSystem == actualSystem {
		return PositionReanchor{}, false
	}
	return PositionReanchor{
		BelievedSystem:   believedSystem,
		BelievedWaypoint: believedWaypoint,
		ActualSystem:     actualSystem,
		ActualWaypoint:   actualWaypoint,
	}, true
}
