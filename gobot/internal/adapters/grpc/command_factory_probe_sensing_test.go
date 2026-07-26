package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// The probe-sensing coordinator replaces the market-freshness sizer + frontier
// expansion pair as the fleet's ONE sensing engine. These tests pin both sides
// of the swap at the registry: the new type builds (creation and restart
// recovery share buildCommandForType), and the two legacy types are REFUSED —
// a still-RUNNING legacy container fails closed at restart recovery ("unknown
// command type"), which is the intended unwire, not an accident.

// TestRegistry_BuildsProbeSensingCoordinator drives the launch config through
// the SAME JSON persist→recover boundary a daemon restart crosses, so creation
// and recovery provably rebuild the identical command. The whitelist rides as
// a [sensing] config.yaml CSV (strings are config-injected, not tunable — the
// int-only tune mechanism cannot carry them).
func TestRegistry_BuildsProbeSensingCoordinator(t *testing.T) {
	s := newFactoryTestServer()
	s.sensingConfig = config.SensingConfig{GoodsWhitelist: "MEDICINE, MICROPROCESSORS,SHIP_PLATING"}

	launch := map[string]interface{}{
		"container_id":                "probe_sensing_coordinator-player-9-boot",
		"depth_floor":                 2_500_000,
		"probe_budget":                120,
		"second_probe_threshold":      14,
		"purchase_cooldown_secs":      10,
		"tick_secs":                   30,
		"wait_low_ms":                 50,
		"wait_high_ms":                1000,
		"freshness_target_secs":       3600,
		"max_spend_per_cycle":         400_000,
		"spend_window_secs":           1800,
		"discovery_declares_per_tick": 4,
	}

	built, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, launch), 9, "probe_sensing_coordinator-player-9-boot")
	require.NoError(t, err)
	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok, "expected *RunProbeSensingCoordinatorCommand, got %T", built)

	require.Equal(t, 9, cmd.PlayerID.Value())
	require.Equal(t, "probe_sensing_coordinator-player-9-boot", cmd.ContainerID)
	require.Equal(t, []string{"MEDICINE", "MICROPROCESSORS", "SHIP_PLATING"}, cmd.GoodsWhitelist,
		"the config-injected CSV whitelist must split and trim into the command's goods list")
	require.Equal(t, int64(2_500_000), cmd.DepthFloor)
	require.Equal(t, 120, cmd.ProbeBudget)
	require.Equal(t, 14, cmd.SecondProbeThreshold)
	require.Equal(t, 10, cmd.PurchaseCooldownSecs)
	require.Equal(t, 30, cmd.TickSecs)
	require.Equal(t, 50, cmd.WaitLowMs)
	require.Equal(t, 1000, cmd.WaitHighMs)
	require.Equal(t, 3600, cmd.FreshnessTargetSecs)
	require.Equal(t, 400_000, cmd.MaxSpendPerCycle)
	require.Equal(t, 1800, cmd.SpendWindowSecs)
	require.Equal(t, 4, cmd.DiscoveryDeclaresPerTick)

	// Standing reconcile loop: the handler owns its loop inside one Handle(),
	// so the runner must be allowed to pace it — never a one-iteration wrap.
	spec, registered := s.containerSpecs["probe_sensing_coordinator"]
	require.True(t, registered)
	require.False(t, spec.CoordinatorOwnsIterations,
		"probe_sensing_coordinator is a standing reconcile loop, not a CoordinatorOwnsIterations type")
}

