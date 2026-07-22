package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the sp-ve3q fix: relaunching a stopped frontier coordinator via
// `frontier start` must RE-ADOPT the last persisted live-tuned config (source=live-config)
// instead of silently reverting every knob to config-file defaults. The seam under test is
// frontierStartConfig: the same build the start handler runs before it persists the new
// container, so the assertion is made through ShowTunableConfig — the `tune --operation
// frontier` view an operator checks. sp-tlekc cut the frontier surface to four knobs and
// hardcoded the probe price ceiling to an immutable const, so there is no longer a safety-knob
// warning to raise (an un-tunable const can never come up "unarmed").

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

// freshFrontierStartBase mirrors the config map the start handler builds from the CLI flags:
// the new container id + mode, every numeric knob left at 0 (= use the default), i.e. an
// operator who relaunches with no flags. (sp-tlekc: the two rate-governor flags are gone.)
func freshFrontierStartBase(newID string) map[string]interface{} {
	return map[string]interface{}{
		"container_id":       newID,
		"dry_run":            false,
		"tick_interval_secs": 0,
		"max_probe_fleet":    0,
		"expansion_max_hops": 0,
	}
}

// A relaunch of a stopped coordinator re-adopts the persisted live-tunes: every tuned knob comes
// back as source=live-config, NOT the default. This is the exact P1 the bead reports, now over the
// sp-tlekc four-knob surface (max_probe_fleet, reach_mode, discover_scan_balance).
func TestFrontierStart_RelaunchReAppliesPersistedTunes(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "frontier_expansion_coordinator-player-OLD"
	const newID = "frontier_expansion_coordinator-player-NEW"
	// The previously-stopped coordinator's persisted config carries the operator's tunes
	// (JSON round-trips numbers to float64, exactly as the DB does).
	seedTuneContainer(t, db, playerID, oldID, frontierContainerType, "frontier_expansion_coordinator", "STOPPED", map[string]interface{}{
		"container_id":          oldID,
		"max_probe_fleet":       110,
		"reach_mode":            1,
		"discover_scan_balance": 40,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	merged, warnings, err := s.frontierStartConfig(ctx, playerID, freshFrontierStartBase(newID))
	require.NoError(t, err)

	// The start handler persists the new RUNNING container with the resolved config; do the same,
	// then observe through the operator's `tune --operation frontier` view.
	seedTuneContainer(t, db, playerID, newID, frontierContainerType, "frontier_expansion_coordinator", "RUNNING", merged)
	show, err := s.ShowTunableConfig(ctx, "", "frontier", playerID)
	require.NoError(t, err)

	fleet := frontierKnob(t, show, "max_probe_fleet")
	require.Equal(t, 110, fleet.Effective, "the tuned fleet cap must survive the relaunch, not reset to the default")
	require.Equal(t, "live-config", fleet.Source, "a re-adopted tune reads as live-config, not default")

	reach := frontierKnob(t, show, "reach_mode")
	require.Equal(t, 1, reach.Effective)
	require.Equal(t, "live-config", reach.Source)

	balance := frontierKnob(t, show, "discover_scan_balance")
	require.Equal(t, 40, balance.Effective)
	require.Equal(t, "live-config", balance.Source)

	require.Empty(t, warnings, "the four-knob surface carries no 0=disabled safety knob → no start warning")
}

// With NO prior coordinator the resolved config is byte-identical to the config-file-default start
// (the constraint: a fresh coordinator comes up exactly as today), and no safety warning is raised
// (sp-tlekc: the price ceiling is an immutable const, never an "unarmed" knob).
func TestFrontierStart_NoPriorCoordinator_ByteIdenticalNoWarning(t *testing.T) {
	_, repo, playerID := tuneTestDB(t)
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	base := freshFrontierStartBase("frontier_expansion_coordinator-player-FRESH")
	want := freshFrontierStartBase("frontier_expansion_coordinator-player-FRESH")

	merged, warnings, err := s.frontierStartConfig(ctx, playerID, base)
	require.NoError(t, err)
	require.Equal(t, want, merged, "a fresh start with no prior tunes is byte-identical to config-file defaults")
	require.Empty(t, warnings, "no 0=disabled safety knob remains → a fresh start is silent")
}

// A knob the operator sets EXPLICITLY on the relaunch (a positive CLI flag) wins over the
// carried-forward value, while a non-flag tune (discover_scan_balance) still survives — the merge
// preserves tunes without ignoring an explicit new intent.
func TestFrontierStart_ExplicitStartFlagOverridesCarriedConfig_NonFlagTunesSurvive(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const oldID = "frontier_expansion_coordinator-player-OLD"
	const newID = "frontier_expansion_coordinator-player-NEW"
	seedTuneContainer(t, db, playerID, oldID, frontierContainerType, "frontier_expansion_coordinator", "STOPPED", map[string]interface{}{
		"container_id":          oldID,
		"max_probe_fleet":       110,
		"discover_scan_balance": 40,
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

	balance, ok := intValue(merged["discover_scan_balance"])
	require.True(t, ok)
	require.Equal(t, 40, balance, "a non-flag tune is still carried forward across the relaunch")

	require.Equal(t, newID, merged["container_id"], "the relaunch always takes the NEW container id, never the prior one")
}
