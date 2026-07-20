package capacity_test

// The contract-delivery DEPOT capital demand must EMIT end-to-end.
//
// The existing PLAN-level tests stop at desired.WarehouseCount>0. These drive the
// WHOLE emit path — ComputeDesired → LadderDiffer.Diff → CapexEmitter.Govern — and
// assert the CapitalDemand the fleet autosizer bridge actually reads. The live
// regression was a mis-nested config arm that left the reconciler on the OFF path,
// where a trade-inflated fleet average suppressed every contract depot and the
// bridge saw a 0 gap. These pin the STRUCTURAL invariant the arm restores: an
// uncovered, positive-ROI depot emits warehouse+stocker capital regardless of the
// trade average or the idle-hull pool.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

type captureSink struct{ demand capacity.CapitalDemand }

func (s *captureSink) EmitCapitalDemand(d capacity.CapitalDemand) { s.demand = d }

// emitCapitalDemand runs the full GOVERN-emit pipeline over signals+calibration
// against the given ACTUAL topology, returning the CapitalDemand the bridge reads.
func emitCapitalDemand(t *testing.T, signals capacity.Signals, actual capacity.TopologySignals, cal capacity.Calibration) capacity.CapitalDemand {
	t.Helper()
	desired := computeDesired(t, signals, cal)
	actions, err := capacity.NewLadderDiffer().Diff(context.Background(), desired, actual, cal)
	require.NoError(t, err)
	sink := &captureSink{}
	_, err = capacity.NewCapexEmitter(sink).Govern(context.Background(), actions, signals.Economics, cal)
	require.NoError(t, err)
	return sink.demand
}

// The core invariant + the fix: an uncovered positive-ROI contract hub under a
// TRADE-INFLATED fleet average emits depot (warehouse+stocker) capital when armed,
// and emits NOTHING when disarmed (the live signature — a reconciler left
// on the OFF path saw a 0 gap while the operator believed it armed).
func TestContractDepotCapital_EmitsWhenArmed_DespiteTradeInflatedAverage(t *testing.T) {
	signals := contractMachineSignals()
	signals.Economics.FleetPerHullCrHr = 42000 // a live arbitrage run inflates the fleet average past every contract hub's marginal

	armed := emitCapitalDemand(t, signals, capacity.TopologySignals{}, armedTradeBlind(0, 0))
	require.True(t, armed.Present, "the emitter always publishes a snapshot")
	require.Positive(t, armed.Hulls, "an armed reconciler must emit a positive contract-delivery capital gap")
	require.Positive(t, armed.WarehouseHulls, "the depot's buffer warehouses are capital that must reach the autosizer")
	require.Positive(t, armed.StockerHulls, "the depot's stockers are capital that must reach the autosizer")

	off := emitCapitalDemand(t, signals, capacity.TopologySignals{}, plannerCalibration(0, 0))
	require.Equal(t, 0, off.Hulls,
		"the OFF path benchmarks the trade-inflated fleet average and suppresses the depot — the exact silent-0 the mis-nested arm left live")
}

// The depot capital is invariant to the idle-hull pool: reuse-eligible idle hulls
// draw down the cheap tiers, but a genuinely uncovered depot the fleet cannot fully
// self-staff still emits warehouse+stocker capital > 0 (the shipwright's invariant:
// depot capital must emit regardless of the idle-hauler count).
func TestContractDepotCapital_EmitsAcrossIdleHullPools(t *testing.T) {
	signals := contractMachineSignals()
	signals.Economics.FleetPerHullCrHr = 42000

	for _, idle := range []int{0, 1, 2} {
		actual := capacity.TopologySignals{IdleHulls: reuseEligibleHulls(idle)}
		got := emitCapitalDemand(t, signals, actual, armedTradeBlind(0, 0))
		require.Positive(t, got.Hulls, "idle=%d: an uncovered depot still needs capital", idle)
		require.Positive(t, got.WarehouseHulls+got.StockerHulls,
			"idle=%d: the depot (warehouse+stocker) is capital idle-reuse must not silently zero", idle)
	}
}

// reuseEligibleHulls builds n tier-1-reusable idle hulls (idle, undedicated,
// cargo-capable) — the pool the ladder may reassign into depot roles.
func reuseEligibleHulls(n int) []capacity.HullUtilization {
	hulls := make([]capacity.HullUtilization, 0, n)
	for i := 0; i < n; i++ {
		hulls = append(hulls, capacity.HullUtilization{
			ShipSymbol:    "IDLE-" + string(rune('A'+i)),
			Idle:          true,
			CargoCapacity: 80,
		})
	}
	return hulls
}
