package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// These tests pin the generalization of the re-apply: relaunching a
// previously-stopped TUNABLE coordinator via its `start` verb must RE-ADOPT the last
// persisted live-tuned config (source=live-config) instead of silently reverting every knob
// to config-file defaults — for the probe-sensing coordinator, the guarded auto-outfit
// coordinator, and the scout-post coordinator. The seam under test is the SHARED
// coordinatorStartConfig: the exact build each
// start handler runs before it persists the new container, asserted through ShowTunableConfig
// — the operator's `tune --operation <op>` view — plus the merge/warning helpers directly.
//
// Test budget: 6 distinct behaviors (sensing re-apply, override precedence, auto-outfit
// re-apply, auto-outfit mode-flag authority, scout-post re-apply, generic safety-warning
// hook) × 2 = 12 max. This file holds 7.

// reapplyKnob returns the ShowTunableConfig row for one knob (fails the test if absent).
func reapplyKnob(t *testing.T, out *TuneShowOutcome, key string) TunableKnobStatus {
	t.Helper()
	for _, k := range out.Knobs {
		if k.Key == key {
			return k
		}
	}
	t.Fatalf("knob %q not present in ShowTunableConfig output", key)
	return TunableKnobStatus{}
}

const autoOutfitContainerType = "AUTO_OUTFIT_COORDINATOR"

// A relaunch of a stopped probe-sensing coordinator re-adopts the persisted live-tunes: the
// launch is identity-only (every knob is tune-only), so this is the pure re-adopt path — the
// credit-moving spend cap and a shaping knob both come back as source=live-config, NOT the
// default, and the new container id wins.
func TestProbeSensingStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "probe_sensing_coordinator-player-OLD"
	const newID = "probe_sensing_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, sensingContainerType, "probe_sensing_coordinator", "STOPPED", map[string]interface{}{
		"container_id":          oldID,
		"probe_cap":             90,     // tune-only — pure carry
		"min_scan_rate_milli":   300,    // tune-only — pure carry
		"capex_reserve_credits": 250000, // credit-moving; a no-flag relaunch must carry it
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	merged, warnings, err := s.coordinatorStartConfig(ctx, playerID, map[string]interface{}{
		"container_id": newID,
	}, probeSensingStartSpec())
	require.NoError(t, err)
	require.Empty(t, warnings, "every sensing knob floors at a positive documented default — none can come up permissive, so no safety warning")
	require.Equal(t, newID, merged["container_id"], "the relaunch always takes the NEW container id")

	seedTuneContainer(t, db, playerID, newID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "sensing", playerID)
	require.NoError(t, err)

	capKnob := reapplyKnob(t, show, "probe_cap")
	require.Equal(t, 90, capKnob.Effective, "a tuned probe cap must survive the relaunch, not reset to default")
	require.Equal(t, "live-config", capKnob.Source)

	floor := reapplyKnob(t, show, "min_scan_rate_milli")
	require.Equal(t, 300, floor.Effective)
	require.Equal(t, "live-config", floor.Source)

	reserve := reapplyKnob(t, show, "capex_reserve_credits")
	require.Equal(t, 250000, reserve.Effective, "the tuned capex reserve must survive a no-flag relaunch")
	require.Equal(t, "live-config", reserve.Source)
}

// The retired launch verbs answer honestly: both legacy engines' start paths return a clear
// "retired" error pointing at the probe-sensing coordinator, and neither persists a container
// — the era-5 unwire keeps the gRPC surface but kills the launch capability.
func TestRetiredLegacyCoordinatorStartVerbs_ReturnRetiredError(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	_, err := s.MarketFreshnessSizerCoordinator(ctx, playerID, 0, false, 0, 0, 0, 0, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retired", "the sizer start verb must say it is retired")
	require.Contains(t, err.Error(), "probe-sensing", "the error must point the operator at the successor")

	_, err = s.FrontierExpansionCoordinator(ctx, playerID, 0, false, 0, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retired", "the frontier start verb must say it is retired")
	require.Contains(t, err.Error(), "probe-sensing", "the error must point the operator at the successor")

	var count int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).Where("player_id = ?", playerID).Count(&count).Error)
	require.Zero(t, count, "a retired verb must persist nothing")
}

// A relaunch of a stopped auto-outfit coordinator re-adopts its persisted credit-moving tune
// (price_ceiling) as source=live-config. Auto-outfit's launch config is identity-only (every
// knob is tune-only), so this is the pure re-adopt path. No safety warning fires: the knob
// floors at a positive default, so it can never come up permissive. (treasury_reserve was a
// second such knob before sp-05glh scrapped it — autooutfit's proportional reserve is gone,
// replaced by the flat common.ImmutableReserveFloor, which has no tune-registry entry to reapply.)
func TestAutoOutfitStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "auto_outfit-player-OLD"
	const newID = "auto_outfit-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, autoOutfitContainerType, "auto_outfit_coordinator", "STOPPED", map[string]interface{}{
		"container_id":  oldID,
		"price_ceiling": 100000,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := map[string]interface{}{"container_id": newID} // live relaunch, identity-only
	merged, warnings, err := s.coordinatorStartConfig(ctx, playerID, base, autoOutfitStartSpec())
	require.NoError(t, err)
	require.Empty(t, warnings, "price_ceiling floors at a positive default — never permissive, no warning")

	seedTuneContainer(t, db, playerID, newID, autoOutfitContainerType, "auto_outfit_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "autooutfit", playerID)
	require.NoError(t, err)

	ceiling := reapplyKnob(t, show, "price_ceiling")
	require.Equal(t, 100000, ceiling.Effective, "the tuned price ceiling must survive the relaunch, not reset to the 500k default")
	require.Equal(t, "live-config", ceiling.Source)
}

