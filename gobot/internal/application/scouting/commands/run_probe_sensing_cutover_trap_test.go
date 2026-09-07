package commands

// THE TRAP THESE CLOSE. The cutover's own adoption gated on a SINGLE fleet tag
// while the standing retry beside it gates on an allowlist that includes OUR tag.
// The disagreement was load-bearing rather than cosmetic: reclaimIdleProbes
// returns before the standing pass runs for as long as the cutover is pending, so
// on exactly the ticks where this pass is the only adoption that runs, a hull
// carrying the sensing tag with no ledger row had nothing that could record it. It
// is driven by nobody and counted by nothing, and the probe cap then authorises
// buying its replacement (RULINGS #4).
//
// These pin the widened eligibility AND the hulls it must still refuse — a hull
// holding a live placement is doing its job and is never reclaimed.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// parkedProbe is a landed hull carrying OUR sensing tag — the trap shape, once it
// holds no ledger row.
func parkedProbe(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	return probeWithFleet(t, symbol, waypoint, parkedsensing.SensingParkedFleetTag)
}

// The core reclaim. The `adopted_stranded` assertion is not decoration: it proves
// the standing retry did NOT run on this tick, which is what makes the cutover's
// own filter the only thing standing between the hull and the ledger.
func TestCutover_AdoptsAParkedTaggedHullThatHoldsNoSlot(t *testing.T) {
	world := newCutoverWorld(t)
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{parkedProbe(t, "PROBE-JOBLESS", "X1-FAR1-A1")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"the standing retry is suppressed while the cutover is pending")
	require.Equal(t, 1, logger.payload("parked_sensing_cutover")["probes_adopted"],
		"so this pass has to absorb the hull, tag and all")

	row := world.ledger.slots[psSlotKey{"X1-FAR1-A1", parkedsensing.SlotKindSpare}]
	require.Equal(t, "PROBE-JOBLESS", row.AssignedShip, "the ledger accounts for the hull now")
	require.Equal(t, parkedsensing.SlotStateParked, row.State,
		"a hull already standing there is PARKED, and claimable as a spare")
}

// A hull that HOLDS a placement is doing its job and is never taken. This is the
// half of the widening that must not move: the tag stops being the gate, the
// LEDGER does not.
func TestCutover_LeavesAParkedHullThatAlreadyHoldsASlot(t *testing.T) {
	for _, kind := range []string{parkedsensing.SlotKindMarket, parkedsensing.SlotKindYard} {
		t.Run(kind, func(t *testing.T) {
			world := newCutoverWorld(t)
			world.posts.posts = nil
			working := parkedsensing.QueuedSlot{
				Waypoint: "X1-FAR1-A1", System: "X1-FAR1", Kind: kind,
				State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-WORKING",
			}
			world.ledger.slots[psSlotKey{"X1-FAR1-A1", kind}] = working
			world.fleet.ships = []*navigation.Ship{parkedProbe(t, "PROBE-WORKING", "X1-FAR1-A1")}
			logger := &capturingLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

			require.Equal(t, 0, logger.payload("parked_sensing_cutover")["probes_adopted"],
				"a hull the ledger already names is not an orphan")
			require.Equal(t, working, world.ledger.slots[psSlotKey{"X1-FAR1-A1", kind}],
				"its placement is untouched — kind, state and hull")
			require.Empty(t, world.ledger.spareHulls,
				"and it is not duplicated into the reserve pool either")
		})
	}
}

// A hull with no readable position is a hull we must not record: recording it
// would name a waypoint the ships table never reported.
func TestCutover_LeavesAParkedHullItCannotLocate(t *testing.T) {
	world := newCutoverWorld(t)
	world.posts.posts = nil
	flying := flyingScoutProbe(t, "PROBE-FLYING")
	flying.SetDedicatedFleet(parkedsensing.SensingParkedFleetTag)
	world.fleet.ships = []*navigation.Ship{flying}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, logger.payload("parked_sensing_cutover")["probes_adopted"])
	require.Empty(t, adoptedHulls(world), "an in-transit hull is recorded nowhere")
}

// The guard the widening is paired with (RULINGS #3). The tags now admitted are
// worn by hulls a live container may be driving, and recording one here would let
// the placement machine re-task it mid-errand.
func TestCutover_LeavesAParkedHullALiveContainerIsDriving(t *testing.T) {
	world := newCutoverWorld(t)
	world.posts.posts = nil
	driven := parkedProbe(t, "PROBE-DRIVEN", "X1-FAR1-A1")
	require.NoError(t, driven.AssignToContainer("scout_tour-abc", shared.NewRealClock()))
	world.fleet.ships = []*navigation.Ship{driven}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, logger.payload("parked_sensing_cutover")["probes_adopted"],
		"one hull, one writer — the container's, not ours")
	require.Empty(t, adoptedHulls(world))
}

// THE BURST IS BOUNDED. A bulk buy stacks its hulls at the yard that sold them, so
// the widening hands this pass a whole waypoint at once — and the co-located arm
// writes hull-keyed reserve rows, which nothing else in the loop paces. The
// overflow is not lost: those hulls are still orphaned and still first in line.
func TestCutover_ReserveBurstStopsAtItsCap(t *testing.T) {
	world := newCutoverWorld(t)
	world.posts.posts = nil
	world.ledger.slots[psSlotKey{"X1-FAR1-A1", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-FAR1-A1", System: "X1-FAR1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-SCANNING",
	}
	ships := []*navigation.Ship{scoutProbe(t, "PROBE-SCANNING", "X1-FAR1-A1")}
	for i := 0; i < DefaultMaxAdoptions+2; i++ {
		ships = append(ships, parkedProbe(t, fmt.Sprintf("PROBE-STACKED-%02d", i), "X1-FAR1-A1"))
	}
	world.fleet.ships = ships
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Len(t, world.ledger.spareHulls, DefaultMaxAdoptions,
		"the co-located arm takes its cap and stops")
	require.Equal(t, DefaultMaxAdoptions, logger.payload("parked_sensing_cutover")["probes_adopted"])
	require.Equal(t, "PROBE-SCANNING",
		world.ledger.slots[psSlotKey{"X1-FAR1-A1", parkedsensing.SlotKindMarket}].AssignedShip,
		"and the scanning placement at that waypoint keeps its own probe")
}
