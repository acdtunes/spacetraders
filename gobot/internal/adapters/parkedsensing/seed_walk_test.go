package parkedsensing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// seed_walk_test.go pins the charting seed's CROSSING, now that a seed may be
// sent further than one gate.
//
// JumpTo used to name the errand's FINAL system on every jump, which is correct
// only while the target is a direct neighbour. Seed staging now reaches
// MaxWalkRings, so the final system is routinely NOT connected to the gate the
// hull is standing on — and a jump naming an unconnected system is rejected by
// the API, leaving the hull re-issuing a refused command every tick. That stall
// looks exactly like a reactor cooldown, which is what makes it worth a test.
//
// What it does instead is the walk RouteAcross already performs for placements:
// resolve the next hop from stored adjacency BEFORE moving, dispatch one step,
// and let the next tick resume from the ships table. These tests run against the
// same persistent walkWorld the placement walk is pinned on, so "it crosses" is
// shown over consecutive ticks rather than asserted of a single call.

// seedHopGates is the two-hop graph the tests below walk: X1-AA -> X1-BB ->
// X1-CC. It is shared with the single-hop JumpTo tests next door in
// mover_dispatch_test.go, which now need a gate graph at all: the crossing
// resolves its next system from stored adjacency, so a seed port wired to an
// empty store fails closed rather than jumping at whatever it was handed.
//
// FORWARD EDGES ONLY, deliberately. The stored graph really is asymmetric —
// measured live, 617 of 5463 edges have no reverse row, because a gate charted
// from one end names a system whose own gate we have not charted yet — and a
// walk that quietly relied on symmetry would route hulls down edges the store
// cannot confirm.
var seedHopGates = stubGateNeighbours{edges: map[string][]string{
	"X1-AA": {"X1-BB"},
	"X1-BB": {"X1-CC"},
}}

// seedWorldAt builds a world with one hull standing at shipAt and a named gate
// in each system on the route.
func seedWorldAt(shipAt string) *walkWorld {
	world := newWalkWorld()
	world.gateOf["X1-AA"] = "X1-AA-J1"
	world.gateOf["X1-BB"] = "X1-BB-J1"
	world.gateOf["X1-CC"] = "X1-CC-J1"
	world.shipAt["PROBE-A"] = shipAt
	world.navStatus["PROBE-A"] = navigation.NavStatusInOrbit
	return world
}

// runJump performs one JumpTo step and fails the test if it blocks — the one
// thing this whole engine forbids.
func runJump(t *testing.T, world *walkWorld, gates stubGateNeighbours, from, target string) error {
	t.Helper()
	seed := adapterSensing.NewSeedCommandPort(world, nil, nil, nil, nil, gates)

	done := make(chan error, 1)
	go func() {
		done <- seed.JumpTo(context.Background(), testPlayerID, "PROBE-A", from, target)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(tickDeadline):
		close(world.release)
		<-done
		t.Fatalf("the seed's gate step was still waiting after %v — the tick is parked inside a "+
			"flight, so expansion never finishes and no seed ever charts: %v", tickDeadline, world.commands())
		return nil
	}
}

// TestSeedCommandPort_JumpTo_JumpsToTheNextHopNotTheTargetSystem is the headline
// case for the widened reach.
func TestSeedCommandPort_JumpTo_JumpsToTheNextHopNotTheTargetSystem(t *testing.T) {
	world := seedWorldAt("X1-AA-J1") // standing ON its gate, so this step is the jump

	require.NoError(t, runJump(t, world, seedHopGates, "X1-AA-J1", "X1-CC"))

	require.Equal(t, []string{"X1-BB"}, world.jumps(),
		"the seed jumped somewhere other than the reachable intermediate — naming the errand's "+
			"final system is a jump the API rejects, and the hull re-issues it forever: %v", world.commands())
}

