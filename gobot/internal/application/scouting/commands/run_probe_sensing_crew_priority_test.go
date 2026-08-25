package commands

// run_probe_sensing_crew_priority_test.go pins the tick's claim order over the
// parked SPARE pool. The drain's spare reuse and expansion's crew claim draw on
// the same hulls, so which runs first IS the allocation policy:
//
//   - charting work outstanding: expansion runs BEFORE the drain, so an idle
//     spare within reach of a dark system becomes a crew hull rather than a
//     station — and a spare no crew can reach still reaches a station on the
//     SAME tick, through the drain behind it.
//   - no charting work: the legacy order stands untouched — fills first, the
//     placement machine behind them, expansion last.
//
// Each order is observed two ways at once: by outcome (which hull lands where —
// the contested spare is the policy made visible) and by the ledger's own call
// sequence, so a reorder that preserved one observable by accident cannot pass
// both.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// crewFillWorld is one IN_SCOPE system holding parked spare hulls and one
// unfilled MARKET placement the drain wants to fill from them.
func crewFillWorld(t *testing.T, spareHulls map[string]string) *cutoverWorld {
	t.Helper()
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.ledger.slots[psSlotKey{"X1-IN1-M9", parkedsensing.SlotKindMarket}] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M9", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateWanted,
	}
	world.ledger.goods["X1-IN1-M9"] = []string{"FOOD"}
	for waypoint, hull := range spareHulls {
		world.ledger.slots[psSlotKey{waypoint, parkedsensing.SlotKindSpare}] = parkedsensing.QueuedSlot{
			Waypoint: waypoint, System: "X1-IN1",
			Kind: parkedsensing.SlotKindSpare, State: parkedsensing.SlotStateParked,
			AssignedShip: hull,
		}
	}
	return world
}

// darkSystem puts charting work on the ledger: a PENDING system with uncharted
// waypoints, which is what holds the chart queue open.
func darkSystem(world *cutoverWorld, system string) {
	world.ledger.systems[system] = parkedsensing.ExpandSystem{
		System: system, Verdict: parkedsensing.VerdictPending,
		CatalogKnown: true, UnchartedCount: 3,
	}
	world.catalog.known[system] = true
	world.catalog.uncharted[system] = []string{system + "-U1", system + "-U2", system + "-U3"}
}

// expansionSystemsRead locates the expansion pass in the call sequence. Its
// Systems read shares its tag with the tick's own top-of-tick read (and with the
// reaper's, which fires only when a QUEUED claim exists — these fixtures hold
// none), and it is deterministically the LAST of them in either order, so the
// assertion binds to the last occurrence and pins the count: a new caller of the
// method breaks this loudly instead of silently rebinding the assertion.
func expansionSystemsRead(t *testing.T, world *cutoverWorld) int {
	t.Helper()
	require.Equal(t, 2, world.ledger.occurrences("Systems"),
		"want exactly the tick-top and expansion Systems reads; a new caller must not silently become 'the expansion pass' to this assertion")
	return world.ledger.lastIndexOf("Systems")
}

func requireExpansionBeforeDrain(t *testing.T, world *cutoverWorld) {
	t.Helper()
	expansion := expansionSystemsRead(t, world)
	drain := world.ledger.indexOf(drainReadEvent())
	require.NotEqual(t, -1, drain, "the drain ran")
	require.Less(t, expansion, drain,
		"with the chart queue open, expansion claims spares BEFORE the drain can reuse them")
}

func requireLegacyOrder(t *testing.T, world *cutoverWorld) {
	t.Helper()
	expansion := expansionSystemsRead(t, world)
	drain := world.ledger.indexOf(drainReadEvent())
	placements := world.ledger.indexOf(placementReadEvent())
	require.NotEqual(t, -1, drain, "the drain ran")
	require.NotEqual(t, -1, placements, "the placement machine ran")
	require.Less(t, drain, placements, "fills run ahead of the placement machine")
	require.Less(t, placements, expansion,
		"with no charting work the legacy order stands: expansion runs last")
}

