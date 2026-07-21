package expansion

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-255rz: ProbeBuyerPositioner (break the "probe unpriceable" stall) --------------------
//
// The positioner is exercised through its PORT (PositionProbeBuyer) with doubles at the SAME three
// collaborator boundaries the ProbePurchaser uses: the mediator (the buyer relay), the ship repo
// (idle undedicated buyers), and the yard finder (scanned probe-yards reachable from a hull's
// system). Observable outcomes only — WHICH hull is relayed WHERE, never the adapter's internals.

// shipInTransit builds an idle, undedicated satellite that is mid-flight — an ineligible positioning
// buyer (a prior relay is under way), used to prove the idempotency guard.
func shipInTransit(t *testing.T, symbol, waypoint string) *navigation.Ship {
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInTransit)
	require.NoError(t, err)
	return ship
}

// AC (the stall fix, RED-first): an idle undedicated hull sitting AWAY from any yard, plus a reachable
// scanned probe-yard, → the positioner RELAYS that hull to the yard so the next tick's presence-gated
// live price reads. It NEVER buys (no PurchaseShipCommand is ever sent — the mediator would reject
// one). The finder is queried with the probe ship type and the HULL's own system (reachability is
// anchored where the hull IS, not at the deep target the ProbePurchaser's proximal path failed on).
func TestPositionProbeBuyer_RelaysEligibleHullToNearestReachableProbeYard(t *testing.T) {
	med := &probeFakeMediator{} // no listings/purchases scripted: the positioner must only navigate
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-A2")}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-HOME-YD", "X1-HOME", 0, 20_000),
	}}
	p := NewProbeBuyerPositioner(med, ships, finder)

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.NoError(t, err)
	require.True(t, dispatched, "an eligible hull is relayed to a reachable probe-yard to break the stall")
	require.Len(t, med.navigations, 1, "exactly one buyer relay is issued")
	require.Equal(t, "X1-HOME-YD", med.navigations[0].Destination, "the hull is sent to the reachable probe-yard")
	require.Equal(t, "IDLE-1", med.navigations[0].ShipSymbol, "the idle undedicated hull is the positioned buyer")
	require.Empty(t, med.purchases, "the positioner NEVER buys — it only makes the price readable (RULINGS #4)")
	require.Equal(t, []string{probeShipType}, finder.lastShipTypes, "the finder is queried for probe-selling yards")
	require.Equal(t, []string{"X1-HOME"}, finder.lastFromSystems, "reachability is anchored to the hull's own system")
}

// RULINGS #7 (never poach): a dedicated hull that is momentarily idle is NEVER claimed to position,
// even with a reachable yard — only an UNDEDICATED hull is eligible. Mutation guard: drop the
// DedicatedFleet skip and the hauler is relayed → the assertion fails.
func TestPositionProbeBuyer_NeverPoachesADedicatedHull(t *testing.T) {
	hauler := probeShip(t, "HAULER-1", "X1-HOME-A2")
	hauler.SetDedicatedFleet("contract") // a contract hauler, momentarily idle between deliveries
	med := &probeFakeMediator{}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{hauler}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-HOME-YD", "X1-HOME", 0, 20_000),
	}}
	p := NewProbeBuyerPositioner(med, ships, finder)

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.NoError(t, err)
	require.False(t, dispatched, "a dedicated hull is never poached to position, even with a reachable yard")
	require.Empty(t, med.navigations, "no ship is navigated: the only idle hull is dedicated (RULINGS #7)")
}

