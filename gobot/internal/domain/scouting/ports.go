package scouting

import "context"

// ScoutPostRepository is the persistence port for the desired-state posts table
// (sp-cxpq). All reads are scoped to the open era so a universe reset never
// leaves the coordinator manning dead-era posts (mirrors the era-scoping of
// waypoints/gate-edges, and the sp-njpu recovery guard). The daemon is the only
// writer (RULINGS #3): the reconciler persists assignment changes and the
// captain's CLI edits both funnel through this port, never a config file.
type ScoutPostRepository interface {
	// ListActive returns every post owned by playerID in the open era. Returns
	// an empty slice (not an error) when there is no open era or no posts.
	ListActive(ctx context.Context, playerID int) ([]*ScoutPost, error)

	// Upsert writes the full desired state of post, keyed by (PlayerID,
	// SystemSymbol), stamping it with the open era. On insert it sets post.ID.
	// The caller owns every field — Upsert never merges — so an assignment the
	// caller did not intend to change must already be populated on post.
	Upsert(ctx context.Context, post *ScoutPost) error

	// Remove deletes the post for (playerID, systemSymbol) in the open era. It is
	// not an error to remove a post that does not exist.
	Remove(ctx context.Context, playerID int, systemSymbol string) error
}
