package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the sp-ve3q fix: relaunching a stopped frontier coordinator via
// `frontier start` must RE-ADOPT the last persisted live-tuned config (source=live-config)
// instead of silently reverting every knob to config-file defaults — including the
// safety-critical max_probe_price overpay ceiling (sp-3u5d; 0 = disabled = buy at any
// price). The seam under test is frontierStartConfig: the same build the start handler
// runs before it persists the new container, so the assertion is made through
// ShowTunableConfig — the `tune --operation frontier` view an operator checks.
//
// Test budget: 4 distinct behaviors (re-apply, byte-identical fresh start, safety warning,
// merge precedence) × 2 = 8 max. This file holds 3.

// frontierKnob returns the ShowTunableConfig row for one knob (fails the test if absent).
func frontierKnob(t *testing.T, out *TuneShowOutcome, key string) TunableKnobStatus {
	t.Helper()
	for _, k := range out.Knobs {
		if k.Key == key {
			return k
		}
	}
	t.Fatalf("knob %q not present in ShowTunableConfig output", key)
	return TunableKnobStatus{}
}

// freshFrontierStartBase mirrors the config map the start handler builds from the CLI
// flags: the new container id + mode, every numeric knob left at 0 (= use the default),
// i.e. an operator who relaunches with no flags.
func freshFrontierStartBase(newID string) map[string]interface{} {
	return map[string]interface{}{
		"container_id":           newID,
		"dry_run":                false,
		"tick_interval_secs":     0,
		"max_probe_fleet":        0,
		"max_spend_per_cycle":    0,
		"purchase_cooldown_secs": 0,
		"expansion_max_hops":     0,
	}
}

// A relaunch of a stopped coordinator re-adopts the persisted live-tunes: the
// safety-critical max_probe_price ceiling (and every other tuned knob) comes back as
// source=live-config, NOT the default. This is the exact P1 the bead reports.
func TestFrontierStart_RelaunchReAppliesPersistedTunes_MaxProbePriceSurvives(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "frontier_expansion_coordinator-player-OLD"
	const newID = "frontier_expansion_coordinator-player-NEW"
	// The previously-stopped coordinator's persisted config carries the operator's tunes
	// (JSON round-trips numbers to float64, exactly as the DB does).
	seedTuneContainer(t, db, playerID, oldID, frontierContainerType, "frontier_expansion_coordinator", "STOPPED", map[string]interface{}{
		"container_id":    oldID,
		"max_probe_price": 60000,
		"max_probe_fleet": 110,
		"discovery_share": 40,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	merged, warnings, err := s.frontierStartConfig(ctx, playerID, freshFrontierStartBase(newID))
	require.NoError(t, err)

	// The start handler persists the new RUNNING container with the resolved config; do
	// the same, then observe through the operator's `tune --operation frontier` view.
	seedTuneContainer(t, db, playerID, newID, frontierContainerType, "frontier_expansion_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "frontier", playerID)
	require.NoError(t, err)

	ceiling := frontierKnob(t, show, "max_probe_price")
	require.Equal(t, 60000, ceiling.Effective, "the tuned overpay ceiling must survive the relaunch, not reset to 0")
	require.Equal(t, "live-config", ceiling.Source, "a re-adopted tune reads as live-config, not default")

	fleet := frontierKnob(t, show, "max_probe_fleet")
	require.Equal(t, 110, fleet.Effective)
	require.Equal(t, "live-config", fleet.Source)

	share := frontierKnob(t, show, "discovery_share")
	require.Equal(t, 40, share.Effective)
	require.Equal(t, "live-config", share.Source)

	require.Empty(t, warnings, "an armed ceiling raises no safety warning")
}

// With NO prior coordinator the resolved config is byte-identical to the config-file-default
// start (the constraint: a fresh coordinator comes up exactly as today), AND the start path
// loudly warns that the safety-critical overpay ceiling came up UNARMED (max_probe_price=0).
func TestFrontierStart_NoPriorCoordinator_ByteIdenticalAndWarnsCeilingUnarmed(t *testing.T) {
	_, repo, playerID := tuneTestDB(t)
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := freshFrontierStartBase("frontier_expansion_coordinator-player-FRESH")
	want := freshFrontierStartBase("frontier_expansion_coordinator-player-FRESH")

	merged, warnings, err := s.frontierStartConfig(ctx, playerID, base)
	require.NoError(t, err)
	require.Equal(t, want, merged, "a fresh start with no prior tunes is byte-identical to config-file defaults")

	require.Len(t, warnings, 1, "a start that comes up with the ceiling disabled must warn, not stay silent")
	require.Contains(t, warnings[0], "max_probe_price", "the warning must name the safety knob that is unarmed")
}

// A knob the operator sets EXPLICITLY on the relaunch (a positive CLI flag) wins over the
// carried-forward value, while a non-flag tune (max_probe_price) still survives — the merge
// preserves tunes without ignoring an explicit new intent.
func TestFrontierStart_ExplicitStartFlagOverridesCarriedConfig_NonFlagTunesSurvive(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "frontier_expansion_coordinator-player-OLD"
	const newID = "frontier_expansion_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, frontierContainerType, "frontier_expansion_coordinator", "STOPPED", map[string]interface{}{
		"container_id":    oldID,
		"max_probe_fleet": 110,
		"max_probe_price": 60000,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := freshFrontierStartBase(newID)
	base["max_probe_fleet"] = 50 // operator explicitly re-sizes the fleet on this relaunch

	merged, _, err := s.frontierStartConfig(ctx, playerID, base)
	require.NoError(t, err)

	fleet, ok := intValue(merged["max_probe_fleet"])
	require.True(t, ok)
	require.Equal(t, 50, fleet, "an explicit start flag (>0) overrides the carried-forward value")

	ceiling, ok := intValue(merged["max_probe_price"])
	require.True(t, ok)
	require.Equal(t, 60000, ceiling, "a non-flag tune is still carried forward across the relaunch")

	require.Equal(t, newID, merged["container_id"], "the relaunch always takes the NEW container id, never the prior one")
}
