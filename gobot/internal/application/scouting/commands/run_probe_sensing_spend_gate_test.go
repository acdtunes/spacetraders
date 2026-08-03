package commands

// A WHOLE SENSING CYCLE, driven end to end, with the expansion switch off.
//
// This is the test that answers the question the way it was asked: not "is the
// guard present" but "what does a real cycle do, and what does its line say".
// sp-com1h was a guard that WAS consulted and simply did not cover the path that
// spends, so reading the guard proved nothing — the only evidence that counts is a
// cycle that reaches the purchase port and does not buy.
//
// THE FIXTURE HAS TO BE ABLE TO BUY, and this is where the sibling test in
// run_probe_orphan_dispatch_test.go stops short: it wires no yard, so its drain
// skips every placement for want of a counter and its "nothing was bought"
// assertion passes on the previous code by its own admission. Here the in-scope
// system has a probe yard, a hull of ours docked at it, and a treasury far above
// the floor — the control below proves the identical world buys a probe with the
// switch on. Demand past the bound, or the bound is never consulted.
//
// AND THE MONEY FLOORS ARE THE DOCUMENTED DEFAULTS. sensingTestCmd() sets neither
// capex_reserve_credits nor capital_multiplier_k_milli, so both resolve to
// defaultCapexReserveCredits (100_000) and defaultCapitalMultiplierKMilli (2000) —
// NOT the 5,000,000 / 10,000 the leak was stopped with at the time. Those maxima
// starve legitimate coverage buying too and lapse the moment the treasury outgrows
// them; a test run against them would prove the workaround, not the fix.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// The world both cases run in is buyableWorld (run_probe_orphan_dispatch_test.go):
// an IN_SCOPE system with one unfilled MARKET placement, a probe yard in that
// system, and a hull of ours docked at it. It is reused rather than re-declared
// precisely because its sibling test already proves the drain buys in it — the
// control below re-proves it here, and a shared fixture cannot drift apart from
// the proof that it can spend.
const buyablePlacement = "X1-KP23-D40"

// runCycle drives one real reconcile and returns the cycle line it emitted.
func runCycle(t *testing.T, world *cutoverWorld) string {
	t.Helper()
	log := &messageLogger{}
	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, log), world.cmd))
	for _, msg := range log.messages {
		if strings.HasPrefix(msg, "Parked sensing cycle:") {
			return msg
		}
	}
	t.Fatalf("the tick emitted no cycle line at all: %v", log.messages)
	return ""
}

// THE CONTROL, first, because the OFF case below is only evidence if this one
// spends. Identical world, switch on, at the documented default floors.
func TestReconcile_ExpansionOn_ACycleBuysAProbeAtDefaultFloors(t *testing.T) {
	world := buyableWorld(t)
	world.cmd.ExpansionEnabled = 1 // on, as it ships

	line := runCycle(t, world)
	t.Logf("observed cycle line (switch ON): %s", line)

	require.NotEmpty(t, world.purchaser.owners,
		"the fixture could not buy even with the switch ON, so the OFF case proves nothing — "+
			"check the yard/docked-hull/treasury wiring, not the gate")
	require.Contains(t, line, "bought 1", "cycle line: %s", line)
	require.NotContains(t, line, "expansion_enabled",
		"a spending cycle must not claim the switch stopped it: %s", line)
}

// THE ACCEPTANCE TEST, at the coordinator, on the same world. `tune
// expansion_enabled 2` and a full cycle buys nothing and says so.
//
// Compare the line this used to produce with the switch already off:
//
//	Parked sensing cycle: ... bought 6 reused 0 queued 5 (6 attempts), ...
//	  expansion +0 discovered, 0 seed(s) requested, 0 claimed, 0 charted
//	  (spending paused: no seed purchase, no explorer demand)
//
// Expansion said paused. The queue bought six. Both halves were telling the truth
// about themselves, and together they said nothing true about the fleet.
func TestReconcile_ExpansionOff_AWholeCycleBuysNothingAndSaysWhy(t *testing.T) {
	world := buyableWorld(t)
	world.cmd.ExpansionEnabled = 2 // the off-switch, as the operator sets it

	line := runCycle(t, world)
	t.Logf("observed cycle line (switch OFF): %s", line)

	require.Contains(t, line, "bought 0 ",
		"a cycle with expansion_enabled=2 bought a probe — this is sp-com1h itself: %s", line)
	require.Contains(t, line, "expansion_enabled",
		"the line does not name the switch that stopped the buying, so an operator reading it during a "+
			"money hunt learns nothing they did not already know: %s", line)

	// Asserted at the ports that move money, not inferred from a counter. Nothing
	// was bought and nothing was even priced.
	require.Zero(t, world.calls.count("buy"),
		"the purchase port was reached with the switch off")
	require.Zero(t, world.calls.count("quote"),
		"a live shipyard price was read for a hull the cycle could not buy — the switch must stop the "+
			"API spend as well as the credits")
	require.Empty(t, world.purchaser.owners, "a purchase claim was taken with the switch off")

	// The placement is left open, unclaimed, for the tick the switch comes back on.
	row := world.ledger.slots[psSlotKey{buyablePlacement, parkedsensing.SlotKindMarket}]
	require.Equal(t, parkedsensing.SlotStateWanted, row.State,
		"the placement was moved out of WANTED by a cycle that could not fund it")
}
