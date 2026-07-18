package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the sp-rsgc generalization of the sp-ve3q frontier re-apply: relaunching
// a previously-stopped TUNABLE coordinator via its `start` verb must RE-ADOPT the last
// persisted live-tuned config (source=live-config) instead of silently reverting every knob
// to config-file defaults — the same bug class the frontier P1 exposed (sp-ve3q), now closed
// for the market-freshness sizer, the guarded auto-outfit coordinator, and the scout-post
// coordinator. The seam under test is the SHARED coordinatorStartConfig: the exact build each
// start handler runs before it persists the new container, asserted through ShowTunableConfig
// — the operator's `tune --operation <op>` view — plus the merge/warning helpers directly.
//
// Test budget: 6 distinct behaviors (sizer re-apply, sizer override precedence, auto-outfit
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

// freshSizerStartBase mirrors the config map MarketFreshnessSizerCoordinator builds from the
// CLI flags: the new container id + mode, every numeric knob at 0 (= use the default) — an
// operator relaunching with no flags.
func freshSizerStartBase(newID string) map[string]interface{} {
	return map[string]interface{}{
		"container_id":           newID,
		"tick_interval_secs":     0,
		"dry_run":                false,
		"sla_seconds":            0,
		"max_probes_per_system":  0,
		"max_probe_fleet":        0,
		"max_spend_per_cycle":    0,
		"purchase_cooldown_secs": 0,
	}
}

// A relaunch of a stopped freshness sizer re-adopts the persisted live-tunes: a tune-only
// knob (spend_window_secs) and the credit-moving spend cap both come back as
// source=live-config, NOT the default.
func TestFreshsizerStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "market_freshness_sizer_coordinator-player-OLD"
	const newID = "market_freshness_sizer_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, sizerContainerType, "market_freshness_sizer_coordinator", "STOPPED", map[string]interface{}{
		"container_id":        oldID,
		"spend_window_secs":   1800,   // tune-only (not a start-flag) — pure carry
		"target_percentile":   95,     // tune-only — pure carry
		"max_spend_per_cycle": 250000, // credit-moving; a no-flag relaunch must carry it
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	merged, warnings, err := s.coordinatorStartConfig(ctx, playerID, freshSizerStartBase(newID), marketFreshnessSizerStartSpec())
	require.NoError(t, err)
	require.Empty(t, warnings, "the sizer spend cap floors at a positive default — it can never come up uncapped, so no safety warning")

	seedTuneContainer(t, db, playerID, newID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "freshsizer", playerID)
	require.NoError(t, err)

	window := reapplyKnob(t, show, "spend_window_secs")
	require.Equal(t, 1800, window.Effective, "a tuned window must survive the relaunch, not reset to default")
	require.Equal(t, "live-config", window.Source)

	pct := reapplyKnob(t, show, "target_percentile")
	require.Equal(t, 95, pct.Effective)
	require.Equal(t, "live-config", pct.Source)

	spend := reapplyKnob(t, show, "max_spend_per_cycle")
	require.Equal(t, 250000, spend.Effective, "the tuned spend cap must survive a no-flag relaunch")
	require.Equal(t, "live-config", spend.Source)
}

// An explicit start flag (positive) on the relaunch overrides the carried-forward value,
// while a non-flag tune still survives — the merge preserves tunes without ignoring explicit
// new intent (mirrors the frontier precedence test).
func TestFreshsizerStart_ExplicitFlagOverridesCarried_NonFlagTuneSurvives(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "market_freshness_sizer_coordinator-player-OLD"
	const newID = "market_freshness_sizer_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, sizerContainerType, "market_freshness_sizer_coordinator", "STOPPED", map[string]interface{}{
		"container_id":        oldID,
		"max_spend_per_cycle": 250000,
		"spend_window_secs":   1800,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := freshSizerStartBase(newID)
	base["max_spend_per_cycle"] = 400000 // operator explicitly re-caps spend on this relaunch

	merged, _, err := s.coordinatorStartConfig(ctx, playerID, base, marketFreshnessSizerStartSpec())
	require.NoError(t, err)

	spend, ok := intValue(merged["max_spend_per_cycle"])
	require.True(t, ok)
	require.Equal(t, 400000, spend, "an explicit start flag (>0) overrides the carried-forward value")

	window, ok := intValue(merged["spend_window_secs"])
	require.True(t, ok)
	require.Equal(t, 1800, window, "a non-flag tune is still carried forward across the relaunch")

	require.Equal(t, newID, merged["container_id"], "the relaunch always takes the NEW container id")
}

// A relaunch of a stopped auto-outfit coordinator re-adopts its persisted credit-moving tunes
// (price_ceiling, treasury_reserve) as source=live-config. Auto-outfit's launch config is
// identity-only (every knob is tune-only), so this is the pure re-adopt path. No safety
// warning fires: both knobs floor at positive defaults, so neither can come up permissive.
func TestAutoOutfitStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "auto_outfit-player-OLD"
	const newID = "auto_outfit-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, autoOutfitContainerType, "auto_outfit_coordinator", "STOPPED", map[string]interface{}{
		"container_id":     oldID,
		"price_ceiling":    100000,
		"treasury_reserve": 80000,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := map[string]interface{}{"container_id": newID} // live relaunch, identity-only
	merged, warnings, err := s.coordinatorStartConfig(ctx, playerID, base, autoOutfitStartSpec())
	require.NoError(t, err)
	require.Empty(t, warnings, "price_ceiling/treasury_reserve floor at positive defaults — never permissive, no warning")

	seedTuneContainer(t, db, playerID, newID, autoOutfitContainerType, "auto_outfit_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "autooutfit", playerID)
	require.NoError(t, err)

	ceiling := reapplyKnob(t, show, "price_ceiling")
	require.Equal(t, 100000, ceiling.Effective, "the tuned price ceiling must survive the relaunch, not reset to the 500k default")
	require.Equal(t, "live-config", ceiling.Source)

	reserve := reapplyKnob(t, show, "treasury_reserve")
	require.Equal(t, 80000, reserve.Effective, "the tuned treasury reserve must survive the relaunch")
	require.Equal(t, "live-config", reserve.Source)
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
