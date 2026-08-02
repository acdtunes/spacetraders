package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// PruneContradictedEdges is the fail-loud half of the 4255 fix. A "not connected" refusal
// hands back data.connections — the authoritative gate set of the system the hull is really
// standing on — which was previously formatted into an error string and thrown away, so a
// phantom edge could be replanned into the identical impossible jump every tick.
//
// The contract is deliberately REMOVAL-ONLY, which is what makes it strictly non-loosening
// (RULINGS #4): a disproved edge is deleted, and nothing is ever added, marked built, or
// marked fresh. Deleting an edge can only ever shrink the routable graph, so this may make
// routing more accurate but can never authorise a jump that would otherwise be refused.

// seedKC84Adjacency writes the incident's real, CORRECT X1-KC84 edge set plus an unrelated
// system, so a test can prove the reconcile leaves healthy topology alone.
func seedKC84Adjacency(t *testing.T) (*persistence.GormGateEdgeRepository, context.Context) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, "X1-KC84", []system.GateEdge{
		{ConnectedSystem: "X1-GF41", GateWaypoint: "X1-GF41-I56"},
		{ConnectedSystem: "X1-RX9", GateWaypoint: "X1-RX9-XZ2B"},
		{ConnectedSystem: "X1-VV36", GateWaypoint: "X1-VV36-I60"},
	}))
	require.NoError(t, repo.Replace(ctx, "X1-GF41", []system.GateEdge{
		{ConnectedSystem: "X1-AJ10", GateWaypoint: "X1-AJ10-F24Z"},
		{ConnectedSystem: "X1-KC84", GateWaypoint: "X1-KC84-A13E"},
	}))
	return repo, ctx
}

// realKC84Connections is what the live 4255 refusal reported for the gate TORWIND-41 stood on.
func realKC84Connections() []string {
	return []string{"X1-VV36-I60", "X1-RX9-XZ2B", "X1-GF41-I56"}
}

// THE POINT: an edge the server's connection set does not contain is disproved, and is
// deleted so it can never be replanned into the same wrong jump again.
func TestPruneContradictedEdges_DeletesTheEdgeTheServerDisproved(t *testing.T) {
	repo, ctx := seedKC84Adjacency(t)
	// A phantom KC84->X1-AJ10 edge — the shape that would route a hull at KC84 into the
	// refused X1-AJ10-F24Z jump.
	require.NoError(t, repo.Replace(ctx, "X1-KC84", []system.GateEdge{
		{ConnectedSystem: "X1-GF41", GateWaypoint: "X1-GF41-I56"},
		{ConnectedSystem: "X1-RX9", GateWaypoint: "X1-RX9-XZ2B"},
		{ConnectedSystem: "X1-VV36", GateWaypoint: "X1-VV36-I60"},
		{ConnectedSystem: "X1-AJ10", GateWaypoint: "X1-AJ10-F24Z"},
	}))

	removed, err := repo.PruneContradictedEdges(ctx, "X1-KC84", realKC84Connections())
	require.NoError(t, err)
	require.Equal(t, 1, removed, "exactly the one disproved edge must be removed")

	edges, ok, err := repo.Edges(ctx, "X1-KC84")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"X1-GF41", "X1-RX9", "X1-VV36"}, connectedSystems(edges),
		"the disproved edge must be gone and the corroborated ones untouched")
}

// NON-DAMAGE: in the live incident KC84's stored set was already exactly right. A reconcile
// against a corroborating connection set must be a complete no-op — the fail-loud check may
// never destroy healthy topology just because a hull was mispositioned.
func TestPruneContradictedEdges_CorroboratedSetSurvivesIntact(t *testing.T) {
	repo, ctx := seedKC84Adjacency(t)

	removed, err := repo.PruneContradictedEdges(ctx, "X1-KC84", realKC84Connections())
	require.NoError(t, err)
	require.Zero(t, removed, "a corroborated edge set must lose nothing")

	edges, ok, err := repo.Edges(ctx, "X1-KC84")
	require.NoError(t, err)
	require.True(t, ok, "a corroborated set must stay a fresh hit, not be invalidated")
	require.Equal(t, []string{"X1-GF41", "X1-RX9", "X1-VV36"}, connectedSystems(edges))
}

