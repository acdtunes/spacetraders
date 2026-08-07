package queries

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeLaneCounter struct {
	census     LaneCensus
	readable   bool
	err        error
	lastSystem []string
}

// countingLanes is the fake census answering with a count and no terms — what a test whose subject
// is the SUBTRACTION needs.
func countingLanes(profitable int) *fakeLaneCounter {
	return &fakeLaneCounter{census: LaneCensus{Profitable: profitable}, readable: true}
}

func (f *fakeLaneCounter) CountProfitableLanes(ctx context.Context, playerID int, systems []string) (LaneCensus, bool, error) {
	f.lastSystem = systems
	return f.census, f.readable, f.err
}

// --- fake ship repository (narrow: the one fleet read the unserved count consumes) -------------

// hullSpec describes one hull the fake fleet serves: the fleet it is dedicated to (empty = the
// general pool) and the system it stands in. The reader reads exactly those two facts.
type hullSpec struct {
	fleet  string
	system string
}

func tradeHulls(n int) []hullSpec { return hullsTagged(n, tradeFleetTag) }
func otherHulls(n int) []hullSpec { return hullsTagged(n, "") }

func hullsTagged(n int, fleet string) []hullSpec {
	out := make([]hullSpec, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hullSpec{fleet: fleet, system: "X1-AA"})
	}
	return out
}

// hullsInSystems places one trade hull in each named system, repeats included — the fixture for
// the system-discovery scan.
func hullsInSystems(systems ...string) []hullSpec {
	out := make([]hullSpec, 0, len(systems))
	for _, s := range systems {
		out = append(out, hullSpec{fleet: tradeFleetTag, system: s})
	}
	return out
}

type fakeShipRepo struct {
	navigation.ShipRepository
	all []*navigation.Ship
	err error
}

func (r *fakeShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.all, r.err
}

func fakeShipRepoWith(t *testing.T, groups ...[]hullSpec) *fakeShipRepo {
	t.Helper()
	repo := &fakeShipRepo{}
	n := 0
	for _, g := range groups {
		for _, spec := range g {
			n++
			repo.all = append(repo.all, hullAt(t, fmt.Sprintf("SHIP-%d", n), spec))
		}
	}
	return repo
}

func erroringShipRepo(err error) *fakeShipRepo {
	return &fakeShipRepo{err: err}
}

// hullAt builds one hull parked at a waypoint inside spec.system — its location is the system
// discovery signal, its dedication tag is the trade-pool signal.
func hullAt(t *testing.T, symbol string, spec hullSpec) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(spec.system+"-1", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 40, cargo, 30,
		"FRAME_HEAVY_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	ship.SetDedicatedFleet(spec.fleet)
	return ship
}

// --- UnservedLaneCount -------------------------------------------------------------------------

func TestUnservedLaneCount_SubtractsTheTradePool(t *testing.T) {
	// 7 profitable lanes, 2 trade-dedicated hulls => 5 unserved.
	repo := fakeShipRepoWith(t, tradeHulls(2), otherHulls(3))
	r := NewUnservedLaneReader(repo, countingLanes(7))

	got, readable, err := r.UnservedLaneCount(context.Background(), 1)
	if err != nil || !readable {
		t.Fatalf("expected a readable count, got readable=%v err=%v", readable, err)
	}
	if got != 5 {
		t.Fatalf("expected 7 profitable − 2 trade hulls = 5 unserved, got %d", got)
	}
}

// The tag's VALUE is the contract, not just its name. The dedication is written elsewhere as a
// bare literal, so a fixture tagged from the same constant the reader matches on would agree with
// itself through any rename and never see the two sides drift apart.
func TestUnservedLaneCount_SubtractsHullsDedicatedToTheLiteralTradeFleet(t *testing.T) {
	repo := fakeShipRepoWith(t, hullsTagged(2, "trade"), hullsTagged(3, "contract"))
	r := NewUnservedLaneReader(repo, countingLanes(7))

	got, readable, err := r.UnservedLaneCount(context.Background(), 1)
	if err != nil || !readable {
		t.Fatalf("expected a readable count, got readable=%v err=%v", readable, err)
	}
	if got != 5 {
		t.Fatalf(`expected 7 profitable − 2 hulls dedicated "trade" = 5 unserved, got %d`, got)
	}
}

// The pool already covering every lane is never a NEGATIVE demand.
func TestUnservedLaneCount_PoolExceedsLanes_ClampsToZero(t *testing.T) {
	repo := fakeShipRepoWith(t, tradeHulls(9))
	r := NewUnservedLaneReader(repo, countingLanes(2))
	got, readable, _ := r.UnservedLaneCount(context.Background(), 1)
	if !readable || got != 0 {
		t.Fatalf("expected a readable 0, got %d readable=%v", got, readable)
	}
}

// FAIL CLOSED on a genuine read failure — both spenders that consume this must refuse to act
// on a signal they could not see.
func TestUnservedLaneCount_FailsClosedOnReadFailure(t *testing.T) {
	t.Run("ship read", func(t *testing.T) {
		r := NewUnservedLaneReader(erroringShipRepo(errors.New("db down")), countingLanes(7))
		_, readable, err := r.UnservedLaneCount(context.Background(), 1)
		if readable || err == nil {
			t.Fatalf("a ship read failure must be unreadable with an error, got readable=%v err=%v", readable, err)
		}
	})
	t.Run("lane surface", func(t *testing.T) {
		r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(1)), &fakeLaneCounter{readable: false})
		_, readable, _ := r.UnservedLaneCount(context.Background(), 1)
		if readable {
			t.Fatalf("an unreadable lane surface must be unreadable here too")
		}
	})
}

// A READABLE zero is a genuine zero (empty cache, no floor-clearing lane) — no demand, no buy
// — and must NOT read as fail-closed, which would be indistinguishable from an outage.
func TestUnservedLaneCount_ReadableZeroIsNotFailClosed(t *testing.T) {
	r := NewUnservedLaneReader(fakeShipRepoWith(t, tradeHulls(0)), countingLanes(0))
	got, readable, err := r.UnservedLaneCount(context.Background(), 1)
	if !readable || err != nil || got != 0 {
		t.Fatalf("expected (0,true,nil), got (%d,%v,%v)", got, readable, err)
	}
}

// The lane surface is scanned over the systems the fleet actually holds hulls in.
func TestUnservedLaneCount_ScansTheSystemsTheFleetOccupies(t *testing.T) {
	lanes := countingLanes(1)
	r := NewUnservedLaneReader(fakeShipRepoWith(t, hullsInSystems("X1-AA", "X1-AA", "X1-BB")), lanes)
	if _, _, err := r.UnservedLaneCount(context.Background(), 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lanes.lastSystem) != 2 {
		t.Fatalf("expected the two DISTINCT systems, got %v", lanes.lastSystem)
	}
}
