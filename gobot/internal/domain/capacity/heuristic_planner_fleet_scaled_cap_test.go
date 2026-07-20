package capacity_test

// sp-3idiw — the class hull ceiling scales to FLEET SIZE. A small fleet earns a small
// (or zero) depot; a larger fleet scales the depot up until an absolute backstop binds.
// Default-off (fraction 0) is byte-identical to the fixed ceiling. Reuses the
// fourEqualRichHubs fixture (four 3-hull hubs, 12 total) + totalDesiredHulls from
// heuristic_planner_cap_test.go.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// fleetOf sets the fixture's fleet hull count — the fleet-scaling input.
func fleetOf(signals capacity.Signals, hullCount int) capacity.Signals {
	signals.Economics.FleetHullCount = hullCount
	return signals
}

// Behavior: a small fleet earns a small depot — fraction 0.5 over an 8-hull fleet caps
// the class at 4 hulls, admitting exactly one 3-hull hub (the second would reach 6 > 4).
// RED: today the fleet fraction is ignored and all four hubs (12 hulls) are admitted.
func TestHeuristicPlanner_FleetScaledCeiling_SmallFleetGetsSmallDepot(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullFleetFraction = 0.5

	desired := computeDesired(t, fleetOf(fourEqualRichHubs(), 8), cal)

	require.Len(t, desired.Hubs, 1, "floor(8×0.5)=4-hull ceiling admits exactly one 3-hull hub")
	require.LessOrEqual(t, totalDesiredHulls(desired), 4)
}

// Behavior: a tiny fleet earns NO depot — fraction 0.5 over a 1-hull fleet resolves the
// ceiling to floor(0.5)=0, which under fleet-scaling means "no new depot" (NOT the OFF
// path's "0 = no cap"). RED: today the fraction is ignored and 0 reads as no-cap → 4 hubs.
func TestHeuristicPlanner_FleetScaledCeiling_TinyFleetGetsNoDepot(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullFleetFraction = 0.5

	desired := computeDesired(t, fleetOf(fourEqualRichHubs(), 1), cal)

	require.Empty(t, desired.Hubs, "a fleet-scaled ceiling of 0 desires no new depot (0 ≠ no-cap when scaling)")
}

// Behavior: the depot scales UP with the fleet — under one fraction, a larger fleet
// admits strictly more hulls than a small one. RED: today both admit the full 12.
func TestHeuristicPlanner_FleetScaledCeiling_LargerFleetScalesUp(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullFleetFraction = 0.5

	small := computeDesired(t, fleetOf(fourEqualRichHubs(), 8), cal)
	large := computeDesired(t, fleetOf(fourEqualRichHubs(), 24), cal)

	require.Greater(t, totalDesiredHulls(large), totalDesiredHulls(small),
		"a larger fleet must scale the depot up")
	require.Equal(t, 12, totalDesiredHulls(large), "floor(24×0.5)=12 admits every hub")
}

// Behavior: the absolute backstop binds a pathologically large fleet — fraction 0.5 over
// a 100-hull fleet would scale to 50, but the backstop (ContractDeliveryHullCeiling=6)
// clamps it down to 6 hulls (two hubs). The backstop protects against an unbounded depot.
func TestHeuristicPlanner_FleetScaledCeiling_AbsoluteBackstopBindsHugeFleet(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullFleetFraction = 0.5
	cal.ContractDeliveryHullCeiling = 6 // absolute backstop

	desired := computeDesired(t, fleetOf(fourEqualRichHubs(), 100), cal)

	require.Len(t, desired.Hubs, 2, "a 100-hull fleet scales to 50 but the backstop clamps to 6 → two hubs")
	require.LessOrEqual(t, totalDesiredHulls(desired), 6)
}

// Behavior (byte-identical): fraction 0 = OFF. The ceiling stays the fixed
// ContractDeliveryHullCeiling exactly as before fleet-scaling — a 6-hull fixed cap
// admits two 3-hull hubs regardless of fleet size.
func TestHeuristicPlanner_FleetScaledCeiling_OffIsByteIdentical(t *testing.T) {
	cal := capacity.DefaultCalibration()
	cal.ContractDeliveryHullFleetFraction = 0 // OFF
	cal.ContractDeliveryHullCeiling = 6

	desired := computeDesired(t, fleetOf(fourEqualRichHubs(), 100), cal)

	require.Len(t, desired.Hubs, 2, "fraction 0 keeps the fixed 6-hull ceiling — byte-identical to the pre-scaling cap")
}
