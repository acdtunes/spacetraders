package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Adoption filtered on dedicated_fleet == "scout" (freshnessScoutFleetTag). On the live fleet NO
// hull carries that tag — all 12 probes are untagged or "probe-buyer" — so the pass had never
// adopted anything and structurally never could, while sensing logged "adopted 0 ... held at the
// buy floor" and tried to buy a TENTH probe with nine idle ones docked at the yard that sold them.
//
// These pin the widened eligibility and, just as importantly, the hulls it must still refuse.

// probeWithFleet builds a landed probe carrying an arbitrary dedicated_fleet tag.
func probeWithFleet(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	t.Helper()
	ship := scoutProbe(t, symbol, waypoint)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// nonProbeWithFleet builds a NON-satellite hull, to prove the frame filter still binds.
func nonProbeWithFleet(t *testing.T, symbol, waypoint, fleet string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	loc.SystemSymbol = shared.ExtractSystemSymbol(waypoint)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(80, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(testPlayerID), loc, fuel, 400, 0, cargo, 80,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// The core widening: an orphan is adopted whatever legacy model tagged it. Each hull stands at its
// OWN waypoint so the one-row-per-waypoint rule does not mask the eligibility question.
func TestAdoption_AbsorbsOrphansOfEveryLegacyTag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet string
	}{
		{"untagged — the 10 live hulls the freshness sizer never tagged", ""},
		{"scout — the touring model's own tag", freshnessScoutFleetTag},
		{"probe-buyer — left behind by the retired buyer coordinator", legacyProbeBuyerFleetTag},
		{"sensing_parked — tagged but never recorded (a failed write's other half)", parkedsensing.SensingParkedFleetTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
			world.posts.posts = nil
			world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-ORPHAN", "X1-IN1-FREE", tc.fleet)}
			logger := &capturingLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

			require.Equal(t, 1, logger.payload("parked_sensing_cycle")["adopted_stranded"],
				"an orphaned probe tagged %q must be absorbed", tc.fleet)
			row := world.ledger.slots["X1-IN1-FREE"]
			require.Equal(t, "PROBE-ORPHAN", row.AssignedShip, "the ledger now accounts for the hull")
			require.Equal(t, parkedsensing.SlotKindSpare, row.Kind)
			require.Equal(t, parkedsensing.SlotStateParked, row.State)
		})
	}
}

// RULINGS #7: widening the tag filter must NOT become a licence to poach. A probe dedicated to a
// live foreign fleet is another coordinator's hull; taking it would give one hull two writers.
// The allowlist is what keeps a future fleet tag safe by default rather than silently absorbable.
func TestAdoption_NeverPoachesAHullDedicatedToALiveForeignFleet(t *testing.T) {
	for _, fleet := range []string{"contract", "trade", "manufacturing", "long-haul", "some-future-fleet"} {
		t.Run(fleet, func(t *testing.T) {
			world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
			world.posts.posts = nil
			world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-FOREIGN", "X1-IN1-FREE", fleet)}
			logger := &capturingLogger{}

			require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

			require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"],
				"a hull dedicated to %q belongs to that fleet", fleet)
			require.Empty(t, world.ledger.slots["X1-IN1-FREE"].AssignedShip)
			require.NotContains(t, world.tagger.tagged, "PROBE-FOREIGN")
		})
	}
}

// The frame filter still binds: an untagged HAULER is not a probe, however orphaned it looks.
func TestAdoption_IgnoresNonProbeFrames(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{nonProbeWithFleet(t, "HAULER-1", "X1-IN1-FREE", "")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
	require.Empty(t, world.ledger.slots["X1-IN1-FREE"].AssignedShip)
}

// MID-TOUR POLICY. Three live probes are `active` on scout_tour containers. A hull an active
// container is driving already has a single writer, and it is not us (RULINGS #3): recording it as
// a parked spare here would let the placement machine re-task a hull mid-flight while the tour
// still believes it owns it. Adoption is a STANDING per-tick retry precisely so nothing has to be
// grabbed urgently — the hull is picked up the tick after its tour releases it.
func TestAdoption_LeavesAHullAnActiveContainerIsDriving(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	touring := probeWithFleet(t, "PROBE-TOURING", "X1-IN1-FREE", "")
	require.NoError(t, touring.AssignToContainer("scout_tour-abc", shared.NewRealClock()))
	world.fleet.ships = []*navigation.Ship{touring}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"a hull mid-tour must not be taken out from under its container")
	require.Empty(t, world.ledger.slots["X1-IN1-FREE"].AssignedShip)
	require.NotContains(t, world.tagger.tagged, "PROBE-TOURING")
}

