package parkedsensing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gate_mapped_test.go pins the distinction the seed-priority ordering rests on: whether we hold ANY
// gate rows for a system, as opposed to whether any of them can be passed.
//
// Only a system we have NEVER read can name neighbours we do not already hold, and those names are
// what grow the ledger. A system whose every exit is under construction has been read — we know
// exactly where it connects and simply cannot go — so charting it reveals no adjacency at all.
//
// THE TWO ARE EASY TO CONFLATE because Neighbours FILTERS under-construction and stale edges out of
// its answer while the rows remain. "Neighbours came back empty" therefore covers both cases, and an
// implementation that derived Mapped from it would rank a known dead end as unexplored territory and
// send a seed nine hops to learn nothing. That is precisely what these tests forbid, and it is the
// mutation that survived until they existed.

// stubEdgeStore is the gate-edge store, with the found flag the real repository returns.
type stubEdgeStore struct {
	edges map[string][]domainSystem.GateEdge
	// known names the systems the store HOLDS ROWS FOR, independently of what those rows say — the
	// `ok` half of the real Edges signature, which is the whole subject here.
	known map[string]bool
	err   error
}

func (s stubEdgeStore) Edges(_ context.Context, system string) ([]domainSystem.GateEdge, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return s.edges[system], s.known[system], nil
}

// AllEdges mirrors Edges in bulk: a system appears iff the store HOLDS ROWS for it, which is the
// `ok` half of the Edges signature and the whole subject of this file. A system absent from
// `known` is unread territory and must stay absent here too, or a caller cannot tell "we looked
// and it connects nowhere" from "we have never looked".
func (s stubEdgeStore) AllEdges(_ context.Context) (map[string][]domainSystem.GateEdge, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := map[string][]domainSystem.GateEdge{}
	for system := range s.known {
		out[system] = append([]domainSystem.GateEdge(nil), s.edges[system]...)
	}
	return out, nil
}

// A system whose every edge is UNDER CONSTRUCTION is MAPPED, even though it reports no neighbours.
// This is the case that separates the two questions, and it is real: the fleet's own pocket was
// sealed by exactly this shape.
func TestGateNeighbourPort_AnAllUnderConstructionSystemIsMappedButHasNoNeighbours(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{"X1-SEALED": true},
		edges: map[string][]domainSystem.GateEdge{"X1-SEALED": {
			{ConnectedSystem: "X1-BEYOND", UnderConstruction: true},
			{ConnectedSystem: "X1-ELSEWHERE", UnderConstruction: true},
		}},
	})

	neighbours, err := port.Neighbours(context.Background(), "X1-SEALED")
	require.NoError(t, err)
	require.Empty(t, neighbours, "an all-under-construction system exposes no traversable neighbours")

	mapped, err := port.Mapped(context.Background(), "X1-SEALED")
	require.NoError(t, err)
	require.True(t, mapped,
		"reported UNMAPPED — we hold this system's gate rows and know exactly where it connects, so "+
			"charting it reveals no new adjacency; ranking it as unexplored territory sends a seed a "+
			"long way to learn nothing")
}

// A system we hold NO rows for is unmapped — genuinely unread, and the only kind whose charting can
// add systems to the ledger.
func TestGateNeighbourPort_ASystemWithNoStoredRowsIsUnmapped(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{},
		edges: map[string][]domainSystem.GateEdge{},
	})

	mapped, err := port.Mapped(context.Background(), "X1-KP42")
	require.NoError(t, err)
	require.False(t, mapped, "a system we hold no gate rows for must read as unmapped")
}

// An ordinary mapped system with traversable edges reads as mapped, so the rule does not promote
// everything at once.
func TestGateNeighbourPort_AnOrdinaryMappedSystemIsMapped(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{
		known: map[string]bool{"X1-HOME": true},
		edges: map[string][]domainSystem.GateEdge{"X1-HOME": {{ConnectedSystem: "X1-NEXT"}}},
	})

	mapped, err := port.Mapped(context.Background(), "X1-HOME")
	require.NoError(t, err)
	require.True(t, mapped)
}

// A store failure PROPAGATES rather than defaulting either way: read as mapped it would quietly
// demote genuine frontier territory, read as unmapped it would promote every ordinary target.
func TestGateNeighbourPort_AnUnreadableStoreFailsRatherThanGuessing(t *testing.T) {
	port := adapterSensing.NewGateNeighbourPort(stubEdgeStore{err: errors.New("gate store unhappy")})

	_, err := port.Mapped(context.Background(), "X1-ANY")
	require.Error(t, err, "an unreadable gate store must not present as a definite answer either way")
}
