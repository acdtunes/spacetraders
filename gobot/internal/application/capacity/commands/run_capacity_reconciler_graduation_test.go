package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// sp-difa.1 — the durable contract-graduation gate on the capacity reconciler (the PRIMARY re-spawner:
// boot-standing + demand from contract HISTORY + tier-1 reuse-idle reassignment that executes without
// the capital gate). When the operator has graduated a player, the reconciler must idle its
// contract-delivery reconciliation — no desired topology, no idle-hull reassignment — durably across
// restarts, so a boot-standing relaunch no longer re-strands hulls from history.

// fakeContractGraduation is the durable per-player era-scoped graduation read (sp-difa.1).
type fakeContractGraduation struct {
	graduated bool
	err       error
}

func (f fakeContractGraduation) IsContractGraduated(_ context.Context, _ int) (bool, error) {
	return f.graduated, f.err
}

// graduatedReassignFixture wires a reconciler whose PLAN→DIFF→GOVERN would approve a tier-1
// reassign-hull action (the exact re-strand: re-dedicate an idle pool hull to contract-delivery). The
// spy actuator records whether that reassignment actually executes.
func graduatedReassignFixture() *loopFixture {
	f := newLoopFixture()
	// The governor approves a reuse-idle reassignment — the re-dedication that strands a pool hull onto
	// the contract-delivery role. Without the graduation gate this executes on the actuator.
	f.governor.result = capacity.GovernResult{
		Approved: []capacity.Action{{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}},
	}
	return f
}

// GRADUATED: the reconciler idles the whole tick — no phase runs, no hull is reassigned to
// contract-delivery — and the outcome reports Graduated. This is the durable fix for the boot re-strand.
func TestCapacityReconciler_ContractGraduated_IdlesWithoutReassigning(t *testing.T) {
	f := graduatedReassignFixture()
	h := f.handler()
	h.SetContractGraduationReader(fakeContractGraduation{graduated: true})

	outcomes := runTicks(t, h, reconcilerCmd(), 2, nil)

	require.Empty(t, f.log.snapshot(), "a graduated player must run NO reconcile phase (SENSE/PLAN/DIFF/GOVERN)")
	require.Empty(t, f.actuator.calls(capacity.VerbReassignHull), "a graduated player must NOT reassign a hull to contract-delivery (no re-strand)")
	require.Equal(t, 0, f.actuator.totalCalls(), "a graduated player idles: zero actuator calls")
	for _, out := range outcomes {
		require.True(t, out.Idle, "graduated tick must idle")
		require.True(t, out.Graduated, "graduated tick must report Graduated")
		require.Empty(t, out.ActionsExecuted)
	}
}

// NOT GRADUATED (baseline / byte-identical): with the reader returning false the reconciler runs
// exactly as today and the reuse-idle reassignment EXECUTES — proving the graduation gate is what
// suppresses it, not some other change. Also covers the fail-OPEN contract (a false/err read runs).
func TestCapacityReconciler_NotGraduated_ReconcilesAndReassigns(t *testing.T) {
	f := graduatedReassignFixture()
	h := f.handler()
	h.SetContractGraduationReader(fakeContractGraduation{graduated: false})

	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot(), "an un-graduated player reconciles as today")
	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 1, "un-graduated: the reuse-idle reassignment executes (the funding-floor behavior)")
}

// FAIL-OPEN: a read error is treated as UN-graduated (the reconciler runs), so a transient DB hiccup
// never silently suppresses the funding floor.
func TestCapacityReconciler_GraduationReadError_FailsOpen(t *testing.T) {
	f := graduatedReassignFixture()
	h := f.handler()
	h.SetContractGraduationReader(fakeContractGraduation{graduated: true, err: context.DeadlineExceeded})

	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot(), "a graduation READ ERROR must fail OPEN — reconcile as today")
	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 1)
}

// UNWIRED (nil reader): byte-identical to pre-sp-difa.1 — the reconciler never idles for graduation.
func TestCapacityReconciler_NilGraduationReader_ByteIdentical(t *testing.T) {
	f := graduatedReassignFixture()
	h := f.handler() // no SetContractGraduationReader

	runTicks(t, h, reconcilerCmd(), 1, nil)

	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot())
	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 1)
}
