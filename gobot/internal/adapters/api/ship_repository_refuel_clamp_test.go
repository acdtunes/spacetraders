package api

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// Three hulls were observed carrying OVER-CAPACITY fuel as reported by
// the live API - 729/600, 726/600, 613/600 - all three immediately after a
// refuel. ShipRepository.Refuel is the single funnel through which every refuel
// (route executor, CLI `ship refuel --units`, and a rebuilt refuel_ship
// container) reaches POST /my/ships/{symbol}/refuel, so it is where both halves
// of the fix belong: never ASK for more than the tank can take, and never let an
// impossible answer through unremarked.

// newRefuelClampRepo builds a ShipRepository over a real test DB with a
// recording API client, and returns both so a test can assert on the wire
// arguments AND on the row that was persisted.
func newRefuelClampRepo(t *testing.T, result *navigation.RefuelResult) (*ShipRepository, *refuelFakeAPIClient, shared.PlayerID, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWINDSTG", Token: "tok-a", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	api := &refuelFakeAPIClient{result: result}
	repo := NewShipRepository(
		api,
		&refuelFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok-a"}},
		nil, nil, db, nil,
	)
	return repo, api, playerID, db
}

// ACCEPTANCE #4: a refuel request never asks for more units than
// (capacity - current).
//
// The assertion is on the units the API CLIENT received, not on the units the
// caller passed - an implementation that computes a clamp and then forwards the
// original value would satisfy any check made on the caller's side.
func TestRefuel_NeverRequestsMoreUnitsThanTheTankCanTake(t *testing.T) {
	tests := []struct {
		name         string
		shipFuel     int
		shipCapacity int
		requested    *int
		wantAPICalls int
		wantSent     *int
	}{
		{
			name:         "a request past the tank's headroom is clamped down to it",
			shipFuel:     100,
			shipCapacity: 400,
			requested:    intPtr(500),
			wantAPICalls: 1,
			wantSent:     intPtr(300),
		},
		{
			name:         "a request for exactly the headroom passes through untouched",
			shipFuel:     100,
			shipCapacity: 400,
			requested:    intPtr(300),
			wantAPICalls: 1,
			wantSent:     intPtr(300),
		},
		{
			name:         "a request below the headroom is left alone - the clamp only ever narrows",
			shipFuel:     100,
			shipCapacity: 400,
			requested:    intPtr(50),
			wantAPICalls: 1,
			wantSent:     intPtr(50),
		},
		{
			// REGRESSION (acceptance #6). nil is the fleet's own path: the route
			// executor always sends nil, meaning "fill the tank", and the API
			// tops up to capacity and no further. Rewriting nil into a computed
			// number would hand the fleet's every refuel over to whatever the
			// local row happened to believe.
			name:         "fill-the-tank (nil units) is never rewritten into a number",
			shipFuel:     100,
			shipCapacity: 400,
			requested:    nil,
			wantAPICalls: 1,
			wantSent:     nil,
		},
		{
			// A full tank has zero headroom, so the clamped request is for zero
			// units. Spending an API call - and a purchase - on that is the
			// waste sp-l7zha is about, so the call is not made at all.
			name:         "a metered request against a full tank makes no API call at all",
			shipFuel:     600,
			shipCapacity: 600,
			requested:    intPtr(100),
			wantAPICalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, api, playerID, _ := newRefuelClampRepo(t, &navigation.RefuelResult{
				FuelCurrent: tc.shipCapacity, FuelCapacity: tc.shipCapacity, CreditsCost: 120,
			})
			ship := newRefuelTestShip(t, tc.shipFuel, tc.shipCapacity)

			result, err := repo.Refuel(context.Background(), ship, playerID, tc.requested)
			require.NoError(t, err)
			require.NotNil(t, result, "callers dereference the result for CreditsCost/AgentCredits; it must never be nil")

			require.Equal(t, tc.wantAPICalls, api.calls, "wrong number of refuel calls reached the API client")
			if tc.wantAPICalls == 0 {
				require.Equal(t, 0, result.FuelAdded, "a skipped refuel must report no fuel added")
				require.Equal(t, 0, result.CreditsCost, "a skipped refuel must report no spend")
				return
			}

			require.Len(t, api.gotUnits, 1)
			if tc.wantSent == nil {
				require.Nil(t, api.gotUnits[0], "fill-the-tank must reach the API as nil, not as a computed unit count")
				return
			}
			require.NotNil(t, api.gotUnits[0], "a metered request must not be turned into a fill-the-tank")
			require.Equal(t, *tc.wantSent, *api.gotUnits[0],
				"the units that reached the API are not the clamped value")
		})
	}
}

