package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// TestSave_PreservesLiveDedicationAgainstStaleShipWriter is the core
// regression. AssignFleet is the single write path for the dedicated_fleet tag,
// but the general ship Save path upserts EVERY column (UpdateAll) from the
// domain ship's in-memory snapshot — including dedicated_fleet (shipToModel).
// A coordinator that loaded a hull BEFORE it was dedicated carries a stale ""
// tag; its next Save (a routine nav/cargo write-back) silently resurrects that
// "" over the live `fleet add --operation manufacturing` dedication. Worse, the
// version guard is BLIND to it: AssignFleet does not bump ships.version,
// so the CAS upsert's `WHERE version = <loaded>` still matches and clobbers.
// This is the silent drop that stalled the gate FAB — no error, no warn.
func TestSave_PreservesLiveDedicationAgainstStaleShipWriter(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-17", "IN_ORBIT", 100)

	// A coordinator loads the hull into memory while it is still undedicated —
	// the stale snapshot (dedicated_fleet == "").
	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-17", playerID)
	require.NoError(t, err)
	require.Equal(t, "", stale.DedicatedFleet(), "precondition: snapshot is undedicated")

	before := dedicatedFleetClobbersPrevented.Load()

	// The captain dedicates the hull live: `fleet add --operation manufacturing`.
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-17", "manufacturing", playerID))

	// The coordinator finishes its leg and writes the hull back through the
	// general Save path. It must NOT resurrect its stale "" tag.
	require.NoError(t, repo.Save(context.Background(), stale))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-17").First(&model).Error)
	require.Equal(t, "manufacturing", model.DedicatedFleet,
		"a stale ship writer must not silently drop a live `fleet add` dedication (sp-90a3)")

	// The prevented drop must be observable, not silent.
	require.Equal(t, before+1, dedicatedFleetClobbersPrevented.Load(),
		"a prevented dedication clobber must be counted + logged, not silently swallowed")
}

// TestSave_StaleWriterStillWinsNonDedicationColumns pins the fix's blast radius:
// the tag is preserved, but every OTHER column keeps today's last-write-wins
// behavior. A stale writer that refuelled must still land its fuel —
// the fix protects the single-write-path dedication tag, nothing else.
func TestSave_StaleWriterStillWinsNonDedicationColumns(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-20", "IN_ORBIT", 100)

	stale, err := repo.FindBySymbol(context.Background(), "TORWIND-20", playerID)
	require.NoError(t, err)

	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-20", "manufacturing", playerID))

	require.NoError(t, stale.Refuel(50))
	require.NoError(t, repo.Save(context.Background(), stale))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-20").First(&model).Error)
	require.Equal(t, "manufacturing", model.DedicatedFleet, "the dedication tag is preserved")
	require.Equal(t, 150, model.FuelCurrent, "non-tag columns keep last-write-wins (fix is scoped to the tag)")
}

// TestSave_SiblingDedicationSurvivesRemovalOfAnotherHull reproduces the exact
// production symptom: a `fleet remove` of ONLY TORWIND-18 also dropped
// TORWIND-17. Per-hull AssignFleet is row-isolated, so 18's removal never
// touches 17's row directly — but a coordinator holding a stale pre-dedication
// snapshot of 17 clobbers 17's tag on its next Save, and the operator observes
// the loss right after removing 18. 17's dedication must survive both 18's
// removal and the stale write-back.
func TestSave_SiblingDedicationSurvivesRemovalOfAnotherHull(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)
	seedShip(t, db, playerID.Value(), "TORWIND-17", "IN_ORBIT", 100)
	seedShip(t, db, playerID.Value(), "TORWIND-18", "IN_ORBIT", 100)

	// The coordinator holds a stale pre-dedication snapshot of 17.
	stale17, err := repo.FindBySymbol(context.Background(), "TORWIND-17", playerID)
	require.NoError(t, err)

	// Both hulls are dedicated to the manufacturing (gate) fleet.
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-17", "manufacturing", playerID))
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-18", "manufacturing", playerID))

	// The captain removes ONLY 18.
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-18", "", playerID))

	// The coordinator writes 17 back through Save with its stale snapshot.
	require.NoError(t, repo.Save(context.Background(), stale17))

	var m17, m18 persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-17").First(&m17).Error)
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-18").First(&m18).Error)
	require.Equal(t, "manufacturing", m17.DedicatedFleet,
		"removing 18 (plus a stale write-back) must not drop 17's dedication (sp-90a3)")
	require.Equal(t, "", m18.DedicatedFleet, "18 was explicitly removed and must stay cleared")
}

// TestSave_DurableDedicationSurvivesRestartReDispatchBurst is the durability
// scenario: N hulls dedicated to the manufacturing fleet must all survive a
// daemon restart. On restart every coordinator reloads its workers and
// re-dispatches them — a burst of Save calls. Any worker whose Ship object was
// materialised before the dedication (or from a source that dropped the tag)
// would silently clobber it in that burst, turning N dedicated hulls into fewer
// with no warning. All N must remain dedicated.
func TestSave_DurableDedicationSurvivesRestartReDispatchBurst(t *testing.T) {
	repo, db, playerID := newShipWriteTestRepo(t)

	symbols := []string{"TORWIND-8", "TORWIND-9", "TORWIND-10", "TORWIND-16", "TORWIND-17", "TORWIND-18"}
	stale := make([]*navigation.Ship, 0, len(symbols))
	for _, symbol := range symbols {
		seedShip(t, db, playerID.Value(), symbol, "IN_ORBIT", 100)
		// Pre-dedication snapshot, as a coordinator would hold across the restart.
		ship, err := repo.FindBySymbol(context.Background(), symbol, playerID)
		require.NoError(t, err)
		stale = append(stale, ship)
		require.NoError(t, repo.AssignFleet(context.Background(), symbol, "manufacturing", playerID))
	}

	// The restart re-dispatch burst: every stale worker written back.
	for _, ship := range stale {
		require.NoError(t, repo.Save(context.Background(), ship))
	}

	var count int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("dedicated_fleet = ?", "manufacturing").Count(&count).Error)
	require.Equal(t, int64(len(symbols)), count,
		"all dedicated hulls must survive the restart re-dispatch burst (sp-90a3 durability)")
}