func placementReadEvent() string {
	return "PlacementWorklist:" + parkedsensing.SlotStateBought + "," + parkedsensing.SlotStateInTransit
}

// With the chart queue open, the crew claim gets FIRST pick of the spare pool
// and the fill machine takes what the crew declined — on the same tick.
//
// The fixture is a genuine contest: both mechanisms prefer the SAME spare (the
// lowest waypoint — reuse scans the system's rows in ledger order, and the claim
// breaks its equal-hop tie on the same order), so the outcome says which ran
// first.
func TestReconcile_ChartQueueOpen_CrewClaimOutranksStationFill(t *testing.T) {
	world := crewFillWorld(t, map[string]string{
		"X1-IN1-A1": "PROBE-SPARE-1",
		"X1-IN1-B2": "PROBE-SPARE-2",
	})
	darkSystem(world, "X1-DARK")
	world.gates.link("X1-IN1", "X1-DARK")

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	dark := world.ledger.systems["X1-DARK"]
	require.Equal(t, "PROBE-SPARE-1", dark.SeedShip,
		"the crew claim picks first, so the contested spare charts the dark system instead of becoming a station")
	require.Equal(t, parkedsensing.SeedStateDispatched, dark.SeedState)
	_, held := world.ledger.slots[psSlotKey{"X1-IN1-A1", parkedsensing.SlotKindSpare}]
	require.False(t, held, "the claimed spare's row is released to the errand")

	market := world.ledger.slots[psSlotKey{"X1-IN1-M9", parkedsensing.SlotKindMarket}]
	require.Equal(t, parkedsensing.SlotStateInTransit, market.State,
		"the fill machine still fills its placement on this same tick")
	require.Equal(t, "PROBE-SPARE-2", market.AssignedShip,
		"…from the spare the crew declined")

	requireExpansionBeforeDrain(t, world)
}

// With the chart queue EMPTY the tick is the legacy tick, exactly: fills first,
// the placement machine behind them, expansion last — and the spare pool is the
// fill machine's.
func TestReconcile_ChartQueueEmpty_LegacyFillFirstOrderIsUnchanged(t *testing.T) {
	world := crewFillWorld(t, map[string]string{"X1-IN1-A1": "PROBE-SPARE-1"})

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	market := world.ledger.slots[psSlotKey{"X1-IN1-M9", parkedsensing.SlotKindMarket}]
	require.Equal(t, parkedsensing.SlotStateInTransit, market.State)
	require.Equal(t, "PROBE-SPARE-1", market.AssignedShip,
		"with no charting work the spare is the fill machine's")
	spare := world.ledger.slots[psSlotKey{"X1-IN1-A1", parkedsensing.SlotKindSpare}]
	require.Equal(t, parkedsensing.SlotStateWanted, spare.State,
		"the reused spare's own slot reverts to a want")

	requireLegacyOrder(t, world)
}

// A spare no crew can reach is not held back for charting: with the queue open
// the crew pass runs first, declines it, and the SAME tick's drain still turns
// it into a station. Charting priority must never starve coverage of hulls
// charting cannot use.
func TestReconcile_ChartQueueOpen_UnreachableSpareStillFillsStationsSameTick(t *testing.T) {
	world := crewFillWorld(t, map[string]string{"X1-IN1-A1": "PROBE-SPARE-1"})
	darkSystem(world, "X1-DARK") // no gate link: the dark system is beyond every spare's reach

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.Empty(t, world.ledger.systems["X1-DARK"].SeedShip,
		"no crew claim: nothing can walk a spare to the dark system")

	market := world.ledger.slots[psSlotKey{"X1-IN1-M9", parkedsensing.SlotKindMarket}]
	require.Equal(t, parkedsensing.SlotStateInTransit, market.State,
		"the unclaimable spare still reaches a station on this tick, not the next")
	require.Equal(t, "PROBE-SPARE-1", market.AssignedShip)

	requireExpansionBeforeDrain(t, world)
}
