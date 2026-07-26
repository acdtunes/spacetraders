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

// refuelFakeAPIClient answers RefuelShip with a caller-supplied result, standing in
// for the live POST /my/ships/{symbol}/refuel.
type refuelFakeAPIClient struct {
	domainPorts.APIClient
	result *navigation.RefuelResult
}

func (f *refuelFakeAPIClient) RefuelShip(_ context.Context, _, _ string, _ *int) (*navigation.RefuelResult, error) {
	return f.result, nil
}

type refuelFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (f *refuelFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return f.p, nil
}

func newRefuelTestShip(t *testing.T, current, capacity int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TORWIND-A", 0, 0)
	require.NoError(t, err)
	location.HasFuel = true
	fuel, err := shared.NewFuel(current, capacity)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		"TORWIND-1", shared.MustNewPlayerID(1), location, fuel, capacity, 40, cargo,
		9, "FRAME_FRIGATE", "COMMAND", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	return ship
}

// TestRefuel_AdoptsTheTankTheAPIActuallyReturned pins the tank a refuel leaves
// behind. The request asks to fill the tank (units nil), but the amount that
// actually lands is the API's to decide - market supply and its 100-unit blocks
// both bound it - and the response carries the resulting fuel state in-band.
//
// Believing the request instead of the response leaves the ship reading FULL while
// the server holds a fraction of that, and every downstream fuel decision (flight
// mode, refuel thresholds, leg budgeting) is then made against fuel that is not
// there. Navigate and warp already fold the API's fuel back through
// UpdateFuelFromAPI; refuel must do the same.
func TestRefuel_AdoptsTheTankTheAPIActuallyReturned(t *testing.T) {
	tests := []struct {
		name        string
		units       *int
		apiResult   *navigation.RefuelResult
		wantCurrent int
	}{
		{
			name:        "partial fill: only what the market could sell",
			units:       nil,
			apiResult:   &navigation.RefuelResult{FuelCurrent: 250, FuelCapacity: 400, CreditsCost: 120},
			wantCurrent: 250,
		},
		{
			name:        "full fill: the tank the response reports",
			units:       nil,
			apiResult:   &navigation.RefuelResult{FuelCurrent: 400, FuelCapacity: 400, CreditsCost: 216},
			wantCurrent: 400,
		},
		{
			name:        "metered request rounded up to the market's 100-unit block",
			units:       intPtr(150),
			apiResult:   &navigation.RefuelResult{FuelCurrent: 300, FuelCapacity: 400, CreditsCost: 144},
			wantCurrent: 300,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.NewTestConnection()
			require.NoError(t, err)

			playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok-a", CreatedAt: time.Now()}
			require.NoError(t, db.Create(&playerRow).Error)
			playerID := shared.MustNewPlayerID(playerRow.ID)

			repo := NewShipRepository(
				&refuelFakeAPIClient{result: tc.apiResult},
				&refuelFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}},
				nil, nil, db, nil,
			)

			ship := newRefuelTestShip(t, 100, 400)

			_, err = repo.Refuel(context.Background(), ship, playerID, tc.units)
			require.NoError(t, err)

			require.Equal(t, tc.wantCurrent, ship.Fuel().Current,
				"ship must hold the fuel the API reported after the refuel, not the fill that was requested")
			require.Equal(t, 400, ship.Fuel().Capacity)
		})
	}
}

func intPtr(v int) *int { return &v }
