package capacity_test

// Unit tests for the capacity CONVERGE actuator (bead st-5ig, epic st-7zk).
//
// The Actuator is the thin wrapper that translates one governed Action into a
// call to the EXISTING primitive that already performs that work (fleet-assign,
// reposition/navigate, the worker-rebalancer, the depot buffer config). It
// decides NOTHING — the loop only ever hands it actions DIFF+GOVERN already
// approved — so every test drives the Actuator through its domain port
// (ReuseIdleHull/Rebalance/AdjustBuffer/ExecuteCapital) and asserts at the
// primitive-port boundary that the right existing primitive was driven with the
// action's routing fields. Doubles live ONLY at those primitive ports (the seam
// the production adapters wrap the real fleet-assign / navigate / rebalancer /
// buffer-config primitives at); the Actuator itself is real.
//
// Test budget: 6 distinct behaviors × 2 = 12 max. 6 written (some parametrized):
//  1. tier-1 reassign  -> drives the hull-reassign primitive with hull + role fleet
//  2. tier-2 reposition -> drives the reposition primitive with hull + anchor
//  3. tier-2 rebalance  -> drives the worker-rebalancer toward the shortfall hub
//  4. tier-3 buffer     -> drives the buffer-config primitive with good + cap
//  5. tier-4 capital    -> fails CLOSED, drives NO primitive (st-0h8 owns capital)
//  6. primitive failure -> surfaces as the action's converge failure

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- primitive-port doubles (spies at the existing-primitive boundary) -------

type fakeReassigner struct {
	calls    int
	playerID shared.PlayerID
	ship     string
	fleet    string
	err      error
}

func (f *fakeReassigner) ReassignHull(_ context.Context, playerID shared.PlayerID, shipSymbol, fleet string) error {
	f.calls++
	f.playerID, f.ship, f.fleet = playerID, shipSymbol, fleet
	return f.err
}

type fakeRepositioner struct {
	calls    int
	playerID shared.PlayerID
	ship     string
	dest     string
	err      error
}

func (f *fakeRepositioner) RepositionHull(_ context.Context, playerID shared.PlayerID, shipSymbol, destinationWaypoint string) error {
	f.calls++
	f.playerID, f.ship, f.dest = playerID, shipSymbol, destinationWaypoint
	return f.err
}

type fakeWorkers struct {
	calls    int
	playerID shared.PlayerID
	hub      string
	waypoint string
	count    int
	err      error
}

func (f *fakeWorkers) RebalanceWorkers(_ context.Context, playerID shared.PlayerID, hubSymbol, workerWaypoint string, count int) error {
	f.calls++
	f.playerID, f.hub, f.waypoint, f.count = playerID, hubSymbol, workerWaypoint, count
	return f.err
}

type fakeBuffers struct {
	calls    int
	playerID shared.PlayerID
	hub      string
	good     string
	cap      int
	err      error
}

func (f *fakeBuffers) AdjustBufferGood(_ context.Context, playerID shared.PlayerID, hubSymbol, good string, unitsCap int) error {
	f.calls++
	f.playerID, f.hub, f.good, f.cap = playerID, hubSymbol, good, unitsCap
	return f.err
}

type fakePlayers struct {
	pid shared.PlayerID
	err error
}

func (f *fakePlayers) ResolvePlayer(_ context.Context) (shared.PlayerID, error) {
	return f.pid, f.err
}

// spies bundles the four primitive doubles + the player resolver so a test can
// arm one and assert the others stayed untouched (the escalation ladder never
// double-actuates a single action).
type spies struct {
	reassigner   *fakeReassigner
	repositioner *fakeRepositioner
	workers      *fakeWorkers
	buffers      *fakeBuffers
	players      *fakePlayers
}

func newActuatorWithSpies(playerID shared.PlayerID) (*capacityAdapters.Actuator, *spies) {
	s := &spies{
		reassigner:   &fakeReassigner{},
		repositioner: &fakeRepositioner{},
		workers:      &fakeWorkers{},
		buffers:      &fakeBuffers{},
		players:      &fakePlayers{pid: playerID},
	}
	a := capacityAdapters.NewActuator(s.reassigner, s.repositioner, s.workers, s.buffers, s.players)
	return a, s
}

// ---- 1. tier-1 reassign -----------------------------------------------------

