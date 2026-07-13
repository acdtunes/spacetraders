package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-hoc6: auto-launch a STANDING InputsOnly goods_factory feeder for each configured gate source
// EXPORT-factory, ALONGSIDE the construction drain. The feeder buys the source factory's IMPORT
// inputs and delivers them in, leaving the OUTPUT in export stock (InputsOnly=true, sp-q02m) so the
// drain remains the SOLE buyer/hauler. This sustains export supply/price so the drain's buying of the
// gate output stays under the buy-ceiling (sp-layd). The buy-vs-feed launch decision
// (planGateSourceFeeders) is a pure function so it is exercised here with no DB, no goroutines.

// The launch plan for the configured gate materials carries exactly the StartGoodsFactory feeder
// arguments: InputsOnly=true, Iterations=-1 (standing/infinite), the material's good, and its system.
func TestPlanGateSourceFeeders_LaunchesConfiguredMaterialsAsInputsOnlyInfinite(t *testing.T) {
	configured := []config.GateSourceFeeder{{Good: "FAB_MATS"}, {Good: "ADVANCED_CIRCUITRY"}}

	launches := planGateSourceFeeders(configured, "X1-HOME", map[string]bool{})

	require.Equal(t, []gateSourceFeederLaunch{
		{Good: "FAB_MATS", System: "X1-HOME", InputsOnly: true, Iterations: -1},
		{Good: "ADVANCED_CIRCUITRY", System: "X1-HOME", InputsOnly: true, Iterations: -1},
	}, launches)
}

// An empty System resolves to the home system; an explicit System overrides it (feed a source factory
// in another system without changing the launch mechanism).
func TestPlanGateSourceFeeders_ExplicitSystemOverridesHomeSystem(t *testing.T) {
	configured := []config.GateSourceFeeder{
		{Good: "FAB_MATS"}, // empty → home
		{Good: "ADVANCED_CIRCUITRY", System: "X1-OTHER"}, // explicit → wins
	}

	launches := planGateSourceFeeders(configured, "X1-HOME", map[string]bool{})

	require.Equal(t, "X1-HOME", launches[0].System)
	require.Equal(t, "X1-OTHER", launches[1].System)
}

// The feeder set is DERIVED from config, never hardcoded: an empty (or nil) configured set launches
// NOTHING and does not panic (regression guard — the mechanism must not invent a material).
func TestPlanGateSourceFeeders_EmptyConfigLaunchesNothing(t *testing.T) {
	require.Empty(t, planGateSourceFeeders(nil, "X1-HOME", map[string]bool{}))
	require.Empty(t, planGateSourceFeeders([]config.GateSourceFeeder{}, "X1-HOME", map[string]bool{}))
}

// Restart-resilience (RULINGS #2): the feeders are goods_factory coordinators re-adopted by
// RecoverRunningContainers on restart. The boot-standing pass is IDEMPOTENT — a good with an
// already-running InputsOnly feeder is skipped, so a restart never double-launches (the SAME
// mechanism the construction drain's EnsureRunning uses). A partially-running set launches only the
// missing feeders.
func TestPlanGateSourceFeeders_SkipsAlreadyRunning_RestartResilientIdempotent(t *testing.T) {
	configured := []config.GateSourceFeeder{{Good: "FAB_MATS"}, {Good: "ADVANCED_CIRCUITRY"}}

	// Both already running (post-restart, recovery re-adopted them) → launch nothing.
	require.Empty(t, planGateSourceFeeders(configured, "X1-HOME", map[string]bool{
		"FAB_MATS": true, "ADVANCED_CIRCUITRY": true,
	}))

	// Only FAB_MATS survived → relaunch just the missing ADVANCED_CIRCUITRY feeder.
	launches := planGateSourceFeeders(configured, "X1-HOME", map[string]bool{"FAB_MATS": true})
	require.Len(t, launches, 1)
	require.Equal(t, "ADVANCED_CIRCUITRY", launches[0].Good)
}

// Fail-safe: a blank good, or an empty-System feeder with no resolvable home system, is SKIPPED
// rather than launched with a bad target (never spin up a factory that cannot find its export market).
func TestPlanGateSourceFeeders_SkipsBlankGoodAndUnresolvableSystem(t *testing.T) {
	require.Empty(t, planGateSourceFeeders([]config.GateSourceFeeder{{Good: ""}}, "X1-HOME", map[string]bool{}))
	// Empty configured System AND empty home system → unresolvable → skip.
	require.Empty(t, planGateSourceFeeders([]config.GateSourceFeeder{{Good: "FAB_MATS"}}, "", map[string]bool{}))
	// But an explicit System still launches even with no home system resolved.
	launches := planGateSourceFeeders([]config.GateSourceFeeder{{Good: "FAB_MATS", System: "X1-X"}}, "", map[string]bool{})
	require.Len(t, launches, 1)
	require.Equal(t, "X1-X", launches[0].System)
}

