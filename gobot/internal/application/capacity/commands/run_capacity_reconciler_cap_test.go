package commands

// sp-rt3b6 PART 2 (application seam) — the contract-delivery hull ceiling resolves
// from the launch config into the live Calibration every phase reads, so the planner
// cap is config-driven (RULINGS #5). The no-drift SOURCE (the autosizer's own
// fleet_ceiling_contract_delivery) is wired in the grpc adapter; here we prove the
// command field flows into cal.ContractDeliveryHullCeiling and that unset = 0 = no cap.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Behavior: an explicit contract-delivery hull ceiling on the command resolves into
// the Calibration the planner reads each tick — the plumbing that makes the cap live.
func TestCapacityReconciler_ContractDeliveryHullCeilingResolvesIntoCalibration(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd()
	cmd.ContractDeliveryHullCeiling = 12

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	require.Equal(t, 12, f.planner.gotCals[0].ContractDeliveryHullCeiling,
		"the resolved calibration must carry the contract-delivery hull ceiling into PLAN")
}

// Behavior (byte-identical): an unset ceiling resolves to 0 = NO cap in the domain —
// the resolver adds no independent default (the effective production value is injected
// from the autosizer's OWN ceiling in the grpc adapter, the single no-drift source).
func TestCapacityReconciler_UnsetContractDeliveryHullCeilingIsNoCap(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd() // ceiling unset

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	require.Zero(t, f.planner.gotCals[0].ContractDeliveryHullCeiling,
		"an unset ceiling must resolve to 0 (no cap) — byte-identical to pre-cap PLAN")
}
