package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-6asm reaper fakes --------------------------------------------------

// reapShipRepo is a ShipRepository over a fixed roster that records every
// ReleaseCaptainReservation call and, like the DB path, flips the matching in-memory hull to
// idle — so a test can prove a reaped hull rejoins partitionTradeFleet's idle bucket.
type reapShipRepo struct {
	navigation.ShipRepository
	ships    []*navigation.Ship
	clock    shared.Clock
	released []string
}

func (r *reapShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *reapShipRepo) ReleaseCaptainReservation(_ context.Context, shipSymbol string, reason string, _ shared.PlayerID) error {
	for _, s := range r.ships {
		if s.ShipSymbol() == shipSymbol {
			if err := s.ReleaseCaptainReservation(reason, r.clock); err != nil {
				return err // mirror the prod path: ShipNotReserved on a concurrent change
			}
			r.released = append(r.released, shipSymbol)
			return nil
		}
	}
	return errors.New("ship not found")
}

// fakeActiveShips is the sp-6asm safety-signal port. live is the set of hulls a live/recent
// container has touched (never reaped); err drives the fail-closed path.
type fakeActiveShips struct {
	live       map[string]bool
	err        error
	callCount  int
	lastActive time.Time
}

func (f *fakeActiveShips) ActiveContainerShips(_ context.Context, _ shared.PlayerID, activeSince time.Time) (map[string]bool, error) {
	f.callCount++
	f.lastActive = activeSince
	if f.err != nil {
		return nil, f.err
	}
	return f.live, nil
}

// ---- sp-6asm reaper ship builders ------------------------------------------

// captainReservedHull is a trade-dedicated hull the captain reserved at clockAt(reserveSecs),
// left parked (nav), released_at NULL — the orphan shape a manual bridge-authority op leaves
// when it never drops the reservation.
func captainReservedHull(t *testing.T, symbol string, reserveSecs int, nav navigation.NavStatus) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-TR-A1", 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, nav)
	require.NoError(t, err)
	ship.SetDedicatedFleet(tradeFleet)
	require.NoError(t, ship.ReserveByCaptain("captain manual use", clockAt(reserveSecs)))
	return ship
}

// reapCmd is the coordinator command with the reaper ARMED at its default 30-min threshold.
func reapCmd() *RunTradeFleetCoordinatorCommand {
	return &RunTradeFleetCoordinatorCommand{
		PlayerID:                            shared.MustNewPlayerID(1),
		ContainerID:                         "trade-coord-1",
		AgentSymbol:                         "TORWIND",
		Enabled:                             true,
		ReapStaleCaptainReservationsEnabled: true,
	}
}

// idleClockSecs is a clock far enough past baseTime that a hull reserved at clockAt(0) has
// been idle well beyond the 30-min (1800s) default threshold.
func idleClockSecs() int { return defaultReapIdleThresholdSeconds + 600 } // 40 min

// ---- sp-6asm reaper tests --------------------------------------------------

// (1) An orphaned captain reservation — owner=captain, active, released_at NULL, parked,
// reserved past the threshold, referenced by NO live/recent container — is RELEASED through
// the daemon's normal path and rejoins partitionTradeFleet's idle bucket for relaunch.
func TestTradeReap_OrphanedCaptainReservation_ReleasedAndRejoinsIdle(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}} // nothing live/recent references it
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 1, reaped)
	require.Equal(t, []string{"TORWIND-7D"}, repo.released, "the orphan must be released through ReleaseCaptainReservation")
	require.True(t, logger.loggedContaining("Reaped stale captain reservation", "TORWIND-7D", "rejoins the idle bucket"))

	// It genuinely rejoins the relaunch pool: partitionTradeFleet now buckets it idle.
	idle, running := partitionTradeFleet(repo.ships)
	require.Equal(t, 0, running)
	require.Len(t, idle, 1)
	require.Equal(t, "TORWIND-7D", idle[0].ShipSymbol())
}

// (2) A LEGITIMATELY-held hull — a live/recent container references it (in the safety set) —
// is NEVER reaped, even though its ships-row reservation looks identical to the orphan.
func TestTradeReap_LiveContainerReferencesHull_NotReaped(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{"TORWIND-7D": true}} // a live captain op is using it
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released, "a hull a live container is using must never be reaped")
	require.True(t, repo.ships[0].IsReservedByCaptain(), "the reservation must be left intact")
}

// (3) A recently-reserved hull (reserved less than the idle threshold ago) is NOT reaped —
// the captain may be mid-setup; the reaper only takes a reservation that has sat idle.
func TestTradeReap_RecentlyReserved_NotReaped(t *testing.T) {
	now := clockAt(60) // reserved at clockAt(0), only 60s ago << 1800s threshold
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.True(t, repo.ships[0].IsReservedByCaptain())
}

// Default-OFF governance gate: an unarmed reaper is fully inert — no release, and it never
// even consults the safety port (byte-identical to pre-sp-6asm).
func TestTradeReap_Disabled_NoOp(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	cmd := reapCmd()
	cmd.ReapStaleCaptainReservationsEnabled = false

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), cmd, repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.Equal(t, 0, port.callCount, "a disabled reaper must not read the safety port")
}