// Selectivity (never-poach, positive side): with BOTH a dedicated and an undedicated idle hull, only
// the UNDEDICATED one is relayed — the dedicated hull is left untouched.
func TestPositionProbeBuyer_PositionsUndedicatedHull_LeavingDedicatedUntouched(t *testing.T) {
	hauler := probeShip(t, "HAULER-1", "X1-HOME-A1")
	hauler.SetDedicatedFleet("contract")
	free := probeShip(t, "FREE-1", "X1-HOME-A2")
	med := &probeFakeMediator{}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{hauler, free}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-HOME-YD", "X1-HOME", 0, 20_000),
	}}
	p := NewProbeBuyerPositioner(med, ships, finder)

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.NoError(t, err)
	require.True(t, dispatched)
	require.Len(t, med.navigations, 1)
	require.Equal(t, "FREE-1", med.navigations[0].ShipSymbol, "only the undedicated hull is relayed; the hauler is left alone")
}

// FAIL-SAFE: every degraded input no-ops gracefully (dispatched=false, no navigation, no error) —
// never a crash, never a stranded hull. Input variations of one behavior (parametrized).
func TestPositionProbeBuyer_FailSafeNoOp(t *testing.T) {
	cases := []struct {
		name       string
		idle       []*navigation.Ship
		shipErr    error
		candidates []shipyardQueries.YardCandidate
		finderErr  error
		nilFinder  bool
	}{
		{name: "no reachable probe-yard (empty candidates)", idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-A2")}, candidates: []shipyardQueries.YardCandidate{}},
		{name: "yard finder unreadable (error)", idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-A2")}, finderErr: errors.New("scan store unreadable")},
		{name: "no eligible idle hull (none idle)", idle: nil, candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}},
		{name: "fleet read error", shipErr: errors.New("db down"), candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}},
		{name: "no yard finder wired (nil)", idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-A2")}, nilFinder: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{}
			ships := &probeFakeShipRepo{idle: tc.idle, err: tc.shipErr}
			var p *ProbeBuyerPositioner
			if tc.nilFinder {
				p = NewProbeBuyerPositioner(med, ships, nil)
			} else {
				p = NewProbeBuyerPositioner(med, ships, &probeFakeYardFinder{candidates: tc.candidates, err: tc.finderErr})
			}

			dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

			require.NoError(t, err, "a degraded input degrades gracefully — never an error")
			require.False(t, dispatched, "no hull is positioned when the fix cannot safely act")
			require.Empty(t, med.navigations, "no ship is navigated on a fail-safe no-op")
		})
	}
}

// IDEMPOTENT (RULINGS #2, restart-safe): re-running while a hull is already at the yard, or already
// relaying, issues NO second nav — so calling it every stalled tick never thrashes or over-positions.
func TestPositionProbeBuyer_Idempotent_NoRedundantRelay(t *testing.T) {
	cases := []struct {
		name string
		idle []*navigation.Ship
	}{
		{name: "a hull already AT the yard (price reads next tick)", idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-YD")}},
		{name: "an undedicated hull already IN TRANSIT (a prior relay is under way)", idle: []*navigation.Ship{shipInTransit(t, "IDLE-1", "X1-HOME-A2")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{}
			ships := &probeFakeShipRepo{idle: tc.idle}
			finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}}
			p := NewProbeBuyerPositioner(med, ships, finder)

			dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

			require.NoError(t, err)
			require.False(t, dispatched, "no relay is issued")
			require.Empty(t, med.navigations, "no redundant navigation — idempotent")
		})
	}
}

// A relay that cannot route surfaces the error (dispatched=false) so the coordinator logs it and
// stays fail-closed — never a silent swallow, never a stranded hull mid-nav.
func TestPositionProbeBuyer_UnroutableRelay_SurfacesErrorFailClosed(t *testing.T) {
	med := &probeFakeMediator{navErr: errors.New("no jump-gate route within reach")}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-1", "X1-HOME-A2")}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{yard("X1-HOME-YD", "X1-HOME", 0, 20_000)}}
	p := NewProbeBuyerPositioner(med, ships, finder)

	dispatched, err := p.PositionProbeBuyer(context.Background(), shared.MustNewPlayerID(1))

	require.Error(t, err, "an unroutable relay surfaces the error (the coordinator logs and stays stalled)")
	require.False(t, dispatched)
	require.Empty(t, med.purchases, "still no buy")
}
