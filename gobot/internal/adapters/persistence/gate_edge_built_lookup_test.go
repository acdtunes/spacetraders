package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// A gate already recorded BUILT, within its trust window and the open era, is
// answered from the store — this is the read that replaces the per-edge live
// construction probe.
func TestRecordedBuiltGate_FreshBuiltRowIsBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.True(t, built, "a fresh, era-scoped, built gate row must answer built")
}

// GUARD: an UNDER-CONSTRUCTION gate is NEVER answered from the store. Its state
// can still change (a build completes), and serving it optimistically would route
// a laden hull into an unbuilt gate. The caller must re-probe it live.
func TestRecordedBuiltGate_UnderConstructionRowIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-I90",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: true,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-AF2-I90")
	require.NoError(t, err)
	require.False(t, built, "an under-construction gate must never be served from the store")
}

// GUARD: a row past its freshness window is not trusted — the same bound Edges()
// applies, so this read adds no staleness the routing cache does not already carry.
func TestRecordedBuiltGate_StaleRowIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(), UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.False(t, built, "a row past the freshness window must not be trusted")
}

// The configured freshness window governs the read, exactly as it governs Edges().
func TestRecordedBuiltGate_HonorsConfiguredFreshWindow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: agoTS(90 * time.Minute), UnderConstruction: false,
	}).Error)

	ctx := context.Background()
	wide := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(4*time.Hour))
	built, err := wide.RecordedBuiltGate(ctx, "X1-PA3-I51")
	require.NoError(t, err)
	require.True(t, built, "inside the configured window the row is trusted")

	narrow := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(30*time.Minute))
	built, err = narrow.RecordedBuiltGate(ctx, "X1-PA3-I51")
	require.NoError(t, err)
	require.False(t, built, "outside the configured window the row is not trusted")
}

// GUARD: an era boundary invalidates the record. A new universe resets every gate
// to under-construction, so a previous era's "built" verdict must never carry over.
func TestRecordedBuiltGate_DeadEraRowIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closed := time.Now().Add(-time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "old", AgentSymbol: "OLD", PlayerID: 1, ClosedAt: &closed}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "new", AgentSymbol: "NEW", PlayerID: 2}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.False(t, built, "a dead-era row must never answer built")
}

// An unknown gate is simply not recorded — the caller probes it live.
func TestRecordedBuiltGate_UnknownGateIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-ZZ99-I01")
	require.NoError(t, err)
	require.False(t, built)
}

// A gate recorded by several neighbours is built when ANY fresh row says so; a
// single stale or under-construction sibling row must not mask a valid record,
// and must not, on its own, manufacture one.
func TestRecordedBuiltGate_MultipleRecordingNeighbours(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(), UnderConstruction: false,
	}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-UQ16", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.True(t, built, "one fresh built record is enough")
}

// GUARD: the negative-result BACKOFF MARKER (connected_system = "") is not an edge
// and carries no construction verdict. A marker row must never answer built —
// under_construction defaults to false on it, so reading markers as edges would
// declare every unreadable frontier gate "built" and route hulls into it.
func TestRecordedBuiltGate_BackoffMarkerIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-FRONTIER", ConnectedSystem: "", GateWaypoint: "X1-FRONTIER-I01",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: false,
		UnreadableSince: freshTS(), AttemptCount: 3,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-FRONTIER-I01")
	require.NoError(t, err)
	require.False(t, built, "a backoff marker is not a construction record")
}