// Armed but the safety port was never wired: fail CLOSED — reap nothing (we cannot confirm a
// hull idle) and log the wiring gap, never release blindly.
func TestTradeReap_ArmedButPortUnwired_FailsClosed(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now) // no SetActiveContainerShips

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.True(t, logger.loggedContaining("no active-container-ships port wired"))
}

// The safety read failing this tick is also fail-closed: reap nothing, log, retry next tick.
func TestTradeReap_PortError_FailsClosed(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{err: errors.New("db unavailable")}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.True(t, logger.loggedContaining("could not read active container ships"))
}

// A captain-reserved hull mid-flight (IN_TRANSIT) is being actively repositioned — never
// reaped, regardless of how long ago it was reserved.
func TestTradeReap_InTransitReservation_NotReaped(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInTransit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.True(t, repo.ships[0].IsReservedByCaptain())
}

// A captain reservation on a hull dedicated to another fleet is not this coordinator's to
// reap — the reaper is scoped to the 'trade' fleet.
func TestTradeReap_NonTradeFleetReservation_NotReaped(t *testing.T) {
	now := clockAt(idleClockSecs())
	ship := captainReservedHull(t, "CONTRACT-1", 0, navigation.NavStatusInOrbit)
	ship.SetDedicatedFleet("contract") // not the trade fleet
	repo := &reapShipRepo{ships: []*navigation.Ship{ship}, clock: now}
	port := &fakeActiveShips{live: map[string]bool{}}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	reaped := h.reapStaleCaptainReservations(tradeCtx(logger), reapCmd(), repo.ships, now.Now(), logger)

	require.Equal(t, 0, reaped)
	require.Empty(t, repo.released)
	require.True(t, ship.IsReservedByCaptain())
}

// The armed reaper passes the correct trailing edge (now - threshold) to the safety port, so
// "recent" is measured over exactly the idle window.
func TestTradeReap_SafetyWindowIsThreshold(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetActiveContainerShips(port)

	cmd := reapCmd()
	cmd.ReapIdleThresholdSecs = 900 // 15 min
	_ = h.reapStaleCaptainReservations(tradeCtx(logger), cmd, repo.ships, now.Now(), logger)

	require.Equal(t, 1, port.callCount)
	require.Equal(t, now.Now().Add(-15*time.Minute), port.lastActive)
}

// End-to-end through reconcileOnce (launcher + port wired, reaper armed): an armed reconcile
// pass releases the orphan even when the idle bucket is otherwise empty.
func TestTradeReconcile_Armed_ReapsOrphanedReservation(t *testing.T) {
	now := clockAt(idleClockSecs())
	repo := &reapShipRepo{
		ships: []*navigation.Ship{captainReservedHull(t, "TORWIND-7D", 0, navigation.NavStatusInOrbit)},
		clock: now,
	}
	port := &fakeActiveShips{live: map[string]bool{}}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}
	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)
	h.SetActiveContainerShips(port)

	launched, err := h.reconcileOnce(tradeCtx(logger), reapCmd())
	require.NoError(t, err)
	require.Equal(t, 0, launched, "the reaped hull relaunches on a later tick, not this one")
	require.Equal(t, []string{"TORWIND-7D"}, repo.released)
}

// ---- predicate table -------------------------------------------------------

func TestIsStaleCaptainReservation(t *testing.T) {
	threshold := time.Duration(defaultReapIdleThresholdSeconds) * time.Second
	now := clockAt(idleClockSecs()).Now()

	t.Run("orphaned parked reservation past threshold -> stale", func(t *testing.T) {
		ship := captainReservedHull(t, "H", 0, navigation.NavStatusInOrbit)
		require.True(t, isStaleCaptainReservation(ship, now, threshold))
	})
	t.Run("docked is also parked -> stale", func(t *testing.T) {
		ship := captainReservedHull(t, "H", 0, navigation.NavStatusDocked)
		require.True(t, isStaleCaptainReservation(ship, now, threshold))
	})
	t.Run("reserved within threshold -> not stale", func(t *testing.T) {
		ship := captainReservedHull(t, "H", 0, navigation.NavStatusInOrbit)
		require.False(t, isStaleCaptainReservation(ship, clockAt(60).Now(), threshold))
	})
	t.Run("in transit -> not stale", func(t *testing.T) {
		ship := captainReservedHull(t, "H", 0, navigation.NavStatusInTransit)
		require.False(t, isStaleCaptainReservation(ship, now, threshold))
	})
	t.Run("container-owned (not a captain reservation) -> not stale", func(t *testing.T) {
		ship := runningTradeHull(t, "H") // owner=container claim
		require.False(t, isStaleCaptainReservation(ship, now, threshold))
	})
	t.Run("idle hull (no reservation) -> not stale", func(t *testing.T) {
		ship := parkedTradeHull(t, "H", 0, "margins_died_both_systems")
		require.False(t, isStaleCaptainReservation(ship, now, threshold))
	})
}
