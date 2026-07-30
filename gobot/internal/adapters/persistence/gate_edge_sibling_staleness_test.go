package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// gate_edge_sibling_staleness_test.go pins the boundary between the two questions the
// stored gate cache is asked, which a single `stale` verdict used to conflate:
//
//	"does this system need a re-probe?"      — PER-ROW. An under-construction gate
//	                                           completes on its own clock, so its row
//	                                           is chased on a short (2h) window.
//	"may I trust this set for routing?"      — WHOLE-SET. A genuinely old set has
//	                                           unverifiable topology and is condemned.
//
// A system's edges are written in ONE Replace under a SINGLE synced_at, so every row
// shares a timestamp. Answering the second question with the first — any stale row
// condemns the set — therefore let ONE still-building exit erase its system's entire
// topology 2h after the last probe, INCLUDING its built, static, permanently-valid
// exits. The system became a WALL in every BFS and routing refused provably-existing
// routes: 173 of 1,168 live systems at once, freezing 266 already-bought probes.
//
// The fixture below is that exact production shape, and it is the one that must not
// regress: a MIXED set (one under-construction row, one built row) under a SINGLE
// synced_at aged past the 2h window but well inside the 24h one.

// mixedAgedSet writes X1-RJ93's real production edge shape: several built exits and one
// still-building exit, all stamped with ONE synced_at at the given age — exactly what
// Replace produces. ageBeyond2hWithin24h is the window where the bug lived.
func mixedAgedSet(t *testing.T, db *gorm.DB, age time.Duration) {
	t.Helper()
	ts := agoTS(age)
	rows := []persistence.GateEdgeModel{
		{SystemSymbol: "X1-RJ93", ConnectedSystem: "X1-XX80", GateWaypoint: "X1-XX80-I1", EraID: intPtr(1), SyncedAt: ts, UnderConstruction: true},
		{SystemSymbol: "X1-RJ93", ConnectedSystem: "X1-AX76", GateWaypoint: "X1-AX76-I2", EraID: intPtr(1), SyncedAt: ts, UnderConstruction: false},
		{SystemSymbol: "X1-RJ93", ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51", EraID: intPtr(1), SyncedAt: ts, UnderConstruction: false},
	}
	for _, r := range rows {
		require.NoError(t, db.Create(&r).Error)
	}
}

// THE DEFECT. One still-building exit, 3h after the last probe, used to condemn the
// whole set — so a system with two perfectly good built exits reported NOTHING and
// walled off every route through it. The built siblings must survive their neighbour's
// shorter clock: a static gate does not become unknown because a DIFFERENT gate in the
// same system is still being built.
func TestGateEdgeRepository_OneBuildingExit_DoesNotCondemnItsBuiltSiblings(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	mixedAgedSet(t, db, 3*time.Hour)

	repo := persistence.NewGormGateEdgeRepository(db)
	edges, ok, err := repo.Edges(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.True(t, ok,
		"the set was condemned WHOLE by one under-construction sibling 3h old; its two built exits are "+
			"well inside the 24h window and must still be readable, or this system is a wall in every BFS")
	require.Equal(t, []string{"X1-AX76", "X1-PA3", "X1-XX80"}, connectedSystems(edges),
		"every row must still be returned — the caller decides passability from UnderConstruction")
}

// The re-probe signal SURVIVES, relocated. Condemning the set was how the 2h clock used
// to reach the fetch-through resolver; dropping it outright would hold a "still building"
// verdict for a full day and refuse a now-valid route. It now rides the PER-ROW Stale flag,
// which is set on exactly the row whose own window expired and on no other.
func TestGateEdgeRepository_MixedAgedSet_FlagsOnlyTheBuildingRowForReprobe(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	mixedAgedSet(t, db, 3*time.Hour)

	repo := persistence.NewGormGateEdgeRepository(db)
	edges, ok, err := repo.Edges(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.True(t, ok)

	stale := map[string]bool{}
	for _, e := range edges {
		stale[e.ConnectedSystem] = e.Stale
	}
	require.True(t, stale["X1-XX80"],
		"the under-construction row is 3h past its 2h window and MUST read Stale — this is the signal "+
			"the fetch-through resolver reads to re-probe the build, and losing it holds the verdict 24h")
	require.False(t, stale["X1-AX76"], "a built row 3h old is inside its 24h window and is NOT stale")
	require.False(t, stale["X1-PA3"], "a built row 3h old is inside its 24h window and is NOT stale")
}

// PRESERVED: the 24h whole-set window keeps its meaning. Past it the BUILT rows are
// themselves unverified, so the set is condemned whole exactly as before — the fix
// narrows which staleness condemns, never how long a set is trusted.
func TestGateEdgeRepository_GenuinelyOldSet_StillCondemnedWhole(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	mixedAgedSet(t, db, 25*time.Hour)

	repo := persistence.NewGormGateEdgeRepository(db)
	_, ok, err := repo.Edges(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.False(t, ok,
		"at 25h the BUILT rows are past their own 24h window, so the whole set is unverified and must "+
			"still read as a miss — the under-construction exemption must not extend the healthy window")
}

// PRESERVED: an unknown-age row condemns the set whatever it claims to be. The
// deploy-time cache invalidation blanks synced_at, and a row of unknown age has an
// unverified UnderConstruction flag too — so it cannot earn the under-construction
// exemption on the strength of the very field that cannot be trusted.
func TestGateEdgeRepository_UnknownAgeRow_StillCondemnsSet_EvenWhenUnderConstruction(t *testing.T) {
	for _, c := range []struct {
		name     string
		syncedAt string
	}{
		{name: "empty synced_at (the migration's invalidation)", syncedAt: ""},
		{name: "unparseable synced_at", syncedAt: "not-a-timestamp"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := database.NewTestConnection()
			require.NoError(t, err)
			require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

			// The unknown-age row is the UNDER-CONSTRUCTION one, so only an exemption that
			// ignored unknown age could wrongly admit this set. Its sibling is genuinely fresh.
			require.NoError(t, db.Create(&persistence.GateEdgeModel{
				SystemSymbol: "X1-RJ93", ConnectedSystem: "X1-XX80", GateWaypoint: "X1-XX80-I1",
				EraID: intPtr(1), SyncedAt: c.syncedAt, UnderConstruction: true,
			}).Error)
			require.NoError(t, db.Create(&persistence.GateEdgeModel{
				SystemSymbol: "X1-RJ93", ConnectedSystem: "X1-AX76", GateWaypoint: "X1-AX76-I2",
				EraID: intPtr(1), SyncedAt: freshTS(), UnderConstruction: false,
			}).Error)

			repo := persistence.NewGormGateEdgeRepository(db)
			_, ok, err := repo.Edges(context.Background(), "X1-RJ93")
			require.NoError(t, err)
			require.False(t, ok,
				"a row of UNKNOWN age must condemn the set even while flagged under-construction: the "+
					"flag itself is unverified, and the deploy-time invalidation relies on this")
		})
	}
}

// The configured healthy window is what the condemnation is measured against, not the
// hardcoded 24h default. A shortened topology TTL must still condemn a mixed set once the
// BUILT rows pass it, or the operator's knob would silently stop applying to any system
// that happens to have a gate under construction.
func TestGateEdgeRepository_CondemnationHonorsConfiguredWindow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)
	mixedAgedSet(t, db, 3*time.Hour)

	// A 1h topology TTL: the built rows at 3h are now past it too.
	repo := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(1*time.Hour))
	_, ok, err := repo.Edges(context.Background(), "X1-RJ93")
	require.NoError(t, err)
	require.False(t, ok,
		"with a 1h configured window the 3h BUILT rows are stale, so the set must be condemned — the "+
			"under-construction exemption is measured against the CONFIGURED window, not a fixed 24h")
}
