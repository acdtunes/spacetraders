package grpc

// container_ops_tune_parked_sensing_test.go pins the sensing tune registry across
// the parked-probe cutover: the knobs that are LIVE, the ones that are GONE, and
// the one whose encoding is unusual enough to need stating.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sensingBounds is the sensing coordinator's registered knob table.
func sensingBounds(t *testing.T) map[string]TuneBound {
	t.Helper()
	bounds, ok := tunableKnobsByContainerType()[string(container.ContainerTypeProbeSensingCoordinator)]
	require.True(t, ok, "the probe-sensing coordinator must be registered as tunable")
	return bounds
}

// The touring model's knobs are GONE from the registry, which is what makes
// `tune probe_budget 120` fail as an unknown key rather than silently writing a
// value nothing reads.
//
// Silent acceptance is the failure this guards. The retired keys still exist as
// fields on the command (recovery reads them so an old container builds), so a
// tune of one would persist happily and change nothing — an operator would see
// success and get no behaviour, which is worse than an error.
func TestSensingTune_RetiredTouringKnobsAreRejected(t *testing.T) {
	bounds := sensingBounds(t)
	defaults := scoutingCmd.SensingTunableDefaults()

	for _, retired := range []string{
		"probe_budget", "second_probe_threshold", "purchase_cooldown_secs",
		"max_spend_per_cycle", "spend_window_secs", "freshness_target_secs",
		"discovery_declares_per_tick", "depth_floor",
	} {
		require.NotContains(t, bounds, retired,
			"%s belonged to the touring model and must no longer be tunable", retired)
		require.NotContains(t, defaults, retired,
			"%s must be gone from the defaults map too, or the anti-drift test would re-admit it", retired)
	}
}

// The knobs that survived the cutover, plus the new ones. Pinned by name so a
// rename has to be deliberate: these are the operator's whole surface, and a key
// that quietly disappears takes a live lever with it.
func TestSensingTune_LiveKnobSet(t *testing.T) {
	bounds := sensingBounds(t)

	live := []string{
		// carried over from the touring model
		"tick_secs", "wait_low_ms", "wait_high_ms", "pressure_half_life_secs",
		// the parked model's own
		"probe_cap", "expansion_enabled", "target_util_pct", "min_scan_rate_milli",
		"value_clamp_r", "inflight_cap", "capital_multiplier_k_milli",
		"capex_reserve_credits", "quartermaster_cadence_secs",
		// The sensing surge's standing in-flight bound (sp-zvywu). It is a CAP, not a
		// switch: `tune surge_inflight_cap 0` reverts to the documented default, so the
		// surge cannot be turned off through the tune surface at all.
		"surge_inflight_cap",
		// The fill queue's coverage-reserve share. Unlike every other knob in this
		// list, its documented default IS 0 — so this one ships off, not armed.
		"coverage_reserve",
	}
	for _, key := range live {
		require.Contains(t, bounds, key, "%s must be tunable", key)
	}
	require.Len(t, bounds, len(live), "no unlisted knob has crept into the registry")
}

// SensingTunableDefaults must mirror the coordinator's own const block — the
// anti-drift discipline. Every registered bound takes its Default from that map,
// so a const changed without the map would silently advertise a stale default in
// `tune --show` while the loop ran on a different one.
func TestSensingTune_DefaultsMirrorTheRegistry(t *testing.T) {
	bounds := sensingBounds(t)
	defaults := scoutingCmd.SensingTunableDefaults()

	require.Len(t, defaults, len(bounds), "the defaults map and the bounds table describe the same knob set")
	for key, bound := range bounds {
		value, ok := defaults[key]
		require.True(t, ok, "%s is registered but has no documented default", key)
		require.Equal(t, value, bound.Default, "%s advertises a default the map does not carry", key)
		require.GreaterOrEqual(t, bound.Default, bound.Min, "%s default is below its own minimum", key)
		require.LessOrEqual(t, bound.Default, bound.Max, "%s default is above its own maximum", key)
		require.NotEmpty(t, bound.Description, "%s must document what it does", key)
	}
}

