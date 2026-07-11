// internal/adapters/grpc/ship_state_scheduler_guard_test.go
package grpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newTestShipWithArrival builds a minimal valid ship carrying a pending
// arrival time — the only shape ScheduleArrival needs to arm its AfterFunc.
// Built via NewShip + SetArrivalTime rather than the 28-param ReconstructShip
// (the panic under test fires at handleArrival's nil-shipRepo deref, before
// any nav-status check, so the exact status is immaterial).
func newTestShipWithArrival(t *testing.T, symbol string, playerID int, arrival time.Time) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint("X1-TR-EXPORT", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	ship.SetArrivalTime(arrival)
	return ship
}

// A panic inside a timer callback must not escape the timer goroutine (it
// would kill the daemon). We force the panic with a nil shipRepo: the
// callback's FindBySymbol nil-derefs. The assertion is simply that the test
// PROCESS survives past the timer firing; without the Guard this test
// crashes the whole `go test` run.
func TestArrivalTimerPanicDoesNotKillProcess(t *testing.T) {
	s := NewShipStateScheduler(nil /* shipRepo: nil-deref on fire */, &shared.RealClock{}, nil)

	arrival := time.Now().Add(1 * time.Millisecond)
	ship := newTestShipWithArrival(t, "TORWIND-9", 7, arrival)
	s.ScheduleArrival(ship)

	time.Sleep(150*time.Millisecond + ClockDriftBuffer)
	require.True(t, true, "process survived a panicking timer callback")
}