// Auto-outfit's launch-time dry-run is an IDENTITY flag: the mode chosen for THIS start is
// authoritative. A live relaunch (no --dry-run) of a coordinator previously started in
// dry-run must CLEAR the persisted auto_outfit_launch_dry_run (go live), while still carrying
// the operator's tunes. This pins the authoritative-key delete-on-absent branch.
func TestAutoOutfitStart_LiveRelaunchClearsPriorLaunchDryRun_TunesSurvive(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "auto_outfit-player-OLD"
	const newID = "auto_outfit-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, autoOutfitContainerType, "auto_outfit_coordinator", "STOPPED", map[string]interface{}{
		"container_id":               oldID,
		"auto_outfit_launch_dry_run": true,
		"price_ceiling":              100000,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := map[string]interface{}{"container_id": newID} // live relaunch: no dry-run flag
	merged, _, err := s.coordinatorStartConfig(ctx, playerID, base, autoOutfitStartSpec())
	require.NoError(t, err)

	_, hasDryRun := merged["auto_outfit_launch_dry_run"]
	require.False(t, hasDryRun, "a live relaunch clears the prior launch-dry-run — the new start's mode wins")

	ceiling, ok := intValue(merged["price_ceiling"])
	require.True(t, ok)
	require.Equal(t, 100000, ceiling, "the operator's tune still carries across the relaunch")
}

// A relaunch of a stopped scout-post coordinator re-adopts its persisted tunes
// (manning_stall_cycles, scout_cross_system_relay_enabled) as source=live-config. The
// scout-post knobs are manning/relay behavior — none credit-moving — so the same re-adopt
// bug applies, just without a safety-critical guard.
func TestScoutPostStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "scout_post_coordinator-player-OLD"
	const newID = "scout_post_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, scoutPostContainerType, "scout_post_coordinator", "STOPPED", map[string]interface{}{
		"container_id":                     oldID,
		"manning_stall_cycles":             30,
		"scout_cross_system_relay_enabled": 1,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := map[string]interface{}{"container_id": newID, "tick_interval_secs": 0}
	merged, warnings, err := s.coordinatorStartConfig(ctx, playerID, base, scoutPostStartSpec())
	require.NoError(t, err)
	require.Empty(t, warnings, "scout-post has no credit-moving knob to warn about")

	seedTuneContainer(t, db, playerID, newID, scoutPostContainerType, "scout_post_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "scoutpost", playerID)
	require.NoError(t, err)

	stall := reapplyKnob(t, show, "manning_stall_cycles")
	require.Equal(t, 30, stall.Effective, "a tuned manning-stall window must survive the relaunch")
	require.Equal(t, "live-config", stall.Source)

	relay := reapplyKnob(t, show, "scout_cross_system_relay_enabled")
	require.Equal(t, 1, relay.Effective, "a tuned relay flag must survive the relaunch")
	require.Equal(t, "live-config", relay.Source)
}

// The generic safety-warning hook warns ONLY when a credit-moving guard resolves permissive
// (effective <= 0): a knob whose documented default is 0 (disabled — frontier's max_probe_price
// shape) warns when the config carries no positive value; a knob whose default is a positive
// safe value never warns, because its effective floors at that default. This is what keeps the
// hook loud for the true overpay exposure and silent (no false alarm) for self-protecting knobs.
func TestCoordinatorStartSafetyWarnings_WarnsOnlyWhenEffectiveResolvesPermissive(t *testing.T) {
	disabledDefault := coordinatorStartSpec{
		safetyKnobs: []coordinatorSafetyKnob{{key: "ceiling", registryDefault: 0, warning: "CEILING UNARMED"}},
	}
	safeDefault := coordinatorStartSpec{
		safetyKnobs: []coordinatorSafetyKnob{{key: "ceiling", registryDefault: 500000, warning: "CEILING UNARMED"}},
	}

	// default 0, config carries nothing positive -> permissive -> warns
	require.Equal(t, []string{"CEILING UNARMED"},
		coordinatorStartSafetyWarnings(map[string]interface{}{}, disabledDefault))
	// default 0, but the config carries a positive value -> armed -> silent
	require.Empty(t, coordinatorStartSafetyWarnings(map[string]interface{}{"ceiling": 60000}, disabledDefault))
	// default positive -> effective floors at the default -> never permissive -> silent
	require.Empty(t, coordinatorStartSafetyWarnings(map[string]interface{}{}, safeDefault))
}
