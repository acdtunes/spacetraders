package harness

// Scenario assertions for the capacity reconciler (st-6wa). Each test seeds a
// real DB world, drives N ticks of the REAL sensor->planner->differ through the
// coordinator, and asserts observable outcomes at the actuation boundary. The
// four load-bearing assertions:
//
//  1. CONVERGENCE + FIXPOINT — a seeded gap closes as the expected ordered,
//     cheapest-first actions; a world already at desired produces zero actions.
//  2. captain/DISABLED idles every tick (zero phase invocations).
//  3. tier-4 capital NEVER auto-executes (proposed, not bought; and the CONVERGE
//     backstop refuses even a wrongly-approved capital action).
//  4. DryRun actuates nothing — the plan surfaces on WouldExecute/WouldFile.
//
// Two assertions carry explicit falsifiability guards (see the mutation guard
// test and the kill-switch "clear" case).

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// Assertion 1a — CONVERGENCE. The seeded gap closes as an ordered, cheapest-
// first action set driven end-to-end by the REAL sensor->planner->differ over
// the seeded DB: the tier-2 reposition BEFORE the tier-3 buffer edits. Every
// expected value traces to the seed (not to an observed run):
//   - WH-2 is parked off the hub anchor       -> reposition to the hub;
//   - IRON's planner cap is ceil(30*1.5)=45    -> cut from the actual 120;
//   - COPPER is un-demanded (actual cap 60)    -> de-whitelisted to 0.
func TestHarness_ConvergesActualTopologyToDesired_CheapestFirst(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedConvergenceWorld(t, db)
	h := newHarness(t, db, playerID) // autonomy governor: cheap tiers -> Approved

	outcomes := h.runTicks(t, false, 1)

	require.Equal(t, []actionSummary{
		{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, Hub: convHub, Ship: "WH-2", Target: convHub},
		{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferCap, Hub: convHub, Good: "IRON", Cap: 45},
		{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferWhitelist, Hub: convHub, Good: "COPPER", Cap: 0},
	}, summarize(outcomes[0].ActionsExecuted), "the gap must close as cheapest-first ordered actions")

	// Each verb reached the actuator at the method its tier owns.
	require.Len(t, h.actuator.calls(capacity.VerbRepositionHull), 1)
	require.Len(t, h.actuator.calls(capacity.VerbAdjustBufferCap), 1)
	require.Len(t, h.actuator.calls(capacity.VerbAdjustBufferWhitelist), 1)
	// No capital, no idle-reuse, nothing proposed — the whole gap was free/cheap.
	require.Zero(t, h.actuator.executeCapitalCount())
	require.Empty(t, h.actuator.calls(capacity.VerbReassignHull))
	require.Empty(t, h.proposals.all())
	require.Empty(t, outcomes[0].FailedPhase)
	require.False(t, outcomes[0].Idle)
}

// Assertion 1b — FIXPOINT (idempotent convergence). With the actual topology
// seeded to match desired, the reconciler runs every phase but emits ZERO
// actions, twice — a stable fixpoint, not a one-shot.
func TestHarness_ReachesFixpoint_ZeroActionsWhenActualMatchesDesired(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedConvergedFixpointWorld(t, db)
	h := newHarness(t, db, playerID)

	outcomes := h.runTicks(t, false, 2)

	for i, out := range outcomes {
		require.Falsef(t, out.Idle, "tick %d RAN every phase — it simply found no work", i+1)
		require.Emptyf(t, out.FailedPhase, "tick %d must not fail", i+1)
		require.Emptyf(t, out.ActionsExecuted, "tick %d: actual already equals desired => zero actions", i+1)
		require.Emptyf(t, out.ProposalsFiled, "tick %d must file nothing", i+1)
	}
	require.Zero(t, h.actuator.total())
	require.Empty(t, h.proposals.all())
	require.Equal(t, 2, h.differ.count(), "the differ ran both ticks and both times found nothing to do")
}

// Assertion 1c — FALSIFIABILITY (differ mutation guard). Swap the REAL differ
// for the inert NoOpDiffer: the SAME seed that converges above now yields ZERO
// actions. This proves the convergence assertion is falsifiable — it is the
// real LadderDiffer wiring that drives the gap closed, not an always-green spy.
func TestHarness_NoOpDifferProducesNoConvergence_MutationGuard(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedConvergenceWorld(t, db)
	h := newHarness(t, db, playerID, withDiffer(capacity.NoOpDiffer{}))

	outcomes := h.runTicks(t, false, 1)

	require.Zero(t, h.actuator.total(), "a neutered differ must surface as ZERO convergence at the actuator")
	require.Empty(t, outcomes[0].ActionsExecuted)
	require.Positive(t, h.sensor.count(), "SENSE/PLAN still ran — only DIFF was neutered")
	require.Positive(t, h.planner.count())
}

