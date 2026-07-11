package persistence_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func freshTS() string              { return time.Now().Format(time.RFC3339) }
func staleTS() string              { return time.Now().Add(-48 * time.Hour).Format(time.RFC3339) }
func agoTS(d time.Duration) string { return time.Now().Add(-d).Format(time.RFC3339) }
func intPtr(i int) *int            { return &i }

// connectedSystems extracts the neighbor symbols from a GateEdge slice, sorted,
// for order-insensitive assertions.
func connectedSystems(edges []system.GateEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.ConnectedSystem)
	}
	sort.Strings(out)
	return out
}

// Replace round-trips through the store: the written edge set reads back
// (era-scoped, fresh), and each neighbor's own gate waypoint is preserved for the
// reverse lookup that lets an uncharted system be fetched later.
func TestGateEdgeRepository_ReplaceThenEdges_RoundTrip(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
		{ConnectedSystem: "X1-GQ92", GateWaypoint: "X1-GQ92-I77"},
	}))

	edges, ok, err := repo.Edges(ctx, "X1-KA42")
	require.NoError(t, err)
	require.True(t, ok, "freshly written edges must be a hit")
	require.Equal(t, []string{"X1-GQ92", "X1-PA3"}, connectedSystems(edges))

	// The reverse lookup returns a neighbor's OWN gate waypoint (recorded as the
	// connection symbol) so that uncharted neighbor can later be fetched.
	gate, ok, err := repo.GateWaypointOf(ctx, "X1-PA3")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "X1-PA3-I51", gate)
}

// Era filtering (sp-vapw): a dead-era edge (fresh timestamp, but a CLOSED era's
// id) must never leak into a live read — not from Edges, not from Adjacency. This
// is the exact class of bug the PZ28 ghost-gate row caused.
func TestGateEdgeRepository_DeadEraRow_IgnoredByLiveReads(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	closedAt := time.Now().Add(-24 * time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	// era_id 1 = closed (torwind), era_id 2 = open (orion).
	deadEra := 1

	// A dead-era edge, deliberately FRESH-timestamped so only ERA scoping (not TTL)
	// can exclude it.
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-PZ28", ConnectedSystem: "X1-GHOST", GateWaypoint: "X1-GHOST-I1",
		EraID: intPtr(deadEra), SyncedAt: freshTS(),
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	// A live system written this (open) era.
	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"}}))

	// The dead-era system reads as a MISS — its ghost row is scoped out.
	_, ok, err := repo.Edges(ctx, "X1-PZ28")
	require.NoError(t, err)
	require.False(t, ok, "a dead-era edge must not be a live hit")

	// The reverse lookup must not resolve a ghost either.
	_, ok, err = repo.GateWaypointOf(ctx, "X1-GHOST")
	require.NoError(t, err)
	require.False(t, ok, "a dead-era ghost must not resolve a gate waypoint")

	// The overview shows only the live system, never the dead-era one.
	adjacency, err := repo.Adjacency(ctx)
	require.NoError(t, err)
	require.Contains(t, adjacency, "X1-KA42")
	require.NotContains(t, adjacency, "X1-PZ28")
}

// A stale edge set (older than the freshness window) reads as a MISS so the
// caller re-fetches — the lazy-refresh signal.
func TestGateEdgeRepository_StaleRows_ReadAsMiss(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	// A row in the OPEN era but with an expired sync timestamp.
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(),
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	_, ok, err := repo.Edges(context.Background(), "X1-KA42")
	require.NoError(t, err)
	require.False(t, ok, "an edge set older than the freshness window must read as a miss")
}

// Replace is a REPLACE, not a merge: a connection dropped from the new set
// disappears, AND a re-sync purges any dead-era row for that system (delete-then-
// insert across all eras).
func TestGateEdgeRepository_Replace_PurgesOldAndDeadEraRows(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closedAt := time.Now().Add(-24 * time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "torwind", AgentSymbol: "TORWIND", PlayerID: 1, ClosedAt: &closedAt}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 2}).Error)

	// A dead-era row for KA42 that a re-sync should purge.
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-DEAD", GateWaypoint: "X1-DEAD-I1",
		EraID: intPtr(1), SyncedAt: freshTS(),
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
		{ConnectedSystem: "X1-UQ16", GateWaypoint: "X1-UQ16-I9"},
	}))
	// Re-sync with a smaller set: UQ16 has since dropped away.
	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
	}))

	edges, ok, err := repo.Edges(ctx, "X1-KA42")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"X1-PA3"}, connectedSystems(edges), "dropped and dead-era neighbors must be gone")
}

