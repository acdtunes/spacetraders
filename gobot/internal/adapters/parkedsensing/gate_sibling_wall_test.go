package parkedsensing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gate_sibling_wall_test.go pins the routing half of the sibling-staleness rule: a stale
// row is excluded from the ANSWER, never allowed to erase the answer.
//
// Neighbours used to drop an edge set WHOLE the moment any row came back flagged stale,
// on the reasoning that one Replace stamps one timestamp so one stale row condemns all.
// That reasoning holds for a set that is genuinely OLD — and the store still condemns
// those before Neighbours ever sees them. It does NOT hold for an under-construction row
// chased on its own shorter clock, which is stale on a schedule while its built siblings
// are perfectly current. Every system with one building exit therefore became a WALL every
// 2h, and BFS refused routes that provably existed.

// A stale UNDER-CONSTRUCTION row is skipped like any impassable edge and its built siblings
// still answer. This is the shape that walled off 173 of 1,168 live systems.
func TestGateNeighbourPort_AStaleBuildingExitDoesNotWallOffItsBuiltSiblings(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{"X1-RJ93": true},
		edges: map[string][]domainSystem.GateEdge{"X1-RJ93": {
			// Past its 2h window: the store flags THIS row for re-probe and no other.
			{ConnectedSystem: "X1-XX80", UnderConstruction: true, Stale: true},
			{ConnectedSystem: "X1-AX76"},
			{ConnectedSystem: "X1-PA3"},
		}},
	})

	neighbours, err := port.Neighbours(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AX76", "X1-PA3"}, neighbours,
		"returned NOTHING — one stale building exit dropped the set whole, making this system a wall in "+
			"every BFS even though both built exits are current and passable")
}

// A stale row is never PASSABLE, whatever its build flag says. Skipping the set was at
// least conservative; replacing it with a per-row skip must stay conservative for that row,
// or the fix would trade a wall for a route across unverified topology.
func TestGateNeighbourPort_AStaleRowIsNeverOfferedAsATraversableNeighbour(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{"X1-RJ93": true},
		edges: map[string][]domainSystem.GateEdge{"X1-RJ93": {
			// Not flagged under construction, but its age is past verification: its build
			// state is UNKNOWN, so it must not be offered as an exit.
			{ConnectedSystem: "X1-UNVERIFIED", Stale: true},
			{ConnectedSystem: "X1-AX76"},
		}},
	})

	neighbours, err := port.Neighbours(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-AX76"}, neighbours,
		"a stale row's build state is unverified and must be excluded from the answer, exactly as an "+
			"under-construction row is — only its power to condemn SIBLINGS is removed")
}

// A system whose every exit is stale reports nothing, the same answer the whole-set drop
// used to give. Nothing is gained by pretending an unverified exit is passable.
func TestGateNeighbourPort_AnAllStaleSystemStillReportsNoNeighbours(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{"X1-DARK": true},
		edges: map[string][]domainSystem.GateEdge{"X1-DARK": {
			{ConnectedSystem: "X1-A", Stale: true},
			{ConnectedSystem: "X1-B", Stale: true},
		}},
	})

	neighbours, err := port.Neighbours(context.Background(), "X1-DARK")
	require.NoError(t, err)
	require.Empty(t, neighbours, "no verified exit means no traversable neighbour")
}