// Assertion 2 — captain/DISABLED idles every tick with ZERO phase invocations,
// paired with its own falsifiability proof: the SAME wiring with the switch
// clear DOES invoke every phase. If the per-tick kill-switch check were removed
// from production, the engaged case would behave like the clear case (counts >
// 0) and the engaged assertion (counts == 0) would fail.
func TestHarness_KillSwitchEngaged_IdlesEveryTick(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedConvergenceWorld(t, db)

	t.Run("engaged: every tick idles, zero phase invocations", func(t *testing.T) {
		h := newHarness(t, db, playerID, withKillEngaged())

		outcomes := h.runTicks(t, false, 3)

		for i, out := range outcomes {
			require.Truef(t, out.Idle, "tick %d must idle under captain/DISABLED", i+1)
			require.Empty(t, out.ActionsExecuted)
			require.Empty(t, out.ProposalsFiled)
		}
		require.Zero(t, h.sensor.count(), "SENSE must not run under captain/DISABLED")
		require.Zero(t, h.planner.count(), "PLAN must not run")
		require.Zero(t, h.differ.count(), "DIFF must not run")
		require.Zero(t, h.actuator.total(), "CONVERGE must actuate nothing")
		require.Empty(t, h.proposals.all())
	})

	t.Run("clear: the same wiring DOES invoke every phase (falsifiability)", func(t *testing.T) {
		h := newHarness(t, db, playerID) // switch clear

		h.runTicks(t, false, 2)

		require.Equal(t, 2, h.sensor.count(), "SENSE runs each tick when the switch is clear")
		require.Equal(t, 2, h.planner.count())
		require.Equal(t, 2, h.differ.count())
		require.Positive(t, h.actuator.total(), "the cleared switch lets convergence actuate")
	})
}

// Assertion 3 — tier-4 capital NEVER auto-executes from the loop's own CONVERGE.
// Both cases drive a REAL tier-4 add_cluster (an uncovered demanded hub) and
// assert zero ExecuteCapital calls.
func TestHarness_CapitalNeverAutoExecutes(t *testing.T) {
	// (a) With the documented autonomy governor, the real capital gap is FILED as
	// a proposal — emitted for approval, never bought. (Once st-x00 lands, this
	// extends to assert the governor's ROI evidence + budget on the proposal.)
	t.Run("real tier-4 gap is proposed, never bought", func(t *testing.T) {
		db := newScenarioDB(t)
		playerID := seedUncoveredCapitalWorld(t, db)
		h := newHarness(t, db, playerID)

		outcomes := h.runTicks(t, false, 1)

		require.Zero(t, h.actuator.executeCapitalCount(), "capital must never reach ExecuteCapital via the loop")
		require.Zero(t, h.actuator.total(), "no cheap verb either — the whole gap is capital")
		filed := h.proposals.all()
		require.Len(t, filed, 1)
		require.Equal(t, capacity.VerbAddCluster, filed[0].Action.Verb)
		require.Equal(t, capacity.TierCapital, filed[0].Action.Tier)
		require.Equal(t, capitalHub, filed[0].Action.HubSymbol)
		require.Equal(t, 3, filed[0].Action.HullDelta, "1 warehouse + 1 stocker + 1 worker")
		require.Equal(t, playerID, filed[0].PlayerID, "the loop stamps the reconciling player before Submit")
		require.Len(t, outcomes[0].ProposalsFiled, 1)
		require.Empty(t, outcomes[0].FailedPhase)
	})

	// (b) STRUCTURAL BACKSTOP (invariant 4): even a governor that WRONGLY approves
	// the tier-4 action cannot make it execute — CONVERGE refuses it and records a
	// loud failure. This does not rest on governor correctness.
	t.Run("wrongly-approved tier-4 is refused by the converge backstop", func(t *testing.T) {
		db := newScenarioDB(t)
		playerID := seedUncoveredCapitalWorld(t, db)
		h := newHarness(t, db, playerID, withGovernor(capitalApprovingGovernor{}))

		outcomes := h.runTicks(t, false, 1)

		require.Zero(t, h.actuator.executeCapitalCount(),
			"invariant 4: an Approved tier-4 action must NEVER execute from the loop's converge")
		require.Zero(t, h.actuator.total())
		require.Empty(t, h.proposals.all())
		require.Equal(t, capacity.PhaseConverge, outcomes[0].FailedPhase,
			"a governor contradicting its own gate must be LOUD")
		require.Contains(t, outcomes[0].Error, "unapproved capital refused")
	})
}