// expansion_enabled is bounded [1,3] and its description STATES all three states.
//
// The bound is what makes the encoding discoverable: `tune expansion_enabled 0`
// is the fleet-wide revert verb, so a 0/1 flag could not express "off" at all.
// An operator reading `tune --show` has only the description to tell them which
// state buys what, rather than reading 3 as an out-of-range mistake.
func TestSensingTune_ExpansionEnabledEncodingIsDocumented(t *testing.T) {
	bound := sensingBounds(t)["expansion_enabled"]

	require.Equal(t, 1, bound.Min)
	require.Equal(t, 3, bound.Max)
	require.Equal(t, 1, bound.Default, "expansion ships ON")
	require.Contains(t, bound.Description, "1=buy probes and dispatch charting seeds")
	require.Contains(t, bound.Description, "2=neither")
	require.Contains(t, bound.Description, "3=buy probes, dispatch no charting seed")
}

// The third state is REACHABLE through the tune surface, and the bound still
// refuses everything past it.
//
// 0 is not a fourth state and must never become one: it is the fleet-wide revert
// verb, which is the whole reason the encoding starts at 1.
func TestSensingTune_ExpansionEnabledAcceptsProbesOnlyAndNothingBeyond(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID,
	})
	s := &DaemonServer{containerRepo: repo}
	tune := func(value int) (*TuneOutcome, error) {
		return s.MutateContainerConfigKey(context.Background(), tuneSensingContainerID, "", "expansion_enabled", value, playerID)
	}

	probesOnly, err := tune(3)
	require.NoError(t, err, "state 3 must be settable, or an operator cannot ask for probes without charting")
	require.Equal(t, 3, probesOnly.NewEffective)

	_, err = tune(4)
	require.Error(t, err, "4 names no state and must be refused before anything is written")

	reverted, err := tune(0)
	require.NoError(t, err)
	require.Equal(t, 1, reverted.NewEffective, "0 reverts to the default; it does not express a state")
}

// The two knobs that bind when the scan rotation is CONSTRUCTED, not per tick,
// must say so — every other sensing knob applies next tick, and an operator who
// tunes one of these and watches for a change would otherwise conclude the tune
// was lost.
func TestSensingTune_RebuildLatencyKnobsSayTheyAreRebuildScoped(t *testing.T) {
	bounds := sensingBounds(t)

	for _, key := range []string{"value_clamp_r", "inflight_cap"} {
		require.Contains(t, bounds[key].Description, "rebuild",
			"%s binds at rotation construction and must document that latency", key)
	}
	for _, key := range []string{"probe_cap", "target_util_pct", "min_scan_rate_milli", "capital_multiplier_k_milli"} {
		require.Contains(t, bounds[key].Description, "next tick",
			"%s is live-tunable and must say so", key)
	}
}

