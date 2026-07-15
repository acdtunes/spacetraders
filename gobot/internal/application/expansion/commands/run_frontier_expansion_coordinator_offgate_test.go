package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeOffGateSelector stands in for the warp-target selector at the port boundary.
type fakeOffGateSelector struct {
	target OffGateTarget
	found  bool
	err    error
	calls  int
}

func (f *fakeOffGateSelector) SelectTarget(_ context.Context, _ int, _ OffGateSelectionParams) (OffGateTarget, bool, error) {
	f.calls++
	return f.target, f.found, f.err
}

// fakeShipyardCoverage stands in for the gate-shipyard scan-exhaustion guard (trigger b).
type fakeShipyardCoverage struct {
	exhausted bool
	readable  bool
	err       error
}

func (f *fakeShipyardCoverage) GateShipyardsScanExhausted(_ context.Context, _ int) (bool, bool, error) {
	return f.exhausted, f.readable, f.err
}

// TestFrontier_OffGate_TunablesResolveLiveOverLaunchOverDefault pins sp-k645 test 6: the
// off-gate demand knobs resolve live-config over launch over documented default — the
// established sp-vwek/sp-0z7f precedence, matching the depth knobs.
func TestFrontier_OffGate_TunablesResolveLiveOverLaunchOverDefault(t *testing.T) {
	cmd := testCmd()
	cmd.OffGateQueueExhaustionCycles = 3
	cmd.OffGateWarpRangeFuel = 250
	cmd.OffGateValueWeight = 7
	cmd.OffGateFuelWeight = 2

	// No live snapshot → launch values apply.
	launch := resolveConfig(cmd, nil)
	require.Equal(t, 3, launch.OffGateQueueExhaustionCycles, "launch value applies with no live snapshot")
	require.Equal(t, 250, launch.OffGateWarpRangeFuel)
	require.Equal(t, 7, launch.OffGateValueWeight)
	require.Equal(t, 2, launch.OffGateFuelWeight)

	// A live snapshot overrides the launch values.
	live := resolveConfig(cmd, liveconfig.Snapshot{
		"off_gate_queue_exhaustion_cycles": 8,
		"off_gate_warp_range_fuel":         600,
		"off_gate_value_weight":            20,
		"off_gate_fuel_weight":             3,
	})
	require.Equal(t, 8, live.OffGateQueueExhaustionCycles, "live snapshot overrides launch")
	require.Equal(t, 600, live.OffGateWarpRangeFuel)
	require.Equal(t, 20, live.OffGateValueWeight)
	require.Equal(t, 3, live.OffGateFuelWeight)

	// An empty snapshot → every knob falls to its documented default.
	def := resolveConfig(testCmd(), liveconfig.Snapshot{})
	require.Equal(t, defaultOffGateQueueExhaustionCycles, def.OffGateQueueExhaustionCycles, "empty snapshot → default")
	require.Equal(t, defaultOffGateWarpRangeFuel, def.OffGateWarpRangeFuel)
	require.Equal(t, defaultOffGateValueWeight, def.OffGateValueWeight)
	require.Equal(t, defaultOffGateFuelWeight, def.OffGateFuelWeight)
}

// TestOffGateDemand_FiresOnQueueExhaustionAfterNCyclesAndExposesTarget pins sp-k645 tests 3+5:
// trigger (a) raises off-gate demand only after the gate-reachable expansion queue has been
// empty for N CONSECUTIVE cycles — never before N (the debounce; a firing on cycle 1 fails the
// early assertions, the mutation guard) — and the raised signal carries the selected warp
// target for slice C to consume.
func TestOffGateDemand_FiresOnQueueExhaustionAfterNCyclesAndExposesTarget(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: nil}) // empty queue every cycle → exhaustion
	h.SetOffGateTargetSelector(&fakeOffGateSelector{found: true, target: OffGateTarget{
		SystemSymbol: "X1-OFF", FromSystem: "X1-EDGE", WarpFuelCost: 5,
	}})

	cmd := testCmd()
	cmd.OffGateQueueExhaustionCycles = 3
	pid := cmd.PlayerID.Value()
	ctx := context.Background()

	// Cycles 1..N-1: debounced — no demand yet.
	for cycle := 1; cycle < 3; cycle++ {
		require.NoError(t, h.ReconcileOnce(ctx, cmd))
		sig, _ := h.OffGateDemand(pid)
		require.False(t, sig.Demanded, "no off-gate demand before N empty-queue cycles (cycle %d)", cycle)
	}

	// Cycle N: demand fires and exposes the selected target (test 5).
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	sig, ok := h.OffGateDemand(pid)
	require.True(t, ok, "the signal is exposed once evaluated")
	require.True(t, sig.Demanded, "off-gate demand fires at the Nth empty-queue cycle")
	require.Equal(t, 1, sig.ExplorerCount)
	require.Contains(t, sig.Reason, "virgin set exhausted")
	require.True(t, sig.HasTarget, "the signal exposes a selected warp target")
	require.Equal(t, "X1-OFF", sig.Target.SystemSymbol, "slice C reads the selected off-gate target")
	require.Equal(t, "X1-EDGE", sig.Target.FromSystem, "and the frontier edge it warps from")
}

// TestOffGateDemand_FiresOnHeavyYardHuntOnlyWhenScanExhausted pins sp-k645 test 4: trigger (b)
// raises off-gate demand when the fleet has a heavy-capacity shortfall it cannot buy
// (shortfall > 0, no heavy yard known) AND the gate shipyards are scan-exhausted — but NOT
// while shipyard coverage is still sparse (a heavy yard might yet be found on-gate). The queue
// is kept non-empty here so trigger (a) never fires and (b) is isolated.
func TestOffGateDemand_FiresOnHeavyYardHuntOnlyWhenScanExhausted(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	// Two virgins keep the queue non-empty across both cycles → trigger (a) stays silent.
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-V1", Hops: 1, KnownMarkets: 0, Charted: false, Scanned: false},
		{SystemSymbol: "X1-V2", Hops: 1, KnownMarkets: 0, Charted: false, Scanned: false},
	}})
	h.SetOffGateTargetSelector(&fakeOffGateSelector{found: true, target: OffGateTarget{SystemSymbol: "X1-OFF"}})
	h.SetDepthObjectiveReader(&fakeObjective{shortfall: 2, yardKnown: false, readable: true})

	cmd := testCmd()
	pid := cmd.PlayerID.Value()
	ctx := context.Background()

	// Coverage SPARSE → (b) must NOT fire.
	h.SetShipyardCoverageReader(&fakeShipyardCoverage{exhausted: false, readable: true})
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	sig, _ := h.OffGateDemand(pid)
	require.False(t, sig.Demanded, "no off-gate demand while gate shipyard coverage is still sparse")

	// Coverage EXHAUSTED → (b) fires.
	h.SetShipyardCoverageReader(&fakeShipyardCoverage{exhausted: true, readable: true})
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	sig, _ = h.OffGateDemand(pid)
	require.True(t, sig.Demanded, "off-gate demand fires on heavy-yard shortfall + no yard + scan-exhausted")
	require.Contains(t, sig.Reason, "heavy-yard", "the firing trigger is (b), not the queue-exhaustion (a)")
	require.True(t, sig.HasTarget, "the heavy-yard hunt also exposes a warp target")
}
