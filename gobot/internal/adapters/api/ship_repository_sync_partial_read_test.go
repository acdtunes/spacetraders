package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// partialReadFakeAPIClient is the real client's shape: it satisfies the domain
// port AND the richer fleetReadReporter, so SyncAllFromAPI takes the reporting
// path exactly as it does in production.
type partialReadFakeAPIClient struct {
	domainPorts.APIClient
	ships  []*navigation.ShipData
	report FleetReadReport
}

func (f *partialReadFakeAPIClient) ListShips(_ context.Context, _ string) ([]*navigation.ShipData, error) {
	return f.ships, nil
}

func (f *partialReadFakeAPIClient) ListShipsWithReport(_ context.Context, _ string) ([]*navigation.ShipData, FleetReadReport, error) {
	return f.ships, f.report, nil
}

func newPartialReadRepo(t *testing.T, apiClient domainPorts.APIClient, playerID shared.PlayerID, db *gorm.DB) *ShipRepository {
	t.Helper()
	playerRepo := &syncPreserveOwnerFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-live"}}
	return NewShipRepository(apiClient, playerRepo, nil, syncPreserveOwnerFakeWaypointProvider{}, db, nil)
}

// THE hazard this slice had to close. Making the fleet read survive an
// unreadable hull hands SyncAllFromAPI a PARTIAL fleet, and reconcileFleetToLive
// deletes every row missing from what it is given — so the resilience fix, on its
// own, converts "TORWIND-5 breaks the read" into "TORWIND-5's row is deleted as
// though it were sold". The hull already in trouble is the one that gets erased.
// A partial read must be strictly non-destructive.
func TestSyncAllFromAPI_PartialReadDoesNotPruneUnreadableHull(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-4", PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
	}).Error)
	// The poisoned hull. It is perfectly alive; the API just cannot serialise it.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-5", PlayerID: liveEra.ID, FrameSymbol: "FRAME_HEAVY_FREIGHTER", CargoCapacity: 225,
	}).Error)

	apiClient := &partialReadFakeAPIClient{
		ships: []*navigation.ShipData{{
			Symbol: "TORWIND-4", Location: "X1-TEST-A1", NavStatus: "IN_ORBIT", FrameSymbol: "FRAME_PROBE",
		}},
		report: FleetReadReport{Unreadable: []UnreadableShip{{
			Page: 1, Index: 4, Symbol: "TORWIND-5", Reason: "nav: cannot unmarshal string into Go struct field",
		}}},
	}
	repo := newPartialReadRepo(t, apiClient, liveID, db)

	synced, err := repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err, "a partial fleet read must still sync the hulls it could read")
	require.Equal(t, 1, synced, "the readable hull is still persisted")

	var readable int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-4").Count(&readable).Error)
	require.Equal(t, int64(1), readable, "the readable hull must survive")

	var poisoned persistence.ShipModel
	require.NoError(t,
		db.Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-5").First(&poisoned).Error,
		"the UNREADABLE hull must not be pruned: absence from a partial read means unparseable, not sold")
	require.Equal(t, "FRAME_HEAVY_FREIGHTER", poisoned.FrameSymbol,
		"the unreadable hull's row must be left exactly as it was, not zeroed")
}

// Calibration for the guard above: suppression must be keyed on the read being
// PARTIAL, not on the reporting path being taken. Without this, a guard that
// always suppressed would pass the hazard test while silently disabling the
// dead-era ghost prune that reconcileFleetToLive exists for.
func TestSyncAllFromAPI_CompleteReadFromReportingClientStillPrunes(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	liveEra := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-live", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&liveEra).Error)
	liveID := shared.MustNewPlayerID(liveEra.ID)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-4", PlayerID: liveEra.ID, FrameSymbol: "FRAME_PROBE",
	}).Error)
	// Genuinely sold this era: absent from a COMPLETE read, so it must go.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-99", PlayerID: liveEra.ID, FrameSymbol: "FRAME_LIGHT_FREIGHTER", CargoCapacity: 80,
	}).Error)

	apiClient := &partialReadFakeAPIClient{
		ships: []*navigation.ShipData{{
			Symbol: "TORWIND-4", Location: "X1-TEST-A1", NavStatus: "IN_ORBIT", FrameSymbol: "FRAME_PROBE",
		}},
		report: FleetReadReport{}, // complete read: nothing unreadable
	}
	repo := newPartialReadRepo(t, apiClient, liveID, db)

	_, err = repo.SyncAllFromAPI(context.Background(), liveID)
	require.NoError(t, err)

	var sold int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-99").Count(&sold).Error)
	require.Equal(t, int64(0), sold, "a complete read is still authoritative and must prune a sold hull")

	var live int64
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("player_id = ? AND ship_symbol = ?", liveEra.ID, "TORWIND-4").Count(&live).Error)
	require.Equal(t, int64(1), live, "the live hull must survive the prune")
}
