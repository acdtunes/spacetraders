package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// TORWIND-5 IS STILL OURS. This is the hazard the whole slice turns on: making the
// fleet read survive a poisoned page hands SyncAllFromAPI a fleet with one hull
// missing, and everything downstream counts hulls out of the ships table. If the
// unreadable hull reads as ABSENT, the prune deletes its row, the autosizer sees a
// 4-hull fleet, and bootstrap buys a REPLACEMENT for a ship we still own.
//
// The assertion that matters is the COUNT: five rows before, five rows after.
//
// This drives the real *SpaceTradersClient against a server that reproduces the
// live failure window-for-window, so the repository, the isolation sweep and the
// prune guard are all on the path — a fake client here would prove only that the
// fake was wired up.
func TestSyncAllFromAPI_PoisonedPageKeepsTheUnreadableHullPresent(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	for _, symbol := range []string{"TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4"} {
		require.NoError(t, db.Create(&persistence.ShipModel{
			ShipSymbol: symbol, PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
		}).Error)
	}
	// The poisoned hull: alive, ours, dedicated to the contract fleet — and
	// unreadable because its cargo record is corrupt on SpaceTraders' server.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-5", PlayerID: liveEra.ID,
		FrameSymbol: "FRAME_HEAVY_FREIGHTER", CargoCapacity: 225, DedicatedFleet: "contract",
	}).Error)

	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisoned: map[int]bool{4: true},
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	repo := newPartialReadRepo(t, client, liveID, db)

	synced, err := repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err, "one unreadable hull must not fail the whole refresh — this is the two-day production freeze")
	require.Equal(t, 4, synced, "the four readable hulls are persisted")

	var total int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ?", liveEra.ID).Count(&total).Error)
	require.Equal(t, int64(5), total,
		"the fleet must still count FIVE hulls: an unreadable hull is PRESENT-BUT-UNKNOWN, and under-counting it is how bootstrap buys a replacement for a ship we still own")

	var poisoned persistence.ShipModel
	require.NoError(t,
		db.Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-5").First(&poisoned).Error,
		"the unreadable hull's row must survive: absence from a partial read means UNREADABLE, not sold")
	require.Equal(t, "FRAME_HEAVY_FREIGHTER", poisoned.FrameSymbol, "its row must be left exactly as it was, not zeroed")
	require.Equal(t, "contract", poisoned.DedicatedFleet, "its fleet pin must survive too")
}

// The counter and the named log line. The ONLY reason the freeze was found is that
// a human read 573 identical log lines by hand; a two-day outage must never again
// be invisible. The client cannot name the hull — a 500 carries no payload to name
// it from — so the repository names it by diffing the readable fleet against our
// own rows, which is exactly the evidence an operator would use.
func TestSyncAllFromAPI_PoisonedPageNamesTheUnreadableHull(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	for _, symbol := range []string{"TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"} {
		require.NoError(t, db.Create(&persistence.ShipModel{
			ShipSymbol: symbol, PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
		}).Error)
	}

	pager := &poisonedFleetServer{
		fleet:    fleetOf("TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4", "TORWIND-5"),
		poisoned: map[int]bool{4: true},
	}
	client, closeFn := newTestClient(pager.handler())
	defer closeFn()

	repo := newPartialReadRepo(t, client, liveID, db)

	named := repo.unreadableHullNames(context.Background(), liveID,
		[]string{"TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-4"},
		FleetReadReport{Unreadable: []UnreadableShip{{Page: 1, Index: 4, Reason: "server refused this hull"}}})

	require.Equal(t, []string{"TORWIND-5"}, named,
		"the unreadable hull must be named, not merely counted — an unnamed 500 is what makes this invisible")
}

// The blind spot the read-before/never-read distinction exposes. A hull the API
// refuses that we have NO row for cannot be named from our own records, and the
// 500 carries no symbol either — so the naming loop had nothing to iterate and
// emitted NOTHING: no log, no counter, for a failure that is real. That is the
// exact invisibility this observability exists to end, so an unattributable
// unreadable hull must still be reported, under a sentinel.
//
// We must NOT invent a row for it. A row under an unknown symbol is the corrupt
// row decodeFleetElement's symbol check already refuses to create.
func TestSyncAllFromAPI_UnattributableUnreadableHullIsStillReported(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	// Every row we hold read fine; the unreadable hull is one we have never seen.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1", PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
	}).Error)

	client, closeFn := newTestClient((&poisonedFleetServer{fleet: fleetOf("TORWIND-1")}).handler())
	defer closeFn()
	repo := newPartialReadRepo(t, client, liveID, db)

	named := repo.unreadableHullNames(context.Background(), liveID, []string{"TORWIND-1"},
		FleetReadReport{Unreadable: []UnreadableShip{{Page: 1, Index: 7, Reason: "server refused this hull"}}})

	require.Equal(t, []string{UnidentifiedHull}, named,
		"an unreadable hull we cannot attribute to any row must still be reported — silence here is the invisible failure")

	var rows int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).Where("player_id = ?", liveEra.ID).Count(&rows).Error)
	require.Equal(t, int64(1), rows, "reporting an unattributable hull must not invent a row for it")
}

// When the payload DID give up a symbol (a decode failure, not a refused page),
// that symbol is the truth and must win over both the row diff and the sentinel —
// it names a hull we may have no row for at all.
func TestSyncAllFromAPI_ReportSymbolNamesAHullWeHaveNoRowFor(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-1", PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
	}).Error)

	client, closeFn := newTestClient((&poisonedFleetServer{fleet: fleetOf("TORWIND-1")}).handler())
	defer closeFn()
	repo := newPartialReadRepo(t, client, liveID, db)

	named := repo.unreadableHullNames(context.Background(), liveID, []string{"TORWIND-1"},
		FleetReadReport{Unreadable: []UnreadableShip{{Page: 1, Index: 7, Symbol: "TORWIND-9", Reason: "nav: cannot unmarshal"}}})

	require.Equal(t, []string{"TORWIND-9"}, named,
		"a symbol the payload yielded names the hull exactly; falling back to the sentinel there discards evidence we have")
}

// Anti-vacuity control for the namer: a COMPLETE read has nothing to name. Without
// this, a namer that returned every row would pass the test above and then flood
// the log (and the counter's ship label) on every healthy sync.
func TestSyncAllFromAPI_CompleteReadNamesNoUnreadableHull(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	for _, symbol := range []string{"TORWIND-1", "TORWIND-2"} {
		require.NoError(t, db.Create(&persistence.ShipModel{
			ShipSymbol: symbol, PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
		}).Error)
	}

	client, closeFn := newTestClient((&poisonedFleetServer{fleet: fleetOf("TORWIND-1", "TORWIND-2")}).handler())
	defer closeFn()
	repo := newPartialReadRepo(t, client, liveID, db)

	named := repo.unreadableHullNames(context.Background(), liveID, []string{"TORWIND-1", "TORWIND-2"}, FleetReadReport{})
	require.Empty(t, named, "a complete read has no unreadable hull to name — not even the sentinel")
}