// RECOVERY PIN: a container persisted by the OLD core still carries the touring
// model's keys, and it must still BUILD and come up on the new core's defaults.
//
// This is the path a daemon restart takes immediately after the deploy — before
// any relaunch has rewritten the config column — so a build failure here would
// mark the sensing container FAILED on the first boot of the new binary and
// leave the fleet with no sensing engine at all.
func TestSensingRecovery_OldCoreConfigStillBuildsOnDefaults(t *testing.T) {
	s := newFactoryTestServer()

	// Verbatim shape of a container the touring core persisted, including a tuned
	// probe_budget and the era-4 freshness target.
	oldConfig := map[string]interface{}{
		"container_id":                "probe_sensing_coordinator-player-2",
		"depth_floor":                 2_500_000,
		"probe_budget":                120,
		"second_probe_threshold":      14,
		"purchase_cooldown_secs":      10,
		"tick_secs":                   30,
		"wait_low_ms":                 50,
		"wait_high_ms":                1000,
		"freshness_target_secs":       10_800,
		"max_spend_per_cycle":         400_000,
		"spend_window_secs":           1800,
		"discovery_declares_per_tick": 4,
	}

	built, err := s.buildCommandForType("probe_sensing_coordinator",
		jsonRoundTrip(t, oldConfig), 2, "probe_sensing_coordinator-player-2")
	require.NoError(t, err, "an old-core config must never fail recovery")

	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok)

	// The keys the two cores SHARE are carried forward verbatim.
	require.Equal(t, 30, cmd.TickSecs)
	require.Equal(t, 50, cmd.WaitLowMs)
	require.Equal(t, 1000, cmd.WaitHighMs)

	// Every parked-model knob is unset, so the documented defaults govern.
	require.Zero(t, cmd.ProbeCap)
	require.Zero(t, cmd.ExpansionEnabled)
	require.Zero(t, cmd.TargetUtilPct)
	require.Zero(t, cmd.MinScanRateMilli)
	require.Zero(t, cmd.ValueClampR)
	require.Zero(t, cmd.InflightCap)
	require.Zero(t, cmd.CapitalMultiplierKMilli)
	require.Zero(t, cmd.CapexReserveCredits)
	require.Zero(t, cmd.QuartermasterCadence)

	// The retired values ARE read onto the command — and are inert. Reading them
	// is what makes the tolerance visible and pinned, rather than resting on a
	// property of configReader that a future refactor could remove.
	require.Equal(t, 120, cmd.ProbeBudget, "the stale key is parsed, not rejected")
	require.Equal(t, 10_800, cmd.FreshnessTargetSecs)
}

// The new knobs round-trip through the SAME JSON persist→recover boundary a
// daemon restart crosses, so creation and recovery provably rebuild the same
// command (numbers come back as float64 on the recovery path).
func TestSensingRecovery_NewKnobsSurviveTheJSONBoundary(t *testing.T) {
	s := newFactoryTestServer()

	built, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id":               "probe_sensing_coordinator-player-8",
		"probe_cap":                  250,
		"expansion_enabled":          2,
		"target_util_pct":            80,
		"min_scan_rate_milli":        250,
		"value_clamp_r":              8,
		"inflight_cap":               5,
		"capital_multiplier_k_milli": 4,
		"capex_reserve_credits":      750_000,
		"quartermaster_cadence_secs": 1800,
	}), 8, "probe_sensing_coordinator-player-8")
	require.NoError(t, err)

	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok)

	require.Equal(t, 250, cmd.ProbeCap)
	require.Equal(t, 2, cmd.ExpansionEnabled, "the off-switch survives the boundary as 2, not as a lost bool")
	require.Equal(t, 80, cmd.TargetUtilPct)
	require.Equal(t, 250, cmd.MinScanRateMilli)
	require.Equal(t, 8, cmd.ValueClampR)
	require.Equal(t, 5, cmd.InflightCap)
	require.Equal(t, 4, cmd.CapitalMultiplierKMilli)
	require.Equal(t, 750_000, cmd.CapexReserveCredits)
	require.Equal(t, 1800, cmd.QuartermasterCadence)
}

// Every registered knob must be REACHABLE through the mutation path — a bound
// with no matching config key would show in `tune --show` and refuse every write.
func TestSensingTune_EveryRegisteredKnobIsAcceptedAtItsDefault(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	const containerID = "probe_sensing_coordinator-player-tune"
	seedTuneContainer(t, db, playerID, containerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING",
		map[string]interface{}{"container_id": containerID})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	for key, bound := range sensingBounds(t) {
		t.Run(key, func(t *testing.T) {
			_, err := s.MutateContainerConfigKey(ctx, containerID, "", key, bound.Default, playerID)
			require.NoError(t, err, "%s is registered but its own default is refused", key)
		})
	}

	// And the persisted column is still valid JSON the recovery path can rebuild.
	var reloaded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(containerConfigJSON(t, repo, containerID, playerID)), &reloaded))
	require.Contains(t, reloaded, "container_id")
}