// sp-8qhu: an edge's UnderConstruction flag round-trips through Replace/Edges, so
// the routing BFS can read a neighbor gate's real build state from the cache.
func TestGateEdgeRepository_UnderConstruction_RoundTrip(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-I1", UnderConstruction: true},
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
	}))

	edges, ok, err := repo.Edges(ctx, "X1-KA42")
	require.NoError(t, err)
	require.True(t, ok)

	bySystem := map[string]system.GateEdge{}
	for _, e := range edges {
		bySystem[e.ConnectedSystem] = e
	}
	require.True(t, bySystem["X1-AF2"].UnderConstruction, "the unbuilt neighbor's flag must persist")
	require.False(t, bySystem["X1-PA3"].UnderConstruction, "the open neighbor must stay open")

	// Adjacency carries the flag too (the verb reads it to annotate).
	adjacency, err := repo.Adjacency(ctx)
	require.NoError(t, err)
	var af2 system.GateEdge
	for _, e := range adjacency["X1-KA42"] {
		if e.ConnectedSystem == "X1-AF2" {
			af2 = e
		}
	}
	require.True(t, af2.UnderConstruction, "Adjacency must expose the under-construction flag")
}

// sp-8qhu TTL split: an under-construction edge uses the SHORTER (2h) freshness
// window while a healthy edge keeps 24h. At the SAME 3h age, the under-construction
// set reads as a MISS (re-probe, so a completed build is noticed same-era) but the
// healthy set is still a HIT — proving the window is per-row, not global.
func TestGateEdgeRepository_UnderConstructionTTL_ShorterThanHealthy(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	// Both edge sets are 3h old: past the 2h under-construction window, well within
	// the 24h healthy window.
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-BUILDING", ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-I1",
		EraID: intPtr(1), SyncedAt: agoTS(3 * time.Hour), UnderConstruction: true,
	}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-HEALTHY", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: agoTS(3 * time.Hour), UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	_, ok, err := repo.Edges(ctx, "X1-BUILDING")
	require.NoError(t, err)
	require.False(t, ok, "an under-construction edge older than 2h must read as a miss (re-probe)")

	_, ok, err = repo.Edges(ctx, "X1-HEALTHY")
	require.NoError(t, err)
	require.True(t, ok, "a healthy edge at the same 3h age must still be fresh (24h window)")
}

// sp-8qhu deploy-gap: an EMPTY synced_at — the exact state the migration leaves on
// a pre-tracking row — reads as a MISS (so routing re-fetches + re-probes before
// trusting the row's OPEN default) AND is flagged Stale by Adjacency (so the verb
// marks it unverified rather than authoritative). This is the invalidated live
// KA42→AF2 row the harbormaster caught.
func TestGateEdgeRepository_EmptySyncedAt_MissAndFlaggedStale(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-GATE",
		EraID: intPtr(1), SyncedAt: "", UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	// Routing read: a MISS, so Connections() falls through to a live re-probe.
	_, ok, err := repo.Edges(ctx, "X1-KA42")
	require.NoError(t, err)
	require.False(t, ok, "an empty synced_at row must read as a miss so routing re-probes it")

	// Overview read: present, but flagged Stale for the verb's ? annotation.
	adjacency, err := repo.Adjacency(ctx)
	require.NoError(t, err)
	require.Len(t, adjacency["X1-KA42"], 1)
	require.True(t, adjacency["X1-KA42"][0].Stale, "Adjacency must flag the invalidated row Stale")
}

// --- sp-ikx1: persisted negative-result backoff for unreadable gates ---