// A tier-1 reassign dedicates the idle hull the differ picked to the fleet its
// ROLE coordinator claims under — warehouse/stocker/worker map to the
// "warehouse"/"stocker"/depot-delivery fleets — reusing the single fleet-assign
// write path. The role is read machine-readably from GapKind, never Reason.
func TestReuseIdleHull_ReassignsIdleHullToItsRoleFleet(t *testing.T) {
	cases := []struct {
		name      string
		gap       capacity.GapKind
		wantFleet string
	}{
		{"warehouse role", capacity.GapWarehouseShort, "warehouse"},
		{"stocker role", capacity.GapStockerShort, "stocker"},
		{"worker role", capacity.GapWorkerShort, depot.DeliveryHullFleet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid := shared.MustNewPlayerID(42)
			a, s := newActuatorWithSpies(pid)
			action := capacity.Action{
				Tier:           capacity.TierReuseIdle,
				Verb:           capacity.VerbReassignHull,
				GapKind:        tc.gap,
				HubSymbol:      "X1-HUB",
				ShipSymbol:     "SHIP-9",
				TargetWaypoint: "X1-HUB-A",
			}

			err := a.ReuseIdleHull(context.Background(), action)

			require.NoError(t, err)
			require.Equal(t, 1, s.reassigner.calls, "the reassign primitive is driven exactly once")
			require.Equal(t, pid, s.reassigner.playerID)
			require.Equal(t, "SHIP-9", s.reassigner.ship)
			require.Equal(t, tc.wantFleet, s.reassigner.fleet,
				"the idle hull joins the fleet its role's coordinator claims under")
			require.Zero(t, s.repositioner.calls)
			require.Zero(t, s.workers.calls)
			require.Zero(t, s.buffers.calls)
		})
	}
}

// An unclassified reassign (no role GapKind) must FAIL CLOSED — never guess a
// fleet — so a malformed action surfaces instead of silently mis-pinning a hull.
func TestReuseIdleHull_FailsClosed_OnUnknownRole(t *testing.T) {
	a, s := newActuatorWithSpies(shared.MustNewPlayerID(1))
	action := capacity.Action{
		Tier:       capacity.TierReuseIdle,
		Verb:       capacity.VerbReassignHull,
		GapKind:    capacity.GapHubUncovered, // not a role-bearing gap
		ShipSymbol: "SHIP-9",
	}

	err := a.ReuseIdleHull(context.Background(), action)

	require.Error(t, err)
	require.Zero(t, s.reassigner.calls, "no hull is reassigned when its role is unknown")
}

// ---- 2. tier-2 reposition + worker rebalance --------------------------------

// A tier-2 reposition drives the reposition/navigate primitive to move the
// named hull onto the hub's desired anchor waypoint.
func TestRebalance_RepositionsMisplacedHullToAnchor(t *testing.T) {
	pid := shared.MustNewPlayerID(7)
	a, s := newActuatorWithSpies(pid)
	action := capacity.Action{
		Tier:           capacity.TierRebalance,
		Verb:           capacity.VerbRepositionHull,
		GapKind:        capacity.GapHullMisplaced,
		HubSymbol:      "X1-HUB",
		ShipSymbol:     "WH-1",
		TargetWaypoint: "X1-HUB-ANCHOR",
	}

	err := a.Rebalance(context.Background(), action)

	require.NoError(t, err)
	require.Equal(t, 1, s.repositioner.calls)
	require.Equal(t, pid, s.repositioner.playerID)
	require.Equal(t, "WH-1", s.repositioner.ship)
	require.Equal(t, "X1-HUB-ANCHOR", s.repositioner.dest)
	require.Zero(t, s.workers.calls)
	require.Zero(t, s.reassigner.calls)
}

// A tier-2 rebalance_workers drives the worker-rebalancer toward the shortfall
// hub with the moved-worker quantity (Count) read machine-readably — the
// rebalancer primitive owns the actual per-worker moves.
func TestRebalance_DrivesWorkerRebalancerTowardShortfallHub(t *testing.T) {
	pid := shared.MustNewPlayerID(7)
	a, s := newActuatorWithSpies(pid)
	action := capacity.Action{
		Tier:           capacity.TierRebalance,
		Verb:           capacity.VerbRebalanceWorkers,
		GapKind:        capacity.GapWorkerShort,
		HubSymbol:      "X1-HUB",
		TargetWaypoint: "X1-HUB-W",
		Count:          3,
	}

	err := a.Rebalance(context.Background(), action)

	require.NoError(t, err)
	require.Equal(t, 1, s.workers.calls)
	require.Equal(t, pid, s.workers.playerID)
	require.Equal(t, "X1-HUB", s.workers.hub)
	require.Equal(t, "X1-HUB-W", s.workers.waypoint)
	require.Equal(t, 3, s.workers.count)
	require.Zero(t, s.repositioner.calls)
}

// ---- 3. tier-3 buffer adjust ------------------------------------------------