// TestRegistry_BuildsProbeSensingCoordinator_DefaultsPassThroughAsZero pins the
// all-defaults boot-standing launch: only the container id is persisted, every
// knob rides as its zero value so the coordinator's documented defaults govern
// (RULINGS #5) — creation and recovery can never disagree about a default.
func TestRegistry_BuildsProbeSensingCoordinator_DefaultsPassThroughAsZero(t *testing.T) {
	s := newFactoryTestServer()

	built, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id": "probe_sensing_coordinator-player-3-boot",
	}), 3, "probe_sensing_coordinator-player-3-boot")
	require.NoError(t, err)
	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok, "expected *RunProbeSensingCoordinatorCommand, got %T", built)

	require.Empty(t, cmd.GoodsWhitelist, "no whitelist key → the coordinator's era-goods default applies")
	require.Zero(t, cmd.DepthFloor)
	require.Zero(t, cmd.ProbeBudget)
	require.Zero(t, cmd.SecondProbeThreshold)
	require.Zero(t, cmd.PurchaseCooldownSecs)
	require.Zero(t, cmd.TickSecs)
	require.Zero(t, cmd.WaitLowMs)
	require.Zero(t, cmd.WaitHighMs)
	require.Zero(t, cmd.DiscoveryDeclaresPerTick)
}

// TestRegistry_RefusesRetiredLegacyCoordinatorTypes is the unwire tripwire: the
// frontier expansion coordinator and the market-freshness sizer are no longer
// buildable. Restart recovery funnels every persisted container through
// buildCommandForType, so this is exactly the fail-closed path a still-RUNNING
// legacy container takes after the deploy — marked FAILED, work abandoned,
// probe-sensing owns the scope. Source files stay in the tree pending era-5
// proof; only the wiring is gone.
func TestRegistry_RefusesRetiredLegacyCoordinatorTypes(t *testing.T) {
	s := newFactoryTestServer()

	for _, legacy := range []string{"frontier_expansion_coordinator", "market_freshness_sizer_coordinator"} {
		t.Run(legacy, func(t *testing.T) {
			built, err := s.buildCommandForType(legacy, map[string]interface{}{
				"container_id": legacy + "-player-9",
			}, 9, legacy+"-player-9")
			require.Error(t, err, "the registry must refuse the retired %s", legacy)
			require.Contains(t, err.Error(), "unknown command type",
				"a retired type fails closed at restart recovery with the registry's unknown-type error")
			require.Nil(t, built)
		})
	}
}

// TestSensingConfigYaml_WhitelistInjectedAtBuild pins the [sensing] config.yaml
// injection: the goods whitelist is a string, unreachable through the int-only
// tune mechanism, so config.yaml is its ONE operator surface — the boot-loaded
// CSV must reach the built command on every build (creation and restart
// recovery both funnel through buildCommandForType).
func TestSensingConfigYaml_WhitelistInjectedAtBuild(t *testing.T) {
	s := newFactoryTestServer()
	s.sensingConfig = config.SensingConfig{GoodsWhitelist: "MEDICINE, URANITE,SHIP_PLATING"}

	built, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id": "probe_sensing_coordinator-player-4-boot",
	}), 4, "probe_sensing_coordinator-player-4-boot")
	require.NoError(t, err)
	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok, "expected *RunProbeSensingCoordinatorCommand, got %T", built)

	require.Equal(t, []string{"MEDICINE", "URANITE", "SHIP_PLATING"}, cmd.GoodsWhitelist,
		"the [sensing] goods_whitelist CSV must be injected at container construction")
}

// TestSensingConfigYaml_UnsetYieldsEraDefault_AndClearsStaleCopy pins the sp-ts82
// live-config discipline for the whitelist: config.yaml is the single source of
// truth, so with the section unset the built command carries NO whitelist (the
// coordinator's era-goods default governs) — even when a stale copy is still
// sitting in the persisted container config from a prior boot.
func TestSensingConfigYaml_UnsetYieldsEraDefault_AndClearsStaleCopy(t *testing.T) {
	s := newFactoryTestServer() // zero sensingConfig — the captain set nothing

	built, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id":    "probe_sensing_coordinator-player-5-boot",
		"goods_whitelist": "STALE_ERA4_GOOD",
	}), 5, "probe_sensing_coordinator-player-5-boot")
	require.NoError(t, err)
	cmd, ok := built.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok)

	require.Empty(t, cmd.GoodsWhitelist,
		"an unset [sensing] section must yield the era-goods default — a stale persisted copy can never shadow config.yaml")
}