// MarkUnreadable persists a backoff marker that UnreadableState reads back, and a repeat
// failure increments the attempt count — the raw facts the service turns into a re-probe
// schedule.
func TestGateEdgeRepository_MarkUnreadable_RoundTripAndIncrements(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	attempts, err := repo.MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", t0)
	require.NoError(t, err)
	require.Equal(t, 1, attempts, "the first failed probe is attempt 1")

	gotAttempts, lastProbe, ok, err := repo.UnreadableState(ctx, "X1-XX56")
	require.NoError(t, err)
	require.True(t, ok, "the marker must read back as backed off")
	require.Equal(t, 1, gotAttempts)
	require.WithinDuration(t, t0, lastProbe, time.Second)

	// A second failure five minutes later increments to attempt 2 and re-stamps the time.
	t1 := t0.Add(5 * time.Minute)
	attempts, err = repo.MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", t1)
	require.NoError(t, err)
	require.Equal(t, 2, attempts)
	gotAttempts, lastProbe, ok, err = repo.UnreadableState(ctx, "X1-XX56")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, gotAttempts)
	require.WithinDuration(t, t1, lastProbe, time.Second)
}

// RULINGS #2: the backoff is persisted, not in-memory, so a daemon restart (a brand-new
// repository over the same DB) resumes it instead of re-storming the API on boot.
func TestGateEdgeRepository_Backoff_SurvivesRestart(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	_, err = persistence.NewGormGateEdgeRepository(db).MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", t0)
	require.NoError(t, err)

	// A fresh repository instance (the restart) still sees the backoff.
	restarted := persistence.NewGormGateEdgeRepository(db)
	attempts, lastProbe, ok, err := restarted.UnreadableState(ctx, "X1-XX56")
	require.NoError(t, err)
	require.True(t, ok, "backoff must survive a restart — a reboot must not re-storm the API")
	require.Equal(t, 1, attempts)
	require.WithinDuration(t, t0, lastProbe, time.Second)
}

// A backoff marker is NOT an edge: a system with only a marker reads as a MISS (routing
// then honors the backoff), and the marker never surfaces in the adjacency overview or
// the frontier scanner's BFS.
func TestGateEdgeRepository_MarkerRow_ExcludedFromEdgesAndAdjacency(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	_, err = repo.MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", time.Now())
	require.NoError(t, err)

	edges, ok, err := repo.Edges(ctx, "X1-XX56")
	require.NoError(t, err)
	require.False(t, ok, "a marker-only system must read as a miss, never a fresh empty hit")
	require.Empty(t, edges)

	adjacency, err := repo.Adjacency(ctx)
	require.NoError(t, err)
	require.NotContains(t, adjacency, "X1-XX56", "a backoff marker must not surface as an edge in the overview")
}

// Item 4/self-heal: when an unreadable gate becomes readable, Replace writes its edges
// AND clears the backoff marker, so the gate rejoins the normal TTL instead of the
// backoff clock.
func TestGateEdgeRepository_Replace_ClearsBackoffMarker(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	_, err = repo.MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", time.Now())
	require.NoError(t, err)
	_, _, ok, _ := repo.UnreadableState(ctx, "X1-XX56")
	require.True(t, ok, "precondition: backed off")

	require.NoError(t, repo.Replace(ctx, "X1-XX56", []system.GateEdge{
		{ConnectedSystem: "X1-NBR", GateWaypoint: "X1-NBR-GATE"},
	}))

	_, _, ok, err = repo.UnreadableState(ctx, "X1-XX56")
	require.NoError(t, err)
	require.False(t, ok, "a successful Replace must clear the backoff marker (self-heal)")

	edges, hit, err := repo.Edges(ctx, "X1-XX56")
	require.NoError(t, err)
	require.True(t, hit, "the now-readable gate's edges must read back as a hit")
	require.Equal(t, []string{"X1-NBR"}, connectedSystems(edges))
}

// Item 5: an era close resets the backoff. The marker is era-scoped exactly like edges,
// so once a new era opens the old marker is out of scope and the gate re-probes fresh.
func TestGateEdgeRepository_Backoff_ResetOnEraClose(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()
	_, err = repo.MarkUnreadable(ctx, "X1-XX56", "X1-XX56-GATE", time.Now())
	require.NoError(t, err)
	_, _, ok, _ := repo.UnreadableState(ctx, "X1-XX56")
	require.True(t, ok, "backed off in era 1")

	// Close era 1 and open era 2 (a universe reset).
	require.NoError(t, db.Model(&persistence.EraModel{}).Where("era_id = ?", 1).
		Update("closed_at", time.Now()).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "rigel", AgentSymbol: "RIGEL", PlayerID: 1}).Error)

	_, _, ok, err = repo.UnreadableState(ctx, "X1-XX56")
	require.NoError(t, err)
	require.False(t, ok, "an era close must reset the backoff — the new era re-probes fresh")
}