// A one-hop errand still jumps straight to its target: the walk resolves the
// same answer for a direct neighbour, so the case that already worked is
// unchanged.
func TestSeedCommandPort_JumpTo_ADirectNeighbourIsStillOneJump(t *testing.T) {
	world := seedWorldAt("X1-AA-J1")

	require.NoError(t, runJump(t, world, seedHopGates, "X1-AA-J1", "X1-BB"))

	require.Equal(t, []string{"X1-BB"}, world.jumps(), "commands: %v", world.commands())
}

// TestSeedCommandPort_JumpTo_FailsClosedWithoutMovingWhenUnroutable is the
// property the change turns on: the route is resolved BEFORE any movement, so an
// unreachable destination never buys a wasted flight to a gate.
//
// A seed flown to a gate it cannot leave through is worse than one never
// dispatched — it has spent fuel, it is further from anywhere useful, and it
// goes on holding probe-cap headroom while charting nothing.
func TestSeedCommandPort_JumpTo_FailsClosedWithoutMovingWhenUnroutable(t *testing.T) {
	// OFF the gate, so an implementation that resolved its route too late would
	// fly the hull to the gate first and only then find it has nowhere to go.
	world := seedWorldAt("X1-AA-M1")

	err := runJump(t, world, seedHopGates, "X1-AA-M1", "X1-ZZ")
	require.Error(t, err, "an unroutable errand reported success without moving anything")

	require.False(t, world.sentAny("NavigateDirectCommand"),
		"the seed was flown toward a gate it has no route out of: %v", world.commands())
	require.False(t, world.sentAny("JumpShipCommand"),
		"a jump was attempted with no next system named: %v", world.commands())
	require.Equal(t, "X1-AA-M1", world.positionOf("PROBE-A"),
		"the hull moved despite the walk failing closed: %v", world.commands())
}

// A target beyond MaxWalkRings is refused for the same reason, and this is the
// adapter half of the bound the application layer enforces at staging time.
func TestSeedCommandPort_JumpTo_RefusesATargetOutsideTheGateComponent(t *testing.T) {
	world := seedWorldAt("X1-AA-J1")
	// A target in a DIFFERENT component: no chain of stored edges reaches it at any depth.
	//
	// This used to build a chain one hop longer than a numeric bound. That bound is gone — a seed's
	// reach is now the traversable component itself — so "beyond the walk" no longer means "too many
	// rings", it means "not connected". The refusal is unchanged and so is what it protects: naming
	// no next system leaves the hull standing still rather than flown partway and stranded.
	gates := stubGateNeighbours{edges: map[string][]string{
		"X1-AA": {"X1-BB"},
		"X1-BB": {"X1-CC"},
		"X1-CC": {"X1-AA"},
		// X1-ZZ appears nowhere: another component entirely.
	}}

	require.Error(t, runJump(t, world, gates, "X1-AA-J1", "X1-ZZ"),
		"a target outside the gate component was accepted; the hull would be walked partway and stranded")
	require.False(t, world.sentAny("JumpShipCommand"), "commands: %v", world.commands())
	require.False(t, world.sentAny("NavigateDirectCommand"), "commands: %v", world.commands())
}

// The crossing resumes from the ships table, one hop per tick, exactly as the
// placement walk does — nothing is persisted about how far it has got, so a
// restart mid-walk picks up from where the hull actually is.
//
// Three consecutive ticks of ONE two-hop errand against ONE world. A single-call
// test cannot reach this: it is the RE-DERIVATION from a moved hull that carries
// the seed across, and an implementation that cached a route would pass every
// test above and still send the second jump to the wrong system.
func TestSeedCommandPort_JumpTo_CrossesTwoGatesOverConsecutiveTicks(t *testing.T) {
	world := seedWorldAt("X1-AA-M1") // parked at its old slot, not the gate

	// Tick 1: fly to X1-AA's gate. No jump — it is not standing on one yet.
	require.NoError(t, runJump(t, world, seedHopGates, world.positionOf("PROBE-A"), "X1-CC"))
	require.True(t, world.sentAny("NavigateDirectCommand"), "commands: %v", world.commands())
	require.Empty(t, world.jumps(), "jumped from off the gate: %v", world.commands())
	require.Equal(t, "X1-AA-J1", world.positionOf("PROBE-A"))

	// Tick 2: on X1-AA's gate. Jump to the INTERMEDIATE, not the target.
	require.NoError(t, runJump(t, world, seedHopGates, world.positionOf("PROBE-A"), "X1-CC"))
	require.Equal(t, []string{"X1-BB"}, world.jumps(), "commands: %v", world.commands())

	// The jump landed it in X1-BB; the arrival scheduler puts it on that
	// system's gate, which is where the next tick reads it.
	world.shipAt["PROBE-A"] = "X1-BB-J1"

	// Tick 3: the route is re-derived from the hull's NEW system, where the
	// target is now a direct neighbour.
	require.NoError(t, runJump(t, world, seedHopGates, world.positionOf("PROBE-A"), "X1-CC"))
	require.Equal(t, []string{"X1-BB", "X1-CC"}, world.jumps(),
		"the second leg did not re-derive from where the hull actually is: %v", world.commands())
}

