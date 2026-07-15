package expansion

import (
	"context"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// shipyardWaypointLister is the narrow waypoint-repo slice the backfill enumerator reads:
// every cached waypoint bearing a trait, era-AGNOSTIC (a SHIPYARD trait is an immutable
// physical fact — a prior-era row is still proof the system holds a yard, the sp-42ow
// lesson). Satisfied by *persistence.GormWaypointRepository.ListWithTrait.
type shipyardWaypointLister interface {
	ListWithTrait(ctx context.Context, trait string) ([]*shared.Waypoint, error)
}

// ChartedShipyardEnumerator backs the sp-rhju backfill sweep's charted-shipyard port
// (commands.ChartedShipyardEnumerator). It answers "which known-shipyard systems could a
// probe be relayed to, and how deep are they" by INTERSECTING two facts:
//
//   - the era-AGNOSTIC set of systems whose swept waypoints reveal a SHIPYARD trait (the
//     complete charted-shipyard set — reading it era-scoped would drop ~90% of real yards,
//     the exact sp-42ow blindness), and
//   - the CURRENT gate-reachable frontier (the ExpansionScanner's candidates, each with its
//     hop depth), which supplies the deeper-first ordering key AND filters any dead-universe
//     or presently-unreachable symbol a relay could never actually reach.
//
// A charted shipyard that is not currently gate-reachable within the hop bound is omitted:
// the backfill can only dispatch a sweep-once post the reconciler can man, so an unreachable
// yard is not a target this cycle. maxHops bounds the reach to the relay horizon.
type ChartedShipyardEnumerator struct {
	scanner   candidateLister
	waypoints shipyardWaypointLister
	maxHops   int
}

// NewChartedShipyardEnumerator wires the enumerator over the frontier scanner (hops +
// reachability) and the waypoint trait lister (the charted-shipyard set). maxHops bounds the
// reach — set to the relay horizon so no unreachable yard is enumerated.
func NewChartedShipyardEnumerator(scanner candidateLister, waypoints shipyardWaypointLister, maxHops int) *ChartedShipyardEnumerator {
	return &ChartedShipyardEnumerator{scanner: scanner, waypoints: waypoints, maxHops: maxHops}
}

// ChartedShipyardSystems returns every currently-reachable known-shipyard system annotated
// with a representative shipyard waypoint and its gate-hop depth, for the backfill to order
// deeper-first and dispatch. A read failure on either source surfaces as an error (the caller
// fails the pass rather than declaring against a partial/empty view).
func (e *ChartedShipyardEnumerator) ChartedShipyardSystems(ctx context.Context, playerID int) ([]scoutingCmd.ChartedShipyardSystem, error) {
	waypoints, err := e.waypoints.ListWithTrait(ctx, "SHIPYARD")
	if err != nil {
		return nil, err
	}
	yardBySystem := representativeYardBySystem(waypoints)

	candidates, err := e.scanner.ExpansionCandidates(ctx, playerID, e.maxHops)
	if err != nil {
		return nil, err
	}

	out := make([]scoutingCmd.ChartedShipyardSystem, 0, len(yardBySystem))
	seen := make(map[string]bool, len(yardBySystem))
	for _, candidate := range candidates {
		yard, hasYard := yardBySystem[candidate.SystemSymbol]
		if !hasYard || seen[candidate.SystemSymbol] {
			continue
		}
		seen[candidate.SystemSymbol] = true
		out = append(out, scoutingCmd.ChartedShipyardSystem{
			SystemSymbol:     candidate.SystemSymbol,
			ShipyardWaypoint: yard,
			Hops:             candidate.Hops,
		})
	}
	return out, nil
}

// idleProbeFleetReader is the narrow ship-repo slice the idle-probe counter needs.
// Satisfied by navigation.ShipRepository.
type idleProbeFleetReader interface {
	FindIdleByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// IdleProbeCounter backs the backfill sweep's supply bound (commands.IdleProbeCounter): the
// count of idle, relay-able scout hulls the reconciler could man a declared backfill post
// with. It counts idle scout-type hulls — the coordinator only COUNTS them (the reconciler
// does the actual poach-guarded claim), so this is a soft upper bound that keeps the sweep
// from declaring more posts than there are hulls to serve, never a claim.
type IdleProbeCounter struct {
	shipRepo idleProbeFleetReader
}

// NewIdleProbeCounter wires the idle-probe supply reader over the ship repository.
func NewIdleProbeCounter(shipRepo idleProbeFleetReader) *IdleProbeCounter {
	return &IdleProbeCounter{shipRepo: shipRepo}
}

// IdleProbeCount returns how many idle scout-type hulls the player has — the backfill's
// per-cycle supply bound.
func (c *IdleProbeCounter) IdleProbeCount(ctx context.Context, playerID shared.PlayerID) (int, error) {
	ships, err := c.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, ship := range ships {
		if ship.IsScoutType() {
			count++
		}
	}
	return count, nil
}

// representativeYardBySystem folds the SHIPYARD-trait waypoints into one deterministic
// representative waypoint per system (the lexicographically smallest symbol), so a system
// with several shipyards always maps to the same target and the enumeration is stable.
func representativeYardBySystem(waypoints []*shared.Waypoint) map[string]string {
	bySystem := make(map[string]string, len(waypoints))
	for _, waypoint := range waypoints {
		system := waypoint.SystemSymbol
		if system == "" {
			system = shared.ExtractSystemSymbol(waypoint.Symbol)
		}
		if existing, ok := bySystem[system]; !ok || waypoint.Symbol < existing {
			bySystem[system] = waypoint.Symbol
		}
	}
	return bySystem
}