// THE HULLS THE WIDENING ALONE STILL MISSES, and the reason the change goes further.
//
// On the live fleet seven idle probes stand on X1-KP23-A2 — the yard that sold them — and that
// waypoint already carries a hull-less WANTED/MARKET placement. The occupancy guard skipped them,
// so widening the tag filter alone would have absorbed ONE hull of twelve and left the rest
// invisible to the probe cap.
//
// A hull standing ON the placement that wants a hull is not a conflict, it is the answer: fill the
// row in place. Kind stays MARKET (the scan rotation is preserved — the concern the prior
// leaves-the-row-alone pin was protecting), state goes WANTED -> PARKED through the guarded
// transition, and the row names the hull it already has standing on it.
func TestAdoption_FillsAHullLessWantedPlacementTheOrphanIsStandingOn(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateWanted, // names no hull
	}
	world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-ONSITE", "X1-IN1-M1", "")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	row := world.ledger.slots["X1-IN1-M1"]
	require.Equal(t, "PROBE-ONSITE", row.AssignedShip, "the placement is filled by the hull standing on it")
	require.Equal(t, parkedsensing.SlotKindMarket, row.Kind,
		"kind must stay MARKET — rewriting it to SPARE is what drops it out of the scan rotation")
	require.Equal(t, parkedsensing.SlotStateParked, row.State, "a hull already standing there is PARKED")
	require.Equal(t, 1, logger.payload("parked_sensing_cycle")["adopted_stranded"])
}

// A QUEUED placement is NOT filled: the drain has already claimed it for purchase and its
// TransitionSlot(QUEUED->BOUGHT) is guarded on that state. Filling it here would break that claim.
func TestAdoption_NeverFillsAQueuedPlacement(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateQueued, PurchaseYard: "X1-IN1-Y1",
	}
	world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-ONSITE", "X1-IN1-M1", "")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	row := world.ledger.slots["X1-IN1-M1"]
	require.Equal(t, parkedsensing.SlotStateQueued, row.State, "an in-flight purchase claim is untouched")
	require.Empty(t, row.AssignedShip)
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
}

// A placement that already NAMES a hull is never evicted, whatever is standing on it.
func TestAdoption_NeverEvictsAnIncumbentFromAFilledPlacement(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateParked, AssignedShip: "PROBE-INCUMBENT",
	}
	world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-INTRUDER", "X1-IN1-M1", "")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "PROBE-INCUMBENT", world.ledger.slots["X1-IN1-M1"].AssignedShip,
		"the incumbent keeps its row; evicting it drops a working probe out of the cap")
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
}

// Two orphans on ONE hull-less placement: exactly one fills it, the other is left for a later tick
// (one row per waypoint). This is the live X1-KP23-A2 shape, where seven hulls share a waypoint.
func TestAdoption_TwoOrphansOnOnePlacement_FillsExactlyOne(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateWanted,
	}
	world.fleet.ships = []*navigation.Ship{
		probeWithFleet(t, "PROBE-A", "X1-IN1-M1", ""),
		probeWithFleet(t, "PROBE-B", "X1-IN1-M1", ""),
	}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, 1, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"one row per waypoint: the second hull waits (sp-dpfp8)")
	require.Contains(t, []string{"PROBE-A", "PROBE-B"}, world.ledger.slots["X1-IN1-M1"].AssignedShip)
}

// Idempotence survives the widening: a settled fleet still costs zero writes, however many ticks
// run. Without this the widened filter would re-adopt every already-recorded hull every tick.
func TestAdoption_WidenedFilterIsStillIdempotent(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-ORPHAN", "X1-IN1-FREE", "")}

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))
	writesAfterFirst := len(world.ledger.upserted)

	logger := &capturingLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, writesAfterFirst, len(world.ledger.upserted),
		"a settled fleet must cost no further writes")
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
}

// The defensive half of the fill guard, found by mutation probe: deleting the AssignedShip check
// left every other test green, because the shapes they use (PARKED/QUEUED incumbents) are already
// refused by the state check alone. Only a WANTED row that NAMES a hull isolates it.
//
// ledgerHolds' own doc calls this combination out: "A WANTED row that named a scout-tagged hull
// would make this pass skip a hull the cap does not count, which is the money-unsafe direction. No
// writer produces that combination today." No writer producing it today is exactly why it must be
// pinned — the guard is what turns an invariant held by CONVENTION into one held by CONSTRUCTION,
// and a convention has no test to fail when a future writer breaks it.
func TestAdoption_NeverStealsAWantedPlacementThatAlreadyNamesAHull(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1", Kind: parkedsensing.SlotKindMarket,
		State: parkedsensing.SlotStateWanted, AssignedShip: "PROBE-PROMISED",
	}
	world.fleet.ships = []*navigation.Ship{probeWithFleet(t, "PROBE-INTRUDER", "X1-IN1-M1", "")}
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Equal(t, "PROBE-PROMISED", world.ledger.slots["X1-IN1-M1"].AssignedShip,
		"a row that already names a hull is never re-pointed, whatever state it is in")
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"])
	require.NotContains(t, world.tagger.tagged, "PROBE-INTRUDER")
}