// SELECTION MAY NEVER OUTRUN DELIVERY, and this pins it at the seam rather than by inspection.
//
// A destination past the ROUTER's reach fails SILENTLY, which is why this is worth a test of its own:
// nextHopToward names no next system, the step errors, and the slot stays IN_TRANSIT still naming a
// hull that counts against the probe cap and never arrives. Nothing logs a refusal loud enough to
// notice; the fleet just quietly holds a probe forever.
//
// The two sides read ONE declaration — the adapter's resolver bound IS appSensing.SeedFlightUnbounded
// — so they cannot drift into disagreement by editing a number. This test fails the moment someone
// gives the resolver a bound of its own while selection keeps walking the whole component.
func TestSeedWalk_TheResolverIsNeverNarrowerThanSelection(t *testing.T) {
	// A chain far longer than any ring bound this engine has carried. Selection stages targets
	// anywhere the component connects, so the resolver must name a next hop at the same distance.
	edges := map[string][]string{}
	const hops = 14
	prev := "X1-AA"
	for i := 1; i <= hops; i++ {
		next := fmt.Sprintf("X1-H%02d", i)
		edges[prev] = []string{next}
		prev = next
	}
	world := seedWorldAt("X1-AA-J1")
	world.gateOf["X1-AA"] = "X1-AA-J1"

	require.NoError(t, runJump(t, world, stubGateNeighbours{edges: edges}, "X1-AA-J1", prev),
		"the resolver refused a destination %d hops out while seed selection would stage it — a "+
			"destination past the router's reach strands the hull silently, holding probe-cap "+
			"headroom forever without ever arriving", hops)
	require.Equal(t, []string{"X1-H01"}, world.jumps(),
		"the resolver must name the FIRST hop toward a distant target: %v", world.commands())
}

// THE REFUSAL MUST REACH THE ENGINE, NOT JUST THE LOG. Returned as a bare error it is
// indistinguishable from a refused command, so the engine holds and retries forever.
func TestSeedCommandPort_JumpTo_AnUnroutableTargetIsMatchableByTheApplicationSentinel(t *testing.T) {
	world := seedWorldAt("X1-AA-M1")

	err := runJump(t, world, seedHopGates, "X1-AA-M1", "X1-ZZ")

	require.ErrorIs(t, err, appSensing.ErrSeedWalkUnroutable,
		"an unroutable target must satisfy errors.Is against the application's sentinel, or the engine "+
			"cannot tell a graph that names no route from a command the API refused — and holds forever")
	require.Contains(t, err.Error(), "no stored gate route",
		"the resolver's own words must survive the wrapping, or the sentinel replaces the diagnosis")
}

// NON-VACUITY: a routable walk must not wear the sentinel, or every errand stands down.
func TestSeedCommandPort_JumpTo_ARoutableTargetCarriesNoUnroutableSentinel(t *testing.T) {
	world := seedWorldAt("X1-AA-J1")

	require.NoError(t, runJump(t, world, seedHopGates, "X1-AA-J1", "X1-CC"))
}