// recordingPressureClient is a driven-port fake for the API client that records
// SetLimiterPressureHalfLife calls. The embedded interface satisfies the
// DaemonServer's apiClient field; only the setter is ever invoked on this path.
type recordingPressureClient struct {
	domainPorts.APIClient
	halfLives []time.Duration
}

func (c *recordingPressureClient) SetLimiterPressureHalfLife(d time.Duration) {
	c.halfLives = append(c.halfLives, d)
}

// TestSensingBuild_AppliesTunedPressureHalfLifeToClient pins the T4-binding
// "a tune survives a bounce" path: a persisted/tuned pressure_half_life_secs in
// the sensing container's config must be applied to the API client's
// limiter-pressure EWMA on every rebuild (creation and restart recovery), while
// an absent key leaves the boot-time config.yaml value untouched and the branch
// stays scoped to the sensing type only.
func TestSensingBuild_AppliesTunedPressureHalfLifeToClient(t *testing.T) {
	s := newFactoryTestServer()
	fake := &recordingPressureClient{}
	s.apiClient = fake

	_, err := s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id":            "probe_sensing_coordinator-player-9-boot",
		"pressure_half_life_secs": 45,
	}), 9, "probe_sensing_coordinator-player-9-boot")
	require.NoError(t, err)
	require.Equal(t, []time.Duration{45 * time.Second}, fake.halfLives,
		"a tuned half-life must reach the client's pressure EWMA at rebuild")

	// Absent/zero → no application: the boot-time config.yaml wiring governs.
	fake.halfLives = nil
	_, err = s.buildCommandForType("probe_sensing_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id": "probe_sensing_coordinator-player-9-boot",
	}), 9, "probe_sensing_coordinator-player-9-boot")
	require.NoError(t, err)
	require.Empty(t, fake.halfLives, "no tuned value → the client's boot-time half-life is left untouched")

	// Scoped to the sensing type: another coordinator's config carrying the key
	// must never touch the process-global client state.
	_, err = s.buildCommandForType("scout_post_coordinator", jsonRoundTrip(t, map[string]interface{}{
		"container_id":            "scout-post-x",
		"pressure_half_life_secs": 45,
	}), 9, "scout-post-x")
	require.NoError(t, err)
	require.Empty(t, fake.halfLives, "the half-life application is sensing-scoped, never another type's side effect")
}

// TestRecovery_RetiredLegacySensingContainersFailClosedWithoutLossAlarm is the
// one-time post-deploy boot: still-RUNNING sizer/frontier rows persisted by the
// pre-retirement daemon must end TERMINATED (never running again — fail closed),
// via the deliberate retired-command-type skip rather than a spurious
// recovery_failed loss — the loud container.lost signal must stay clean for
// real losses (the sp-hoj8u retirement discipline).
func TestRecovery_RetiredLegacySensingContainersFailClosedWithoutLossAlarm(t *testing.T) {
	rec := &syncRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "legacy-sizer-1", "market_freshness_sizer_coordinator",
		"MARKET_FRESHNESS_SIZER_COORDINATOR", `{"container_id":"legacy-sizer-1"}`, playerID, nil)
	insertRunningContainer(t, db, "legacy-frontier-1", "frontier_expansion_coordinator",
		"FRONTIER_EXPANSION_COORDINATOR", `{"container_id":"legacy-frontier-1"}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "legacy-sizer-1", "FAILED", "retired_command_type")
	requireContainerState(t, db, "legacy-frontier-1", "FAILED", "retired_command_type")
	require.Nil(t, s.registeredRunner("legacy-sizer-1"), "a retired container must never come back as a live runner")
	require.Nil(t, s.registeredRunner("legacy-frontier-1"), "a retired container must never come back as a live runner")
	require.Empty(t, rec.lost(), "a deliberate retirement is not a loss — no container.lost alarm")
}