// A tier-3 buffer adjust drives the buffer-config primitive with the good and
// the differ's desired cap — for BOTH verbs. A de-whitelist is encoded by the
// differ as UnitsCap 0, so the same primitive call carries add / cap-change /
// remove uniformly (input variations of one behavior, parametrized).
func TestAdjustBuffer_DrivesBufferConfiguratorWithDesiredCap(t *testing.T) {
	cases := []struct {
		name string
		verb capacity.ActionVerb
		good string
		cap  int
	}{
		{"whitelist a good at its cap", capacity.VerbAdjustBufferWhitelist, "DRUGS", 24},
		{"de-whitelist a good (cap 0)", capacity.VerbAdjustBufferWhitelist, "SHIP_PARTS", 0},
		{"correct a whitelisted good's cap", capacity.VerbAdjustBufferCap, "MEDICINE", 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid := shared.MustNewPlayerID(5)
			a, s := newActuatorWithSpies(pid)
			action := capacity.Action{
				Tier:      capacity.TierBufferAdjust,
				Verb:      tc.verb,
				HubSymbol: "X1-HUB",
				Good:      tc.good,
				UnitsCap:  tc.cap,
			}

			err := a.AdjustBuffer(context.Background(), action)

			require.NoError(t, err)
			require.Equal(t, 1, s.buffers.calls)
			require.Equal(t, pid, s.buffers.playerID)
			require.Equal(t, "X1-HUB", s.buffers.hub)
			require.Equal(t, tc.good, s.buffers.good)
			require.Equal(t, tc.cap, s.buffers.cap)
			require.Zero(t, s.reassigner.calls)
		})
	}
}

// ---- 4. tier-4 capital: fail closed, no buy ---------------------------------

// ExecuteCapital is post-approval ONLY (st-0h8 owns the capital path). In the
// cheap-tier actuator it fails CLOSED with an explicit not-wired error and
// drives NO primitive — no buy, no move, nothing — so an approved tier-4 action
// that somehow reached it can never auto-execute a purchase from here.
func TestExecuteCapital_FailsClosed_DrivesNoPrimitive(t *testing.T) {
	a, s := newActuatorWithSpies(shared.MustNewPlayerID(1))
	action := capacity.Action{
		Tier:                 capacity.TierCapital,
		Verb:                 capacity.VerbBuyHull,
		GapKind:              capacity.GapWorkerShort,
		HubSymbol:            "X1-HUB",
		HullDelta:            1,
		EstimatedCostCredits: 400_000,
	}

	err := a.ExecuteCapital(context.Background(), action)

	require.Error(t, err, "capital execution is fail-closed in the cheap-tier actuator")
	require.Contains(t, err.Error(), "not wired")
	require.Zero(t, s.reassigner.calls, "no buy: no primitive of any kind is driven")
	require.Zero(t, s.repositioner.calls)
	require.Zero(t, s.workers.calls)
	require.Zero(t, s.buffers.calls)
}

// ---- 5. primitive failure surfaces ------------------------------------------

// When the underlying primitive fails, the Actuator surfaces the error (the loop
// records it as the action's CONVERGE failure, logs, and continues — the failed
// item reappears as gap next tick). Also covers an unresolvable player: the
// action fails rather than acting on behalf of the wrong (zero) player. Input
// variations of one behavior, parametrized.
func TestActuator_SurfacesPrimitiveFailure(t *testing.T) {
	boom := errors.New("primitive boom")
	reassign := capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, GapKind: capacity.GapWarehouseShort, ShipSymbol: "S1"}
	reposition := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, ShipSymbol: "S1", TargetWaypoint: "WP"}
	rebalance := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbRebalanceWorkers, HubSymbol: "H", TargetWaypoint: "WP", Count: 1}
	buffer := capacity.Action{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferCap, HubSymbol: "H", Good: "DRUGS", UnitsCap: 10}

	cases := []struct {
		name string
		arm  func(s *spies)
		act  func(a *capacityAdapters.Actuator) error
	}{
		{"tier-1 reassign primitive fails", func(s *spies) { s.reassigner.err = boom }, func(a *capacityAdapters.Actuator) error { return a.ReuseIdleHull(context.Background(), reassign) }},
		{"tier-2 reposition primitive fails", func(s *spies) { s.repositioner.err = boom }, func(a *capacityAdapters.Actuator) error { return a.Rebalance(context.Background(), reposition) }},
		{"tier-2 rebalancer primitive fails", func(s *spies) { s.workers.err = boom }, func(a *capacityAdapters.Actuator) error { return a.Rebalance(context.Background(), rebalance) }},
		{"tier-3 buffer primitive fails", func(s *spies) { s.buffers.err = boom }, func(a *capacityAdapters.Actuator) error { return a.AdjustBuffer(context.Background(), buffer) }},
		{"player unresolvable", func(s *spies) { s.players.err = boom; s.players.pid = shared.PlayerID{} }, func(a *capacityAdapters.Actuator) error { return a.ReuseIdleHull(context.Background(), reassign) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, s := newActuatorWithSpies(shared.MustNewPlayerID(1))
			tc.arm(s)

			err := tc.act(a)

			require.ErrorIs(t, err, boom, "the primitive failure surfaces to the loop verbatim")
		})
	}
}
