package api

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// ─────────────────────────────────────────────────────────────────────────────────────────────
// FUEL RE-SYNC — in-process (sqlite + fake API) teeth for the daemon read-back the live-stack
// ship-actions "refuel" scenario asserts. `ship refresh` runs SyncShipFromAPI: GetShip (from the
// twin) → shipDataToModel → DB upsert → modelToDomain → the reconciled Fuel the CLI prints as
// `Fuel: current/capacity`. The scenario's teeth are (a) the drained tank surfacing on refresh
// (fuelBefore < capacity) and (b) the post-refuel FULL tank surfacing (current == capacity).
//
// A re-sync that KEPT the stale DB fuel — never reconciling to the twin's authoritative snapshot —
// is exactly the "daemon drops fuel on re-sync" no-op that reads as a live RED. These cases seed a
// STALE fuel row and prove the refresh overwrites it with the API value (and clamps a transient
// current>capacity over-report, per sp-xxhn), through the real SyncShipFromAPI persistence path.
// ─────────────────────────────────────────────────────────────────────────────────────────────

// fuelResyncFakeAPIClient serves one hardcoded ShipData from GetShip so
// SyncShipFromAPI has a snapshot to reconcile without a live API/twin.
type fuelResyncFakeAPIClient struct {
	domainPorts.APIClient
	shipData *navigation.ShipData
}

func (f *fuelResyncFakeAPIClient) GetShip(_ context.Context, _ string, _ string) (*navigation.ShipData, error) {
	return f.shipData, nil
}

// fuelResyncFakePlayerRepo supplies just enough of player.PlayerRepository for
// SyncShipFromAPI to resolve an API token.
type fuelResyncFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *fuelResyncFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

// fuelResyncFakeWaypointProvider always errors so modelToDomain falls back to the
// denormalized location — fuel reconciliation is orthogonal to waypoint lookup.
type fuelResyncFakeWaypointProvider struct{}

func (fuelResyncFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: waypoint lookup not needed by this test")
}

// TestSyncShipFromAPI_ReconcilesFuelOffStaleRow is the read-back locus for the
// live refuel scenario: a `ship refresh` must reconcile the daemon's cached fuel
// to the twin's current value, whether the tank was DRAINED by a voyage or
// REFILLED at a market — never keep the stale row, never store current>capacity.
func TestSyncShipFromAPI_ReconcilesFuelOffStaleRow(t *testing.T) {
	const symbol = "TWINAGENT-1"

	cases := []struct {
		name         string
		staleRow     *persistence.ShipModel // pre-existing daemon cache (nil = first sync, no row)
		apiCurrent   int                    // what the twin now reports
		apiCapacity  int
		wantCurrent  int // the reconciled tank the CLI would print / the pilot observes
		wantCapacity int
	}{
		{
			// post-refuel: the twin filled the tank; the stale row still shows the drained level.
			name:         "refuel fills — full tank surfaces over a stale drained row",
			staleRow:     &persistence.ShipModel{FuelCurrent: 100, FuelCapacity: 400},
			apiCurrent:   400,
			apiCapacity:  400,
			wantCurrent:  400,
			wantCapacity: 400,
		},
		{
			// post-voyage: the twin burned fuel; the stale row still shows a full tank. This is the
			// refuel pre-check's teeth — fuelBefore must read BELOW capacity after the refresh.
			name:         "drain surfaces — burned tank surfaces below a stale full row",
			staleRow:     &persistence.ShipModel{FuelCurrent: 400, FuelCapacity: 400},
			apiCurrent:   100,
			apiCapacity:  400,
			wantCurrent:  100,
			wantCapacity: 400,
		},
		{
			// a transient API over-report must never persist an invariant-violating current>capacity.
			name:         "clamps a current>capacity over-report to capacity",
			staleRow:     nil,
			apiCurrent:   500,
			apiCapacity:  400,
			wantCurrent:  400,
			wantCapacity: 400,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.NewTestConnection()
			require.NoError(t, err)

			playerRow := persistence.PlayerModel{AgentSymbol: "TWINAGENT", Token: "tok-a"}
			require.NoError(t, db.Create(&playerRow).Error)
			playerID := shared.MustNewPlayerID(playerRow.ID)

			if tc.staleRow != nil {
				tc.staleRow.ShipSymbol = symbol
				tc.staleRow.PlayerID = playerRow.ID
				tc.staleRow.AssignmentStatus = "idle"
				require.NoError(t, db.Create(tc.staleRow).Error)
			}

			apiClient := &fuelResyncFakeAPIClient{shipData: &navigation.ShipData{
				Symbol:        symbol,
				Location:      "X1-PZ28-F55",
				NavStatus:     "DOCKED",
				FlightMode:    "CRUISE",
				FuelCurrent:   tc.apiCurrent,
				FuelCapacity:  tc.apiCapacity,
				CargoCapacity: 40,
				EngineSpeed:   30, // ReconstructShip requires engine_speed > 0
				FrameSymbol:   "FRAME_FRIGATE",
				Role:          "COMMAND",
			}}
			playerRepo := &fuelResyncFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}}

			repo := NewShipRepository(apiClient, playerRepo, nil, fuelResyncFakeWaypointProvider{}, db, nil)

			ship, err := repo.SyncShipFromAPI(context.Background(), symbol, playerID)
			require.NoError(t, err)

			// The reconciled domain fuel the CLI read-back reflects.
			require.Equal(t, tc.wantCurrent, ship.Fuel().Current, "reconciled fuel.current the pilot observes after ship refresh")
			require.Equal(t, tc.wantCapacity, ship.Fuel().Capacity, "reconciled fuel.capacity")

			// And it is what actually persisted — the stale row was overwritten, not kept.
			var stored persistence.ShipModel
			require.NoError(t, db.Where("ship_symbol = ? AND player_id = ?", symbol, playerRow.ID).First(&stored).Error)
			require.Equal(t, tc.wantCurrent, stored.FuelCurrent, "persisted fuel_current after re-sync")
			require.Equal(t, tc.wantCapacity, stored.FuelCapacity, "persisted fuel_capacity after re-sync")
		})
	}
}
