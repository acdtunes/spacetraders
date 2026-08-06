package shared

import "testing"

// ChartSkippable decides MEMBERSHIP of the charting set; ChartPriority decides
// SEQUENCE within it. These tests pin the boundary between them, because the
// cost of confusing the two is asymmetric: over-skipping silently stops charting
// real markets and yards, and nothing downstream would report it.

// ONLY THE BARREN TIER IS SKIPPABLE. Everything with any observed market or
// shipyard is still flown, including the rare cases.
func TestChartSkippable_SkipsOnlyTheTypeWithNoObservedMarketOrShipyard(t *testing.T) {
	if !ChartSkippable(WaypointTypeAsteroid) {
		t.Fatal("ASTEROID is 0-for-114,838 charted across two universes; it is the one type worth skipping and the whole saving depends on it")
	}
	for _, waypointType := range []string{
		WaypointTypeOrbitalStation, WaypointTypeJumpGate, WaypointTypeMoon,
		WaypointTypePlanet, WaypointTypeFuelStation, WaypointTypeAsteroidBase,
		WaypointTypeEngineeredAsteroid, WaypointTypeGasGiant,
	} {
		if ChartSkippable(waypointType) {
			t.Fatalf("%s is skippable, but it carries markets or shipyards in the census. Skipping it stops charting real ones and nothing downstream would report the loss", waypointType)
		}
	}
}

// GAS_GIANT IS THE BOUNDARY CASE and the bead named it explicitly: at 133 of
// 1154 it holds a market 11.5% of the time. Rare is not never.
//
// It also sits directly ABOVE the barren tier in the ordering, so a skip written
// as "drop the last tier" or "drop anything at or below unproven" takes it too —
// which is why it is asserted apart from the loop above.
func TestChartSkippable_DoesNotTakeTheRareTypeSittingJustAboveBarren(t *testing.T) {
	if ChartSkippable(WaypointTypeGasGiant) {
		t.Fatal("GAS_GIANT is skipped; it is RARE (133/1154), not never, and it ranks immediately above the barren tier — this is what an off-by-one-tier skip looks like")
	}
	if ChartPriority(WaypointTypeGasGiant) >= ChartPriority(WaypointTypeAsteroid) {
		t.Fatal("fixture is inert: GAS_GIANT no longer ranks above ASTEROID, so this test can no longer detect a skip that swallows the adjacent tier")
	}
}

// AN UNRECOGNISED TYPE IS CHARTED, NOT SKIPPED. If the game adds a waypoint type
// we must go and look at it rather than silently never looking — absence of
// evidence is not evidence of barrenness, and the failure is invisible.
func TestChartSkippable_ChartsATypeItHasNeverHeardOf(t *testing.T) {
	for _, unknown := range []string{"", "DYSON_SPHERE", "ASTEROID_FIELD", "asteroid"} {
		if ChartSkippable(unknown) {
			t.Fatalf("%q is skipped, but nothing is known about it. A skip must rest on positive evidence of barrenness; skipping the unknown means never gathering any", unknown)
		}
	}
}

// The barren tier matches the type EXACTLY. ASTEROID_BASE and
// ENGINEERED_ASTEROID share its prefix and are ~100% market-bearing, so a
// prefix or substring test would drop guaranteed markets while still looking
// like a working skip.
func TestChartSkippable_MatchesTheTypeExactlyAndNotByPrefix(t *testing.T) {
	for _, cousin := range []string{WaypointTypeAsteroidBase, WaypointTypeEngineeredAsteroid} {
		if ChartSkippable(cousin) {
			t.Fatalf("%s is skipped by a prefix match; it carries a market in nearly every observation", cousin)
		}
	}
}
