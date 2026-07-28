package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// The coordinator half of the sensing probe-buy foreign-key fix.
//
// The adapter's own tests (internal/adapters/parkedsensing) prove that the claim
// owner must be a real container id, because ships.container_id carries a foreign
// key to containers(id) and the database refuses anything else. Those tests pin
// the BOTTOM of the chain. This one pins the TOP: that the coordinator actually
// hands its own live container id down to the purchase port.
//
// Both halves are needed, and the gap between them is not theoretical. The adapter
// now fails the buy CLOSED when it is handed no owner, so a coordinator that
// passed the empty string would leave the fleet exactly where the bug left it —
// no probes bought, no charting seeds, no new systems — while every adapter-level
// test still passed. A fix that is never consulted is not a fix.

// TestReconcile_HandsThePurchaserThisTicksContainerID drives a real tick through
// to a purchase and asserts what the purchase port was told.
func TestReconcile_HandsThePurchaserThisTicksContainerID(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})

	// An unfilled placement in the in-scope system: the thing a probe is bought FOR.
	world.ledger.slots["X1-IN1-M1"] = parkedsensing.QueuedSlot{
		Waypoint: "X1-IN1-M1", System: "X1-IN1",
		Kind: parkedsensing.SlotKindMarket, State: parkedsensing.SlotStateWanted,
	}
	// A yard in that system that sells probes, with one of our hulls docked at it
	// to do the buying.
	world.catalog.yards["X1-IN1"] = []string{"X1-IN1-Y1"}
	world.shipPos.docked["X1-IN1-Y1"] = "PROBE-BUYER"

	require.NoError(t, world.handler.ReconcileOnce(world.ctx, world.cmd))

	require.NotEmpty(t, world.purchaser.owners,
		"the tick never reached a purchase, so this test proves nothing about the claim owner — check the buy-queue fixture")
	require.Equal(t, world.cmd.ContainerID, world.purchaser.owners[0],
		"the purchase port must be handed THIS tick's container id: it is the owner written to ships.container_id, and only a real container row satisfies the foreign key there")

	// Belt and braces on the failure that would silently re-break the fleet: an
	// empty owner is refused by the adapter, so it must never be what we send.
	require.NotEmpty(t, world.purchaser.owners[0],
		"an empty claim owner is refused fail-closed by the purchase adapter, which would strand probe buying exactly as the original defect did")
}