// Assertion 4 — DryRun actuates nothing: SENSE/PLAN/DIFF/GOVERN run, but CONVERGE
// invokes no actuator verb and files no proposal — the planned set surfaces on
// TickOutcome.WouldExecute / WouldFile instead.
func TestHarness_DryRunActuatesNothing(t *testing.T) {
	t.Run("cheap plan surfaces on WouldExecute; nothing actuated", func(t *testing.T) {
		db := newScenarioDB(t)
		playerID := seedConvergenceWorld(t, db)
		h := newHarness(t, db, playerID)

		outcomes := h.runTicks(t, true /* DryRun */, 1)

		require.Zero(t, h.actuator.total(), "DryRun invokes NO actuator verb")
		require.Empty(t, h.proposals.all(), "DryRun never calls ProposalChannel.Submit")
		require.Empty(t, outcomes[0].ActionsExecuted)
		require.Empty(t, outcomes[0].FailedPhase, "observing is not failing")
		// The read-only phases still ran...
		require.Equal(t, 1, h.sensor.count())
		require.Equal(t, 1, h.planner.count())
		require.Equal(t, 1, h.differ.count())
		// ...and the FULL planned set is surfaced for a captain to watch.
		require.Equal(t, []actionSummary{
			{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, Hub: convHub, Ship: "WH-2", Target: convHub},
			{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferCap, Hub: convHub, Good: "IRON", Cap: 45},
			{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferWhitelist, Hub: convHub, Good: "COPPER", Cap: 0},
		}, summarize(outcomes[0].WouldExecute))
	})

	t.Run("capital plan surfaces on WouldFile; no proposal filed", func(t *testing.T) {
		db := newScenarioDB(t)
		playerID := seedUncoveredCapitalWorld(t, db)
		h := newHarness(t, db, playerID)

		outcomes := h.runTicks(t, true, 1)

		require.Zero(t, h.actuator.total())
		require.Zero(t, h.actuator.executeCapitalCount())
		require.Empty(t, h.proposals.all())
		require.Empty(t, outcomes[0].ProposalsFiled)
		require.Empty(t, outcomes[0].WouldExecute, "the whole gap is capital — nothing to execute")
		require.Len(t, outcomes[0].WouldFile, 1)
		require.Equal(t, capacity.VerbAddCluster, outcomes[0].WouldFile[0].Action.Verb)
	})
}

// Assertion 5 — TIER-1 IDLE REUSE (st-780). An uncovered demanded hub with three
// idle, undedicated, non-cluster hulls waiting closes its WHOLE 1/1/1 shortfall
// by REUSING those hulls (tier-1 reassign_hull), never proposing tier-4 capital.
// Before st-780 the SENSE lane never filled Topology.IdleHulls, so the ladder
// could not see the free hulls and escalated the entire gap to a tier-4
// add_cluster. Every expected value traces to the seed: the planner wants 1
// warehouse + 1 stocker + 1 worker (the same demand the capital scenario above
// proves), and the three seeded idle hulls (IDLE-1..3, taken in ship-symbol
// order) fill those roles, each retargeted onto the hub anchor.
func TestHarness_ReusesIdleHullsForUncoveredHub_TierOneNotCapital(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedReusableIdleWorld(t, db)
	h := newHarness(t, db, playerID) // autonomy governor: tier-1 -> Approved -> executed

	outcomes := h.runTicks(t, false, 1)

	require.Equal(t, []actionSummary{
		{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, Hub: capitalHub, Ship: "IDLE-1", Target: capitalHub},
		{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, Hub: capitalHub, Ship: "IDLE-2", Target: capitalHub},
		{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, Hub: capitalHub, Ship: "IDLE-3", Target: capitalHub},
	}, summarize(outcomes[0].ActionsExecuted), "the whole 1/1/1 gap must close by REUSING idle hulls (tier-1), not capital")

	require.Len(t, h.actuator.calls(capacity.VerbReassignHull), 3, "each role reused one idle hull via ReuseIdleHull")
	require.Zero(t, h.actuator.executeCapitalCount(), "the free idle hulls covered the whole gap — no capital")
	require.Empty(t, h.proposals.all(), "tier-1 reuse is autonomous — nothing proposed")
	require.Empty(t, outcomes[0].FailedPhase)
	require.False(t, outcomes[0].Idle)
}

// Assertion 5b — FALSIFIABILITY / MUTATION GUARD (st-780). The SAME seed, but a
// wrapper re-empties Topology.IdleHulls after SENSE — reproducing the bug this
// bead fixes. With the free hulls invisible, the identical 1/1/1 gap re-escalates
// straight to ONE tier-4 add_cluster proposal and zero reuse. This proves the
// SENSE lane's IdleHulls population is exactly what makes tier-1 reachable
// end-to-end: flipping only that signal flips the whole outcome tier-1 <-> tier-4.
func TestHarness_SuppressedIdleHullsReEscalateToCapital_MutationGuard(t *testing.T) {
	db := newScenarioDB(t)
	playerID := seedReusableIdleWorld(t, db)
	h := newHarness(t, db, playerID, withIdleHullsSuppressed())

	outcomes := h.runTicks(t, false, 1)

	require.Empty(t, h.actuator.calls(capacity.VerbReassignHull), "blanked IdleHulls => tier-1 reuse is unreachable")
	require.Zero(t, h.actuator.total(), "the whole gap is capital again — nothing executes autonomously")
	filed := h.proposals.all()
	require.Len(t, filed, 1, "the entire 1/1/1 gap re-escalates to ONE tier-4 add_cluster")
	require.Equal(t, capacity.VerbAddCluster, filed[0].Action.Verb)
	require.Equal(t, capacity.TierCapital, filed[0].Action.Tier)
	require.Equal(t, 3, filed[0].Action.HullDelta, "1 warehouse + 1 stocker + 1 worker, none reusable")
	require.Len(t, outcomes[0].ProposalsFiled, 1)
	require.Empty(t, outcomes[0].ActionsExecuted)
}
