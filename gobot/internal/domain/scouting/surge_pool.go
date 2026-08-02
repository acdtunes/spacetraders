package scouting

import "context"

// UnpricedSystem is one CHARTED system the fleet holds no price data for, with the
// two facts the ranking is built from.
type UnpricedSystem struct {
	System string
	// MarketWaypoints are the system's CHARTED waypoints carrying a marketplace,
	// in a deterministic order. A system with none is not in the pool at all: no
	// probe standing anywhere in it could ever produce a price, so ranking it would
	// only ever consume a dispatch slot a priceable system needed.
	MarketWaypoints []string
	// Waypoints is how many waypoint rows the system holds in the OPEN era — the
	// richness proxy, and the weakest of the three ranking terms.
	Waypoints int
}

// UnpricedSystemPool answers "which charted systems have no prices?" — the surge's
// whole work list, as a set difference over the local stores.
//
// ERA-SCOPED AND FAIL-CLOSED, and both halves are load-bearing. `waypoints` carries
// rows from every era the agent has ever played, and a dead-era system 404s the
// moment anything contacts it: an unscoped read of exactly this table cost ~290 API
// failures an hour for ten hours while utilisation sat at 88% against an 85%
// ceiling. An implementation therefore scopes to the open era through
// persistence.OpenEraScope — NOT openEraID + eraScopePredicate, whose nil path
// matches `era_id IS NULL` and so answers from pre-backfill rows instead of
// refusing — and returns an ERROR when the era cannot be resolved. The pass reads
// that error as "dispatch nothing this tick".
//
// A LOCAL READ, never the API. The pool is a difference between two tables the
// fleet already holds; paying the network to rediscover it would spend the budget
// this pass exists to protect.
//
// It lives in the domain rather than beside its consumer so the adapter that
// implements it does not have to import the application package that drives it.
type UnpricedSystemPool interface {
	UnpricedSystems(ctx context.Context, playerID int) ([]UnpricedSystem, error)
}