// ACCEPTANCE #5: an API response reporting current > capacity is clamped, logged,
// and never persisted as-is.
//
// 729/600 is the exact tank the live API reported for TORWINDSTG during the
// incident. The clamp itself already lived in shared.ReconstructFuel - which is
// why the local row read 600/600 while the API read 729/600 - but it happened in
// SILENCE, so an impossible tank left no trace anywhere an operator would look.
func TestRefuel_OverCapacityAPIResponseIsClampedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	restore := captureStdLog(t, &logs)
	defer restore()

	repo, _, playerID, db := newRefuelClampRepo(t, &navigation.RefuelResult{
		FuelCurrent: 729, FuelCapacity: 600, FuelAdded: 129, CreditsCost: 200,
	})
	ship := newRefuelTestShip(t, 100, 600)

	_, err := repo.Refuel(context.Background(), ship, playerID, nil)
	require.NoError(t, err, "an impossible tank must not fail the refuel - the hull is fine, the number is not")

	require.Equal(t, 600, ship.Fuel().Current,
		"an over-capacity tank must be clamped to capacity, never adopted verbatim")
	require.Equal(t, 600, ship.Fuel().Capacity)

	var persisted persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", ship.ShipSymbol()).First(&persisted).Error)
	require.Equal(t, 600, persisted.FuelCurrent,
		"the persisted row must never hold an over-capacity value")

	// The log must name the anomaly. Asserting on the raw numbers as well as the
	// action key rejects a generic "refuelled" line that happens to mention fuel:
	// this line must appear ONLY for the impossible case.
	require.Contains(t, logs.String(), "fuel_over_capacity",
		"the clamp must be loud - an impossible tank that leaves no log line is invisible to every operator")
	require.Contains(t, logs.String(), "729")
	require.Contains(t, logs.String(), "600")
}

// The other side of the same assertion: an ordinary refuel must NOT emit the
// anomaly line. Without this, an implementation that logged on every refuel
// would pass the test above while making the signal worthless.
func TestRefuel_NormalResponseEmitsNoOverCapacityWarning(t *testing.T) {
	var logs bytes.Buffer
	restore := captureStdLog(t, &logs)
	defer restore()

	repo, _, playerID, _ := newRefuelClampRepo(t, &navigation.RefuelResult{
		FuelCurrent: 400, FuelCapacity: 400, FuelAdded: 300, CreditsCost: 216,
	})
	ship := newRefuelTestShip(t, 100, 400)

	_, err := repo.Refuel(context.Background(), ship, playerID, nil)
	require.NoError(t, err)

	require.Equal(t, 400, ship.Fuel().Current)
	require.NotContains(t, logs.String(), "fuel_over_capacity",
		"a healthy refuel-to-full must not trip the over-capacity signal")
}

// A units request that is clamped must say so. A silent narrowing would hide the
// caller bug that produced the impossible request in the first place.
func TestRefuel_ClampedUnitsRequestIsLogged(t *testing.T) {
	var logs bytes.Buffer
	restore := captureStdLog(t, &logs)
	defer restore()

	repo, _, playerID, _ := newRefuelClampRepo(t, &navigation.RefuelResult{
		FuelCurrent: 400, FuelCapacity: 400, CreditsCost: 120,
	})
	ship := newRefuelTestShip(t, 100, 400)

	_, err := repo.Refuel(context.Background(), ship, playerID, intPtr(500))
	require.NoError(t, err)

	require.Contains(t, logs.String(), "refuel_units_clamped")
}

// captureStdLog redirects the standard logger into buf for the duration of a
// test. ShipRepository logs through the std logger (see the persist warnings in
// this package), so that is where its lines land. Tests using this must not run
// in parallel - the std logger is process-wide.
func captureStdLog(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	restore := func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
	t.Cleanup(restore)
	return restore
}