// STRICTLY NON-LOOSENING (RULINGS #4): the payload names a gate we have no edge for. The
// reconcile must NOT create it. Inserting an edge from a refusal payload would hand the BFS
// a connection with no verified build state and could authorise a route we would otherwise
// refuse; the edge may only ever enter through the normal fetch path, which probes
// construction and fails closed.
func TestPruneContradictedEdges_NeverAddsAnEdgeFromThePayload(t *testing.T) {
	repo, ctx := seedKC84Adjacency(t)

	// The server reports a fourth connection we have never recorded.
	authoritative := append(realKC84Connections(), "X1-NEW9-Z99Z")
	removed, err := repo.PruneContradictedEdges(ctx, "X1-KC84", authoritative)
	require.NoError(t, err)
	require.Zero(t, removed)

	edges, _, err := repo.Edges(ctx, "X1-KC84")
	require.NoError(t, err)
	require.Equal(t, []string{"X1-GF41", "X1-RX9", "X1-VV36"}, connectedSystems(edges),
		"a connection the payload names must never be inserted as a routable edge")
}

// GUARD: an EMPTY authoritative set is refused OUTRIGHT, before any statement is issued. The
// jump-gate endpoint is known to return incomplete/empty reads (sp-hguq3/sp-dmxy5), and turning
// one into a WHERE ... NOT IN (<empty>) against a live DELETE is a whole system's topology
// riding on how one driver happens to render an empty set. No evidence means no statement.
//
// The closed connection is what makes that falsifiable: any statement this method issues now
// fails loudly, so the guard is the only thing that can produce a clean (0, nil). A test that
// merely re-read the rows afterwards would pass on the driver's incidental empty-IN behaviour
// and prove nothing about the guard.
func TestPruneContradictedEdges_EmptyAuthoritativeSetIssuesNoStatement(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, "X1-KC84", []system.GateEdge{
		{ConnectedSystem: "X1-GF41", GateWaypoint: "X1-GF41-I56"},
		{ConnectedSystem: "X1-RX9", GateWaypoint: "X1-RX9-XZ2B"},
	}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	removed, err := repo.PruneContradictedEdges(ctx, "X1-KC84", nil)
	require.NoError(t, err, "no evidence must short-circuit before any statement reaches the database")
	require.Zero(t, removed)
}

// The same refusal seen from the data side: an empty set leaves a live system's edges whole.
func TestPruneContradictedEdges_EmptyAuthoritativeSetDeletesNothing(t *testing.T) {
	repo, ctx := seedKC84Adjacency(t)

	removed, err := repo.PruneContradictedEdges(ctx, "X1-KC84", nil)
	require.NoError(t, err)
	require.Zero(t, removed)

	edges, ok, err := repo.Edges(ctx, "X1-KC84")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"X1-GF41", "X1-RX9", "X1-VV36"}, connectedSystems(edges),
		"an empty connection set is no evidence and must delete nothing")
}

// SCOPING: the reconcile touches ONE system. The refusal describes the gate the hull is
// standing on and says nothing about any other system's edges — in the live incident X1-GF41's
// set was correct, and widening the delete would have corrupted it.
func TestPruneContradictedEdges_LeavesOtherSystemsAlone(t *testing.T) {
	repo, ctx := seedKC84Adjacency(t)

	_, err := repo.PruneContradictedEdges(ctx, "X1-KC84", realKC84Connections())
	require.NoError(t, err)

	edges, ok, err := repo.Edges(ctx, "X1-GF41")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"X1-AJ10", "X1-KC84"}, connectedSystems(edges),
		"a neighbouring system's edges are not evidence in this refusal and must be untouched")
}

// GUARD: the negative-result BACKOFF MARKER (connected_system = "") is not an edge — it holds
// the unreadable ORIGIN gate, which no connection set will ever contain. Sweeping it up as
// "contradicted" would silently clear the backoff and re-open the 400-storm.
func TestPruneContradictedEdges_NeverDeletesTheBackoffMarker(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KC84", ConnectedSystem: "X1-GF41", GateWaypoint: "X1-GF41-I56",
		EraID: intPtr(1), SyncedAt: freshTS(),
	}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KC84", ConnectedSystem: "", GateWaypoint: "X1-KC84-A13E",
		EraID: intPtr(1), SyncedAt: "", UnreadableSince: freshTS(), AttemptCount: 3,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	_, err = repo.PruneContradictedEdges(ctx, "X1-KC84", realKC84Connections())
	require.NoError(t, err)

	attempts, _, ok, err := repo.UnreadableState(ctx, "X1-KC84")
	require.NoError(t, err)
	require.True(t, ok, "the backoff marker must survive a reconcile")
	require.Equal(t, 3, attempts, "the backoff clock must not be reset by a reconcile")
}