// Every launch is InputsOnly=true — the invariant that keeps the construction drain the SOLE buyer
// and hauler of the output (InputsOnly leaves the fabricated output in export stock, sp-q02m; the
// feeder never harvests/hauls it).
func TestPlanGateSourceFeeders_AllLaunchesAreInputsOnly(t *testing.T) {
	configured := []config.GateSourceFeeder{
		{Good: "FAB_MATS"}, {Good: "ADVANCED_CIRCUITRY"}, {Good: "ELECTRONICS", System: "X1-E"},
	}
	for _, l := range planGateSourceFeeders(configured, "X1-HOME", map[string]bool{}) {
		require.True(t, l.InputsOnly, "feeder %s must be InputsOnly so the drain stays sole output-buyer", l.Good)
		require.Equal(t, -1, l.Iterations, "feeder %s must be standing/infinite", l.Good)
	}
}

// anyFeederMissing gates the (impure) home-system resolution: true iff some configured good has no
// running feeder yet. A warm restart with all feeders re-adopted returns false, so the boot pass does
// no ship read.
func TestAnyFeederMissing(t *testing.T) {
	configured := []config.GateSourceFeeder{{Good: "FAB_MATS"}, {Good: "ADVANCED_CIRCUITRY"}}

	require.True(t, anyFeederMissing(configured, map[string]bool{}))
	require.True(t, anyFeederMissing(configured, map[string]bool{"FAB_MATS": true}))
	require.False(t, anyFeederMissing(configured, map[string]bool{"FAB_MATS": true, "ADVANCED_CIRCUITRY": true}))
	require.False(t, anyFeederMissing(nil, map[string]bool{}))
	// A blank good is not "missing" (it is never launched), so it must not force resolution.
	require.False(t, anyFeederMissing([]config.GateSourceFeeder{{Good: ""}}, map[string]bool{}))
}

// parseFeederConfig reads a persisted goods_factory container's target good + inputs_only from its
// Config JSON (StartGoodsFactory persists both), so the idempotency check only counts InputsOnly
// feeders — a harvesting factory for the same good is NOT one of ours.
func TestParseFeederConfig_ReadsTargetGoodAndInputsOnly(t *testing.T) {
	good, inputsOnly := parseFeederConfig(`{"target_good":"FAB_MATS","inputs_only":true,"system_symbol":"X1-HOME"}`)
	require.Equal(t, "FAB_MATS", good)
	require.True(t, inputsOnly)

	good, inputsOnly = parseFeederConfig(`{"target_good":"FAB_MATS","inputs_only":false}`)
	require.Equal(t, "FAB_MATS", good)
	require.False(t, inputsOnly)

	// Malformed/absent config must not panic.
	good, inputsOnly = parseFeederConfig(`not json`)
	require.Equal(t, "", good)
	require.False(t, inputsOnly)
}

// The StartGoodsFactory launch config for a feeder builds a RunFactoryCoordinatorCommand that is
// InputsOnly=true and MaxIterations=-1 (standing/infinite) — the seam the plan feeds. Mirrors the
// construction-coordinator boot-standing command-build assertion (daemon_boot_standing_test.go).
func TestGoodsFactoryFeederCommand_BuildsInputsOnlyInfinite(t *testing.T) {
	s := newFactoryTestServer()
	built, err := s.buildCommandForType("goods_factory_coordinator", map[string]interface{}{
		"container_id":   "feeder-test",
		"target_good":    "FAB_MATS",
		"system_symbol":  "X1-HOME",
		"max_iterations": -1,
		"inputs_only":    true,
	}, 1, "feeder-test")
	require.NoError(t, err)

	cmd, ok := built.(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok, "expected *RunFactoryCoordinatorCommand, got %T", built)
	require.True(t, cmd.InputsOnly, "gate-source feeder must be InputsOnly (feed inputs, leave output for the drain)")
	require.Equal(t, -1, cmd.MaxIterations, "gate-source feeder must be standing/infinite")
	require.Equal(t, "FAB_MATS", cmd.TargetGood)
	require.Equal(t, "X1-HOME", cmd.SystemSymbol)
}
