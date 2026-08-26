package commands

// probe_sensing_expansion_floor_test.go pins the SEPARATION of the expansion
// pause threshold from the scan pacer's floor.
//
// The two answer different questions. min_scan_rate_milli asks "how much scanning
// is guaranteed when the API is under pressure"; the expansion floor asks "how
// starved must the residual be before the fleet stops opening new charting work".
// While one key served both, raising the scan floor moved the expansion threshold
// with it — and since the emergency brake multiplies the residual DOWN while the
// pacer re-imposes the floor, raising the scan floor could only ever make the
// expansion gate harder to clear. A scan-rate change therefore stopped charting
// outright, delivering no extra scans in exchange.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// THE HEADLINE PROPERTY: moving the scan floor does not move the expansion floor.
func TestExpansionFloor_IsIndependentOfTheScanFloor(t *testing.T) {
	cmd := sensingTestCmd()
	base := resolveSensingConfig(context.Background(), cmd, nil)
	baseline := expandKnobs(base).MinBudgetRate

	raised := resolveSensingConfig(context.Background(), cmd, liveconfig.Snapshot{
		"min_scan_rate_milli": 450,
	})

	require.Equal(t, 450, raised.MinScanRateMilli, "the scan floor did move")
	require.Equal(t, baseline, expandKnobs(raised).MinBudgetRate,
		"raising the scan pacer's floor must not raise the bar the expansion pass has to clear — "+
			"the brake drives the residual DOWN while the pacer re-imposes the floor, so a coupled "+
			"threshold turns a scan-rate change into a charting stop")
}

// And the expansion floor moves on its OWN key, live, without a rebuild.
func TestExpansionFloor_HasItsOwnLiveKnob(t *testing.T) {
	cmd := sensingTestCmd()

	tuned := resolveSensingConfig(context.Background(), cmd, liveconfig.Snapshot{
		// float64: the JSON-recovery shape a persisted config round-trips through.
		"expansion_min_budget_milli": float64(120),
	})

	require.Equal(t, 120, tuned.ExpansionMinBudgetMilli)
	require.InDelta(t, 0.120, expandKnobs(tuned).MinBudgetRate, 1e-9,
		"the knob is milli-req/s, the same convention as min_scan_rate_milli")
	require.Equal(t, defaultMinScanRateMilli, tuned.MinScanRateMilli,
		"and it leaves the scan floor where it was")
}

// It ships ARMED (RULINGS #22): with no config present at all the gate runs at its
// documented default rather than at zero.
func TestExpansionFloor_ShipsArmedAtItsDocumentedDefault(t *testing.T) {
	cfg := resolveSensingConfig(context.Background(), sensingTestCmd(), nil)

	require.Equal(t, defaultExpansionMinBudgetMilli, cfg.ExpansionMinBudgetMilli)
	require.Positive(t, expandKnobs(cfg).MinBudgetRate, "an unset knob must not disarm the gate")
}

// The default is sized against what the emergency brake can actually reach, which
// is the whole reason the coupled threshold was wrong. The brake is floored at 0.1
// and multiplies the residual AFTER it is clamped up to the scan floor, so against
// the default scan floor the deepest residual reachable is 0.010 req/s. The
// expansion floor has to sit near that, not at the scan floor itself — at the scan
// floor, the FIRST halving of the brake already pauses charting.
func TestExpansionFloor_TripsOnlyNearTheBrakeFloor(t *testing.T) {
	cfg := resolveSensingConfig(context.Background(), sensingTestCmd(), nil)
	floor := expandKnobs(cfg).MinBudgetRate
	scanFloor := float64(cfg.MinScanRateMilli) / 1000.0

	// One halving of the brake is ordinary API pressure and must NOT pause charting.
	require.Greater(t, scanFloor*0.5, floor,
		"a single brake halving must still clear the expansion floor, or routine pressure stops charting")

	// A brake driven to its own floor is a rate-limit storm, and that must.
	require.Less(t, scanFloor*0.1, floor,
		"a brake at its floor must not clear the expansion floor, or the gate never yields at all")
}

// Zero and negative both revert to the documented default, matching every other
// knob's `tune <key> 0` semantics — so the gate cannot be disarmed through the
// tune surface.
func TestExpansionFloor_ZeroAndNegativeRevertToTheDefault(t *testing.T) {
	for _, value := range []int{0, -50} {
		cmd := sensingTestCmd()
		cmd.ExpansionMinBudgetMilli = value
		cfg := resolveSensingConfig(context.Background(), cmd, nil)
		require.Equal(t, defaultExpansionMinBudgetMilli, cfg.ExpansionMinBudgetMilli,
			"launch value %d must resolve to the documented default", value)
	}
}
