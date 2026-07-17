package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// pruneStaleFakeAPIClient returns a configurable slice of ShipData from
// ListShips so a reconcile test can model the exact live fleet GET /my/ships
// reports (the source of truth), including the case where the live fleet is a
// strict subset of what the DB currently holds.
type pruneStaleFakeAPIClient struct {
	domainPorts.APIClient
	ships []*navigation.ShipData
}

func (f *pruneStaleFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return f.ships, nil
}

// TestSyncAllFromAPI_PrunesDeadEraGhostHull is the sp-wn8u regression.
//
// ROOT CAUSE: the agent TORWIND re-registers on every server reset under a NEW
// players row (new player_id) for the SAME agent_symbol, and ship symbols are
// REUSED across eras — TORWIND-2B was a heavy freighter last era and is a probe
// this era. SyncAllFromAPI only UPSERTs the ships GET /my/ships returns; it
// never removed rows the live API no longer reports. A dead-era player row
// carries a dead token, so its own SyncAllFromAPI fails and its ship rows are
// never touched again — they linger forever as ghosts. Any read that aggregates
// by agent_symbol (not the exact live player_id) then unions the live fleet with
// dead-era rows and reads a stale frame_symbol (the operator's "19 heavies" = 9
// live + 10 dead-era). GET /my/ships is authoritative: after a successful,
// non-empty sync the ONLY rows that should remain for the agent are the ones
// just upserted under the live player_id.
func TestSyncAllFromAPI_PrunesDeadEraGhostHull(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// Two eras of the SAME agent. player rows are created per re-registration.
	deadEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-dead", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&deadEra).Error)
	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	// Dead-era ghost: TORWIND-2B was a heavy freighter last era. Its token is
	// dead, so its own sync never runs again and this row never self-heals.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:    "TORWIND-2B",
		PlayerID:      deadEra.ID,
		FrameSymbol:   "FRAME_HEAVY_FREIGHTER",
		CargoCapacity: 225,
	}).Error)

	// Live API is authoritative: this era, TORWIND-2B is a PROBE (cargo 0).
	apiClient := &pruneStaleFakeAPIClient{ships: []*navigation.ShipData{{
		Symbol:        "TORWIND-2B",
		Location:      "X1-TEST-A1",
		NavStatus:     "IN_ORBIT",
		FrameSymbol:   "FRAME_PROBE",
		CargoCapacity: 0,
	}}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: liveID, Token: "tok-live"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err)

	// The live-era row exists with the authoritative frame.
	var live persistence.ShipModel
	require.NoError(t, db.Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-2B").First(&live).Error)
	require.Equal(t, "FRAME_PROBE", live.FrameSymbol, "live row must carry the API-authoritative frame")

	// The dead-era ghost is gone — pruned by the reconcile.
	var ghostCount int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", deadEra.ID, "TORWIND-2B").Count(&ghostCount).Error)
	require.Equal(t, int64(0), ghostCount, "dead-era ghost hull must be pruned")

	// The reported corruption is resolved: the operator's agent-scoped heavy
	// count now returns 0, not the phantom count that mislabeled probes as heavies.
	var heavyCount int64
	require.NoError(t, db.Raw(
		`SELECT count(*) FROM ships s JOIN players p ON p.id = s.player_id `+
			`WHERE p.agent_symbol = ? AND s.frame_symbol = ?`,
		"TORWIND", "FRAME_HEAVY_FREIGHTER").Scan(&heavyCount).Error)
	require.Equal(t, int64(0), heavyCount, "no stale heavy-freighter ghosts may remain for the agent")
}

// TestSyncAllFromAPI_DoesNotPruneOtherAgentsShips is the scoping-safety guard on
// the cross-player delete: the reconcile is agent-scoped, so syncing TORWIND's
// live fleet must never touch a DIFFERENT agent's rows. Without this bound a
// broadened prune could wipe another agent's whole fleet.
func TestSyncAllFromAPI_DoesNotPruneOtherAgentsShips(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	otherAgent := persistence.PlayerModel{AgentSymbol: "OTHER", Token: "tok-other", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&otherAgent).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:    "OTHER-1",
		PlayerID:      otherAgent.ID,
		FrameSymbol:   "FRAME_FRIGATE",
		CargoCapacity: 40,
	}).Error)

	apiClient := &pruneStaleFakeAPIClient{ships: []*navigation.ShipData{{
		Symbol:      "TORWIND-9",
		Location:    "X1-TEST-A1",
		NavStatus:   "IN_ORBIT",
		FrameSymbol: "FRAME_PROBE",
	}}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: liveID, Token: "tok-live"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err)

	var otherCount int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", otherAgent.ID, "OTHER-1").Count(&otherCount).Error)
	require.Equal(t, int64(1), otherCount, "a different agent's ship must survive TORWIND's fleet reconcile")
}

// TestSyncAllFromAPI_PrunesHullNoLongerReturnedByAPI covers the within-era half
// of the reconcile: a hull sold/destroyed this era (so GET /my/ships no longer
// returns it) must be pruned from its OWN live player_id, keyed on the live set —
// never on the stale column. Distinct code path from the dead-era case (same
// player_id, absent from the live set) rather than a foreign player_id.
func TestSyncAllFromAPI_PrunesHullNoLongerReturnedByAPI(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	// Two ships under the SAME live player; TORWIND-99 was sold and is absent
	// from the live API response below.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-10", PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-99", PlayerID: liveEra.ID, FrameSymbol: "FRAME_LIGHT_FREIGHTER", CargoCapacity: 80,
	}).Error)

	apiClient := &pruneStaleFakeAPIClient{ships: []*navigation.ShipData{{
		Symbol:      "TORWIND-10",
		Location:    "X1-TEST-A1",
		NavStatus:   "IN_ORBIT",
		FrameSymbol: "FRAME_PROBE",
	}}}
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: liveID, Token: "tok-live"}}
	repo := NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)

	_, err = repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err)

	var stillLive int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-10").Count(&stillLive).Error)
	require.Equal(t, int64(1), stillLive, "a still-live hull must be kept")

	var sold int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-99").Count(&sold).Error)
	require.Equal(t, int64(0), sold, "a hull no longer returned by the live API must be pruned")
}
