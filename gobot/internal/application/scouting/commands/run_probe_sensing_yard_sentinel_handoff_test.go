package commands

// THE MANDATORY HANDOFF TEST, part 2: the parked-sensing side.
//
// Bootstrap's own half of the yard-sentinel hand-off — buy, protect via a captain reservation, release
// at EXPANSION — is pinned in internal/application/bootstrap/commands
// (TestBootstrap_YardSentinel_ColdStartThroughExpansion_BootstrapReleasesForAdoption), which ends with
// bootstrap calling Release on the sentinel's captain reservation. This file starts from EXACTLY the
// state that release leaves behind — a scout-type hull, idle, carrying NO dedicated-fleet tag — and
// proves the OTHER, load-bearing half: the ALREADY boot-standing probe-sensing coordinator (it runs
// continuously from daemon boot, independent of bootstrap's own hand-off) adopts it into productive
// parked-sensing duty on its own very next tick, through adoptStrandedProbes's real, unexported
// eligibility path (adoptableFleetTag("")==true, the first case of its allowlist).
//
// Together the two tests are the real, end-to-end proof the acceptance bar demands: not merely that
// the sentinel avoids scouting duty (bootstrap_home_tour_test.go's
// TestSelectHomeTourHulls_NeverTakesTheYardSentinel, in the grpc package, covers that separately), but
// that the released hull is ACTUALLY, DEMONSTRABLY adopted rather than left a silent permanent orphan —
// exactly the failure mode that would not be visible from a quick live check.
//
// Split across two packages because adoptStrandedProbes is unexported here and
// internal/application/bootstrap/commands must not import the internal/adapters/grpc layer that could
// reach it (grpc already imports scouting/commands, so the reverse would cycle); bootstrap/commands and
// scouting/commands are themselves siblings with no import edge either way, so this file imports
// bootstrapCmd ONLY for the one exported constant (YardSentinelReservationReason) that ties the two
// halves to the SAME identifying string.

import (
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// releasedYardSentinel builds a scout-type hull in EXACTLY the shape bootstrap's own EXPANSION release
// leaves behind: captain-reserved with the sentinel's reason, then released — idle, DedicatedFleet=="".
func releasedYardSentinel(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	ship := scoutProbe(t, symbol, waypoint)
	ship.SetDedicatedFleet("") // bootstrap's buy never tags a dedicated fleet (bootstrap_ports_acquire.go)
	clock := shared.NewRealClock()
	require.NoError(t, ship.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, clock),
		"models bootstrap's own BuyAndReserve")
	require.True(t, ship.IsAssigned(), "captain-reserved must read as non-idle for the whole of COLDSTART/GATE")
	require.NoError(t, ship.ReleaseCaptainReservation("expansion_handoff", clock),
		"models bootstrap's own actExpansion release (run_bootstrap_gate.go)")
	require.True(t, ship.IsIdle(), "the EXPANSION release returns it to plain idle")
	require.Empty(t, ship.DedicatedFleet(), "and it was never dedicated-fleet tagged")
	return ship
}

// THE HANDOFF ITSELF. A released sentinel, standing exactly where it was parked, is adopted as a
// productive parked-sensing spare — recorded in the ledger and tagged into the sensing fleet — on the
// very next probe-sensing tick. This is the single most important assertion in the whole feature: if it
// ever failed, the sentinel would sit doing nothing forever with nothing ever noticing.
func TestYardSentinelHandoff_ReleasedSentinelIsAdoptedIntoParkedSensing(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil // nothing manned — the released sentinel is a plain, unclaimed orphan

	sentinel := releasedYardSentinel(t, "SENTINEL-1", "X1-HOME-YARD")
	world.fleet.ships = []*navigation.Ship{sentinel}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	slot, recorded := slotFor(world, "SENTINEL-1")
	require.True(t, recorded,
		"the released sentinel must be adopted — a silent permanent orphan is exactly the failure mode this feature must avoid")
	require.Equal(t, "X1-HOME-YARD", slot.Waypoint)
	require.Equal(t, parkedsensing.SlotStateParked, slot.State)
	require.Equal(t, parkedsensing.SensingParkedFleetTag, world.tagger.tagged["SENTINEL-1"],
		"and it is tagged into productive parked-sensing duty, not left a dead asset")
}

// THE NEGATIVE COUNTERPART, proving the release is the load-bearing action and not incidental: a
// sentinel STILL under its captain reservation (bootstrap has not reached EXPANSION yet, or its release
// failed) is NEVER adopted — adoptStrandedProbes's own IsIdle() guard holds it back, exactly as it does
// for any hull a live claim is driving. If this ever adopted a still-reserved hull, it would be racing
// COLDSTART/GATE for a hull they still believe is parked at the yard.
func TestYardSentinelHandoff_StillReservedSentinelIsNeverAdopted(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil

	sentinel := scoutProbe(t, "SENTINEL-1", "X1-HOME-YARD")
	sentinel.SetDedicatedFleet("")
	require.NoError(t, sentinel.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))
	world.fleet.ships = []*navigation.Ship{sentinel}

	for i := 0; i < 3; i++ {
		require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	}

	_, recorded := slotFor(world, "SENTINEL-1")
	require.False(t, recorded,
		"a still-reserved sentinel must never be adopted while COLDSTART/GATE is actively parking it there")
	require.NotContains(t, world.tagger.tagged, "SENTINEL-1")
}
