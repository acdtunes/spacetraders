package capacity

// sp-3idiw — the production GateShortfallReader locates the home system's INCOMPLETE
// jump gate and reports its outstanding materials, mirroring the bootstrap gate-snapshot
// discovery. Every miss (no home system, no gate, a finished gate, an API error) fails
// closed to an empty shortfall so the reconciler never fabricates a depot.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeWaypointLister struct {
	waypoints []*shared.Waypoint
	err       error
}

func (f fakeWaypointLister) ListBySystem(context.Context, string) ([]*shared.Waypoint, error) {
	return f.waypoints, f.err
}

type fakeConstructionFinder struct {
	sites map[string]*manufacturing.ConstructionSite
	err   error
}

func (f fakeConstructionFinder) FindByWaypoint(_ context.Context, wp string, _ int) (*manufacturing.ConstructionSite, error) {
	return f.sites[wp], f.err
}

func homeFunc(system string) GateHomeSystemFunc {
	return func(context.Context, int) string { return system }
}

func jumpGate(symbol string) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, SystemSymbol: shared.ExtractSystemSymbol(symbol), Type: "JUMP_GATE"}
}

func siteWith(symbol string, complete bool, mats ...manufacturing.ConstructionMaterial) *manufacturing.ConstructionSite {
	return manufacturing.ReconstructConstructionSite(symbol, "JUMP_GATE", mats, complete)
}

// Behavior: an incomplete home-system jump gate yields its outstanding materials.
func TestGateShortfallAPIReader_ReportsIncompleteGateShortfall(t *testing.T) {
	gate := "X1-UM5-I56"
	reader := NewGateShortfallReader(
		homeFunc("X1-UM5"),
		fakeWaypointLister{waypoints: []*shared.Waypoint{
			{Symbol: "X1-UM5-A1", Type: "PLANET"},
			jumpGate(gate),
		}},
		fakeConstructionFinder{sites: map[string]*manufacturing.ConstructionSite{
			gate: siteWith(gate, false,
				manufacturing.NewConstructionMaterial("FAB_MATS", 1600, 200),
				manufacturing.NewConstructionMaterial("ADVANCED_CIRCUITRY", 400, 400), // fulfilled → dropped
			),
		}},
	)

	shortfall, err := reader.GateShortfall(context.Background(), 1)

	require.NoError(t, err)
	require.Equal(t, gate, shortfall.GateWaypoint)
	require.Len(t, shortfall.Materials, 1, "only the still-unfulfilled material is reported")
	require.Equal(t, "FAB_MATS", shortfall.Materials[0].TradeSymbol)
	require.Equal(t, 1400, shortfall.Materials[0].Remaining)
}

// Behavior: a finished gate is not the fill target — empty shortfall (the depot dissolves).
func TestGateShortfallAPIReader_CompleteGateYieldsEmpty(t *testing.T) {
	gate := "X1-UM5-I56"
	reader := NewGateShortfallReader(
		homeFunc("X1-UM5"),
		fakeWaypointLister{waypoints: []*shared.Waypoint{jumpGate(gate)}},
		fakeConstructionFinder{sites: map[string]*manufacturing.ConstructionSite{
			gate: siteWith(gate, true, manufacturing.NewConstructionMaterial("FAB_MATS", 1600, 1600)),
		}},
	)

	shortfall, err := reader.GateShortfall(context.Background(), 1)

	require.NoError(t, err)
	require.Empty(t, shortfall.GateWaypoint, "a finished gate is not the fill target")
}

// Behavior (fail-closed): no home system, no jump gate, and a waypoint-list error each
// yield an empty shortfall — never a fabricated depot.
func TestGateShortfallAPIReader_FailsClosedOnMisses(t *testing.T) {
	noHome := NewGateShortfallReader(homeFunc(""), fakeWaypointLister{}, fakeConstructionFinder{})
	got, err := noHome.GateShortfall(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, got.GateWaypoint, "no home system → no gate demand")

	noGate := NewGateShortfallReader(homeFunc("X1-UM5"),
		fakeWaypointLister{waypoints: []*shared.Waypoint{{Symbol: "X1-UM5-A1", Type: "PLANET"}}},
		fakeConstructionFinder{})
	got, err = noGate.GateShortfall(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, got.GateWaypoint, "no jump gate in the home system → no gate demand")

	listErr := NewGateShortfallReader(homeFunc("X1-UM5"),
		fakeWaypointLister{err: errors.New("db down")}, fakeConstructionFinder{})
	_, err = listErr.GateShortfall(context.Background(), 1)
	require.Error(t, err, "a waypoint-list error surfaces so the sensor logs + fails the gate family closed")
}
