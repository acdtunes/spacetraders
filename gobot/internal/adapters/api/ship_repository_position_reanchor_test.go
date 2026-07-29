package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// recordingReanchorObserver captures the re-anchors published at the observation seam.
type recordingReanchorObserver struct {
	seen []PositionReanchor
}

func (o *recordingReanchorObserver) ShipPositionReanchored(_ context.Context, reanchor PositionReanchor) {
	o.seen = append(o.seen, reanchor)
}

// syncReanchorHarness stands up a real DB, a seeded ships row and a repository whose API
// returns the position the SERVER reports. Returning the observer lets each test state
// what it expects to have been published.
func syncReanchorHarness(t *testing.T, rowSystem, rowWaypoint, serverWaypoint string) (*ShipRepository, *recordingReanchorObserver, shared.PlayerID) {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-41",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "idle",
		SystemSymbol:     rowSystem,
		LocationSymbol:   rowWaypoint,
		NavStatus:        "IN_ORBIT",
	}).Error)

	apiClient := &syncPreserveFleetFakeAPIClient{shipData: &navigation.ShipData{
		Symbol:      "TORWIND-41",
		Location:    serverWaypoint,
		NavStatus:   "IN_ORBIT",
		EngineSpeed: 10,
		FrameSymbol: "FRAME_EXPLORER",
		Role:        "EXPLORER",
	}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}

	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
	observer := &recordingReanchorObserver{}
	repo.SetPositionReanchorObserver(observer)
	return repo, observer, playerID
}

// TestSyncShipFromAPI_ShoutsWhenItCorrectsASystemWeHadWrong is the observability half of
// the TORWIND-41 hunt. The hull's row named X1-GF41; the server says X1-KC84. The sync
// silently corrected exactly this, over and over, while the daemon went on failing — so
// the only trace the incident ever left was a downstream 4255 pointing at the router.
// A correction is proof our durable state was WRONG, and it must be published with both
// systems named so the next occurrence hands over the evidence rather than the symptom.
func TestSyncShipFromAPI_ShoutsWhenItCorrectsASystemWeHadWrong(t *testing.T) {
	repo, observer, playerID := syncReanchorHarness(t, "X1-GF41", "X1-GF41-I57", "X1-KC84-A1")

	_, err := repo.SyncShipFromAPI(context.Background(), "TORWIND-41", playerID)
	require.NoError(t, err)

	require.Len(t, observer.seen, 1, "a sync that corrected the hull's SYSTEM must publish exactly one re-anchor")
	got := observer.seen[0]
	require.Equal(t, "TORWIND-41", got.ShipSymbol)
	require.Equal(t, playerID.Value(), got.PlayerID)
	require.Equal(t, "X1-GF41", got.BelievedSystem, "the signal must name the system we wrongly believed — that is the evidence")
	require.Equal(t, "X1-KC84", got.ActualSystem, "and the system the hull is actually in")
	require.Equal(t, "X1-GF41-I57", got.BelievedWaypoint)
	require.Equal(t, "X1-KC84-A1", got.ActualWaypoint)
}

// TestSyncShipFromAPI_StaysSilentWhenTheRowWasAlreadyRight is the other half, and it is
// load-bearing: SyncShipFromAPI runs on many ordinary paths, so an alarm that fired on
// every sync would be exactly the un-read noise this signal exists to replace. Agreement
// between the row and the server is the normal case and must say nothing at all.
func TestSyncShipFromAPI_StaysSilentWhenTheRowWasAlreadyRight(t *testing.T) {
	repo, observer, playerID := syncReanchorHarness(t, "X1-KC84", "X1-KC84-A1", "X1-KC84-B2")

	_, err := repo.SyncShipFromAPI(context.Background(), "TORWIND-41", playerID)
	require.NoError(t, err)

	require.Empty(t, observer.seen,
		"a hull that merely moved between waypoints of the SAME system is ordinary traffic, not a lost cross-system write")
}

// TestSyncAllFromAPI_ShoutsWhenThePeriodicResyncFindsAHullInAnotherSystem covers the
// broadest detector: the ~hourly full-fleet resync sees EVERY hull without waiting for a
// coordinator to happen to re-anchor one, so it is the pass most likely to be the first
// witness of a lost cross-system write. It must report the contradiction, not quietly
// overwrite it — the batch path has its own preserve block and its own upsert, so its
// silence would have to be fixed separately from the single-ship path's.
func TestSyncAllFromAPI_ShoutsWhenThePeriodicResyncFindsAHullInAnotherSystem(t *testing.T) {
	repo, observer, playerID := syncReanchorHarness(t, "X1-GF41", "X1-GF41-I57", "X1-KC84-A1")

	_, err := repo.SyncAllFromAPI(context.Background(), playerID)
	require.NoError(t, err)

	require.Len(t, observer.seen, 1, "the periodic resync must report a hull it found in a different system from its row")
	require.Equal(t, "X1-GF41", observer.seen[0].BelievedSystem)
	require.Equal(t, "X1-KC84", observer.seen[0].ActualSystem)
	require.Equal(t, "TORWIND-41", observer.seen[0].ShipSymbol)
}
