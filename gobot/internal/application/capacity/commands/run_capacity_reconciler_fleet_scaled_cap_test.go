package commands

// sp-3idiw (application seam) — the fleet-scaling fraction resolves from the launch
// config into the live Calibration the planner reads each tick, so fleet-scaled sizing
// is config-driven (RULINGS #5). Unset = 0 = OFF = byte-identical (fixed ceiling).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Behavior: an explicit fleet fraction on the command flows into the Calibration PLAN reads.
func TestCapacityReconciler_ContractDeliveryHullFleetFractionResolvesIntoCalibration(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd()
	cmd.ContractDeliveryHullFleetFraction = 0.5

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	require.Equal(t, 0.5, f.planner.gotCals[0].ContractDeliveryHullFleetFraction,
		"the resolved calibration must carry the fleet-scaling fraction into PLAN")
}

// Behavior (byte-identical): an unset fraction resolves to 0 = OFF (fixed ceiling).
func TestCapacityReconciler_UnsetFleetFractionIsOff(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd() // fraction unset

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	require.Zero(t, f.planner.gotCals[0].ContractDeliveryHullFleetFraction,
		"an unset fraction must resolve to 0 (OFF) — byte-identical fixed-ceiling PLAN")
}
