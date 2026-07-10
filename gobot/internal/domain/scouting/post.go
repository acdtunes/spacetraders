// Package scouting holds the domain types for the standing scout-post system
// (sp-cxpq): a desired-state table of per-system market-freshness assignments
// that the scout_post_coordinator reconciles every tick, the way the contract
// fleet coordinator reconciles its dedicated fleet.
//
// A ScoutPost is operational state, not a rich aggregate — it mirrors the
// gate-edge / tour-telemetry idiom (a persisted row read directly), so the type
// here is a plain value plus the repository port. The coordinator owns all
// mutation (RULINGS #3, single-writer); the captain edits posts only through the
// daemon RPC that writes this table, never a file.
package scouting

import "time"

// PostKind distinguishes a permanent freshness post from a one-shot frontier
// sweep. Both are manned identically by the reconciler; they differ only in what
// happens when their tour finishes:
//   - standing posts run an infinite tour and keep a system fresh forever;
//   - sweep-once posts run a single finite tour and auto-remove once it
//     completes, freeing their hull for the next unmanned post.
//
// The "scan queue" in the acceptance criteria is exactly the set of unmanned
// sweep-once posts — the captain seeds them from the frontier census (sp-7gr2),
// so no separate queue subsystem is needed.
type PostKind string

const (
	PostKindStanding  PostKind = "standing"
	PostKindSweepOnce PostKind = "sweep_once"
)

// Valid reports whether k is a recognized post kind.
func (k PostKind) Valid() bool {
	return k == PostKindStanding || k == PostKindSweepOnce
}

// ScoutPost is one desired-state assignment: "keep system S scanned to within
// FreshnessTarget, using hull AssignedHull, via tour container TourContainerID".
// AssignedHull == "" means the post is unmanned and the reconciler will claim an
// idle satellite for it on its next tick. Both the assignment and the tour
// container id are persisted so a daemon restart re-adopts the same hull onto
// the same post (RULINGS #2).
//
// RepositionContainerID (sp-s232) tracks an in-flight cross-gate RELAY that is
// jump-routing an idle satellite from another system TO this post — the fleet-wide
// half of manning that sp-qxa4 deliberately left out. It is mutually exclusive with
// AssignedHull: a repositioning post is NOT manned (the hull is still in transit, so
// IsManned stays false and the in-system repair pass never touches it), and manning
// clears any relay first. Persisted so a daemon restart re-adopts the same relay
// (RULINGS #2) — recovery marks the worker interrupted (claim preserved) and the
// coordinator re-dispatches from the hull's current position.
type ScoutPost struct {
	ID                    int
	PlayerID              int
	SystemSymbol          string
	FreshnessTarget       time.Duration
	Kind                  PostKind
	AssignedHull          string
	TourContainerID       string
	RepositionContainerID string
	CreatedAt             time.Time
}

// IsManned reports whether a hull is currently assigned to this post. A post with a
// reposition relay in flight is NOT manned — its satellite is still crossing gates
// (AssignedHull is empty until the relay lands and the next in-system tick mans it).
func (p *ScoutPost) IsManned() bool {
	return p.AssignedHull != ""
}

// IsRepositioning reports whether a cross-gate relay is currently jump-routing a
// satellite toward this post (sp-s232). While true the coordinator neither re-mans
// nor re-dispatches the post — the relay owns it until it lands or dies.
func (p *ScoutPost) IsRepositioning() bool {
	return p.RepositionContainerID != ""
}
