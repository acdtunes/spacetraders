package cargo

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the "ship jettison" behavior (bead sp-psfc) at the handler
// boundary the daemon's JettisonCargo container dispatches to. Jettison is the
// last-resort disposal path for stranded/bait cargo that no reachable market
// buys: it discards units directly via the SpaceTraders API instead of selling
// them, auto-transitioning a docked ship to orbit first since the jettison
// endpoint requires the ship to be in orbit.

const (
	testJettisonWaypoint = "X1-TEST-A1"
	testJettisonGood     = "GAS"
	testJettisonShip     = "JETTISONER-1"
)

// jettisonFakeShipRepo embeds the interface so unused methods panic, keeping
// the fake honest about what the jettison handler actually touches.
type jettisonFakeShipRepo struct {
	navigation.ShipRepository
	ship     *navigation.Ship
	saved    bool
	orbited  bool
	orbitErr error
}

func (r *jettisonFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *jettisonFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.saved = true
	return nil
}

func (r *jettisonFakeShipRepo) Orbit(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	r.orbited = true
	return r.orbitErr
}

type jettisonCall struct {
	shipSymbol string
	goodSymbol string
	units      int
	token      string
}

type jettisonFakeAPIClient struct {
	domainPorts.APIClient
	err   error
	calls []jettisonCall
}

func (c *jettisonFakeAPIClient) JettisonCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) error {
	c.calls = append(c.calls, jettisonCall{shipSymbol: shipSymbol, goodSymbol: goodSymbol, units: units, token: token})
	return c.err
}

func newJettisonShip(t *testing.T, cargoCapacity, heldUnits int, status navigation.NavStatus) *navigation.Ship {
	t.Helper()

	var inventory []*shared.CargoItem
	if heldUnits > 0 {
		item, err := shared.NewCargoItem(testJettisonGood, testJettisonGood, "", heldUnits)
		require.NoError(t, err)
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(cargoCapacity, heldUnits, inventory)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint(testJettisonWaypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		testJettisonShip,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		cargoCapacity,
		cargo,
		30,
		"FRAME_LIGHT_FREIGHTER",
		"HAULER",
		nil,
		status,
	)
	require.NoError(t, err)
	return ship
}

// newJettisonHandler wires the handler with a nil player repo: the jettison
// handler resolves its API token from the request context (auth.WithPlayerToken)
// rather than through playerRepo, so a real fake would never be exercised. If a
// future change starts reading playerRepo, this nil will panic loudly instead
// of silently passing with a stale fake.
func newJettisonHandler(ship *navigation.Ship, api *jettisonFakeAPIClient) (*JettisonCargoHandler, *jettisonFakeShipRepo) {
	shipRepo := &jettisonFakeShipRepo{ship: ship}
	var playerRepo player.PlayerRepository
	handler := NewJettisonCargoHandler(shipRepo, playerRepo, api)
	return handler, shipRepo
}

func jettisonCtx() context.Context {
	return auth.WithPlayerToken(context.Background(), "test-token")
}

func TestJettisonCargoHappyPathRemovesUnitsAndCallsAPI(t *testing.T) {
	ship := newJettisonShip(t, 40, 20, navigation.NavStatusInOrbit)
	api := &jettisonFakeAPIClient{}
	handler, shipRepo := newJettisonHandler(ship, api)

	resp, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: testJettisonGood,
		Units:      12,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)

	result, ok := resp.(*JettisonCargoResponse)
	require.True(t, ok)
	require.Equal(t, 12, result.UnitsJettisoned)

	require.Len(t, api.calls, 1)
	require.Equal(t, testJettisonShip, api.calls[0].shipSymbol)
	require.Equal(t, testJettisonGood, api.calls[0].goodSymbol)
	require.Equal(t, 12, api.calls[0].units)
	require.Equal(t, "test-token", api.calls[0].token)

	require.True(t, shipRepo.saved, "ship cargo change should be persisted")
	require.False(t, shipRepo.orbited, "already in orbit, no transition needed")
	require.Equal(t, 8, ship.Cargo().GetItemUnits(testJettisonGood), "cargo reduced by units jettisoned")
}

func TestJettisonCargoAutoTransitionsDockedShipToOrbitFirst(t *testing.T) {
	ship := newJettisonShip(t, 40, 20, navigation.NavStatusDocked)
	api := &jettisonFakeAPIClient{}
	handler, shipRepo := newJettisonHandler(ship, api)

	_, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: testJettisonGood,
		Units:      5,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)

	require.True(t, shipRepo.orbited, "docked ship should be moved to orbit before jettisoning")
	require.Equal(t, navigation.NavStatusInOrbit, ship.NavStatus())
	require.Len(t, api.calls, 1)
}

func TestJettisonCargoErrorsWhenUnitsExceedHeld(t *testing.T) {
	ship := newJettisonShip(t, 40, 5, navigation.NavStatusInOrbit)
	api := &jettisonFakeAPIClient{}
	handler, shipRepo := newJettisonHandler(ship, api)

	_, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: testJettisonGood,
		Units:      10,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient cargo")
	require.Empty(t, api.calls, "no jettison API call when units exceed held")
	require.False(t, shipRepo.saved, "no persistence when validation fails")
}

func TestJettisonCargoErrorsWhenGoodNotInCargo(t *testing.T) {
	ship := newJettisonShip(t, 40, 5, navigation.NavStatusInOrbit) // holds GAS only
	api := &jettisonFakeAPIClient{}
	handler, _ := newJettisonHandler(ship, api)

	_, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: "IRON_ORE", // not held at all
		Units:      1,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient cargo")
	require.Empty(t, api.calls, "no jettison API call for a good not in cargo")
}

func TestJettisonCargoPropagatesAPIError(t *testing.T) {
	ship := newJettisonShip(t, 40, 20, navigation.NavStatusInOrbit)
	api := &jettisonFakeAPIClient{err: errors.New("jettison rejected: ship in transit")}
	handler, shipRepo := newJettisonHandler(ship, api)

	_, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: testJettisonGood,
		Units:      5,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "jettison rejected")
	require.False(t, shipRepo.saved, "no persistence when the API call fails")
	require.Equal(t, 20, ship.Cargo().GetItemUnits(testJettisonGood), "cargo untouched when the API call fails")
}

func TestJettisonCargoErrorsWhenShipInTransit(t *testing.T) {
	ship := newJettisonShip(t, 40, 20, navigation.NavStatusInTransit)
	api := &jettisonFakeAPIClient{}
	handler, shipRepo := newJettisonHandler(ship, api)

	_, err := handler.Handle(jettisonCtx(), &JettisonCargoCommand{
		ShipSymbol: testJettisonShip,
		GoodSymbol: testJettisonGood,
		Units:      5,
		PlayerID:   shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot orbit while in transit")
	require.Empty(t, api.calls, "no jettison API call while the ship is in transit")
	require.False(t, shipRepo.saved)
}
