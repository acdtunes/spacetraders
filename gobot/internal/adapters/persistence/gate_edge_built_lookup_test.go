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

// THE RECOVERY. A finished build is a PERMANENT fact, so age cannot invalidate it.
// This matters because the refresh path keys on staleness: the sets being refreshed
// are by construction the ones holding stale rows, so rejecting a stale built verdict
// rejects exactly the rows the probe was meant to spare.
func TestRecordedBuiltGate_StaleBuiltRowIsStillBuilt(t *testing.T) {
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
	require.True(t, built, "a completed build does not expire within an era")
}

// GUARD: a stale UNDER-CONSTRUCTION row is still not served. That verdict is the
// mutable one — the build may since have finished — so it always goes live.
func TestRecordedBuiltGate_StaleUnderConstructionRowStillFallsThrough(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-I90",
		EraID: intPtr(1), SyncedAt: staleTS(), UnderConstruction: true,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-AF2-I90")
	require.NoError(t, err)
	require.False(t, built)
}

// The verdict is age-INDEPENDENT: no freshness window, however narrow, may reject it.
func TestRecordedBuiltGate_BuiltVerdictIgnoresTheFreshnessWindow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: agoTS(90 * time.Minute), UnderConstruction: false,
	}).Error)

	ctx := context.Background()
	for _, window := range []time.Duration{time.Nanosecond, 30 * time.Minute, 4 * time.Hour} {
		repo := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(window))
		built, err := repo.RecordedBuiltGate(ctx, "X1-PA3-I51")
		require.NoError(t, err)
		require.True(t, built, "window %s must not reject a permanent fact", window)
	}
}

// GUARD — THE ONE AGE-LIKE CHECK THAT SURVIVES. An EMPTY synced_at does not mean
// "observed long ago", it means NEVER OBSERVED: under_construction is a column
// default (false), not a probe result. The schema migration that introduced the
// column deliberately blanks synced_at for exactly this reason, so that pre-tracking
// rows are re-probed before routing trusts them. Serving those as built would route a
// hull through a gate whose build state was never read.
func TestRecordedBuiltGate_NeverObservedRowIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: "", UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.False(t, built, "an unobserved default is not a construction record")
}

// GUARD: an unparseable timestamp is likewise not evidence of an observation.
func TestRecordedBuiltGate_UnparseableSyncedAtIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: "not-a-timestamp", UnderConstruction: false,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.False(t, built)
}

// GUARD: an era boundary invalidates the record. A new universe resets every gate
// to under-construction, so a previous era's "built" verdict must never carry over.
func TestRecordedBuiltGate_DeadEraRowIsNotBuilt(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closed := time.Now().Add(-time.Hour)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "old", AgentSymbol: "OLD", PlayerID: 1, ClosedAt: &closed}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "new", AgentSymbol: "NEW", PlayerID: 2}).Error)
	// Deliberately STALE as well: age no longer rejects a built verdict, so this pins
	// that era scoping — not the freshness window — is what contains the reset boundary.
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(), UnderConstruction: false,
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

// A gate can be recorded by several neighbours. One OBSERVED built record is enough,
// even when a sibling row says under construction: that sibling is not necessarily an
// observation — a FAILED probe is written as under-construction by design (fail closed),
// so it must not be able to override a neighbour that actually saw the gate finished.
// Monotonicity is what makes this safe: nothing can un-build the gate afterwards.
func TestRecordedBuiltGate_ObservedBuiltWinsOverASiblingUnderConstructionRow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-KA42", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: staleTS(), UnderConstruction: false,
	}).Error)
	require.NoError(t, db.Create(&persistence.GateEdgeModel{
		SystemSymbol: "X1-UQ16", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51",
		EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: true,
	}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	built, err := repo.RecordedBuiltGate(context.Background(), "X1-PA3-I51")
	require.NoError(t, err)
	require.True(t, built, "an observed built record survives a sibling fail-closed row")
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
