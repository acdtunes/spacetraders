package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-40t45 reserve-survival regression ----------------------------------
//
// TORWIND-81 was DELIBERATELY reserved (`ship reserve --force`) and then silently yanked
// ~40 min later when a trade coordinator re-adopted the hull — violating the reserve
// contract ("no coordinator claims a reserved hull out from under you"). The sp-6asm
// stale-captain-reservation reaper did it: its "parked + aged + no live container"
// heuristic necessarily misfired on the legitimate reserve→park→hold-idle flow, because NO
// automated path ever creates an owner=captain reservation — the ONLY creator is the
// operator's own `ship reserve`. So every reservation the reaper could reap was a
// deliberate operator hold. The reaper is now removed; this test is the durable lock: a
// full reconcile pass over a parked, aged, unreferenced captain reservation must leave it
// untouched (still owner=captain), so no future reaper-like path can re-introduce the bug.

// reserveSurvivalShipRepo is a ShipRepository over a fixed roster that EXECUTES (and
// records) any ReleaseCaptainReservation the way the DB path does — flipping the matching
// in-memory hull off owner=captain. If any reconcile path releases the deliberate hold this
// test sees it: released is non-empty and the hull stops reading IsReservedByCaptain.
type reserveSurvivalShipRepo struct {
	navigation.ShipRepository
	ships    []*navigation.Ship
	clock    shared.Clock
	released []string
}

func (r *reserveSurvivalShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

func (r *reserveSurvivalShipRepo) ReleaseCaptainReservation(_ context.Context, shipSymbol string, reason string, _ shared.PlayerID) error {
	for _, s := range r.ships {
		if s.ShipSymbol() == shipSymbol {
			if err := s.ReleaseCaptainReservation(reason, r.clock); err != nil {
				return err
			}
			r.released = append(r.released, shipSymbol)
			return nil
		}
	}
	return errors.New("ship not found")
}

// A deliberate captain reservation — parked (IN_ORBIT), reserved 31 min ago (past the line
// the removed sp-6asm reaper used), with NO live/recent container naming it — must SURVIVE a
// full reconcile pass. This is exactly the TORWIND-81 shape the reaper reaped; with the reaper
// gone the reservation is left owner=captain, untouched, and this test is the permanent guard.
func TestTradeReconcile_DeliberateCaptainReservation_SurvivesReconcile(t *testing.T) {
	now := clockAt(1860) // 31 min after the clockAt(0) reservation — past the old 30-min reap line
	reserved := reservedTradeHull(t, "TORWIND-81")
	repo := &reserveSurvivalShipRepo{ships: []*navigation.Ship{reserved}, clock: now}
	launcher := &fakeTourLauncher{}
	logger := &tradeCaptureLogger{}

	h := NewRunTradeFleetCoordinatorHandler(repo, now)
	h.SetTourLauncher(launcher)

	launched, err := h.reconcileOnce(tradeCtx(logger), tradeCmd())

	require.NoError(t, err)
	require.Equal(t, 0, launched, "a deliberate captain reservation is never relaunched")
	require.Empty(t, repo.released, "no reconcile path may release a deliberate captain reservation (sp-40t45)")
	require.True(t, reserved.IsReservedByCaptain(),
		"the deliberate reservation MUST survive a full reconcile pass — the reaper that yanked TORWIND-81 is gone for good")
	require.Empty(t, launcher.launches, "no tour is launched on a captain-held hull")
}
