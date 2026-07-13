package grpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// These tests cover the daemon-side live per-op factory worker cap (sp-ev0n) + its
// restart-resilience (RULINGS #2/#3). The daemon is the single writer of the persisted
// cap: it reads the container config, sets worker_cap, and writes it back. The core is
// exercised over the config MAP (the same shape the daemon reads/writes) so no live DB
// is needed; the GORM Get + UpdateContainerConfig around it is thin plumbing.

// TestMutateFactoryWorkerCapConfig_Set writes the cap into the config and marks it changed.
func TestMutateFactoryWorkerCapConfig_Set(t *testing.T) {
	config := map[string]interface{}{}

	result, changed := mutateFactoryWorkerCapConfig(config, 2)

	require.True(t, changed, "setting a cap where none existed must report changed=true")
	require.Equal(t, 2, result)
	back, ok := intValue(config["worker_cap"])
	require.True(t, ok)
	require.Equal(t, 2, back, "mutation must be written back into the config map for the caller to persist")
}

// TestMutateFactoryWorkerCapConfig_SameValueNoOp: setting the cap to its current value
// reports changed=false so the daemon can skip a redundant DB write.
func TestMutateFactoryWorkerCapConfig_SameValueNoOp(t *testing.T) {
	config := map[string]interface{}{"worker_cap": 4}

	result, changed := mutateFactoryWorkerCapConfig(config, 4)

	require.False(t, changed, "setting the cap to its current value must be a no-op")
	require.Equal(t, 4, result)
}

// TestFactoryWorkerCapFromConfig decodes the live per-op cap from a container config
// JSON string — the read side of the live provider. Absent/non-positive → no override.
func TestFactoryWorkerCapFromConfig(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantCap int
		wantOK  bool
	}{
		{"set", `{"worker_cap":2}`, 2, true},
		{"json-float-roundtrip", `{"worker_cap":3.0}`, 3, true},
		{"empty-config", ``, 0, false},
		{"absent-key", `{"target_good":"FAB_MATS"}`, 0, false},
		{"zero-is-no-override", `{"worker_cap":0}`, 0, false},
		{"negative-is-no-override", `{"worker_cap":-1}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap, ok, err := factoryWorkerCapFromConfig(tc.json)
			require.NoError(t, err)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantCap, cap)
		})
	}
}

func TestFactoryWorkerCapFromConfig_Malformed_Errors(t *testing.T) {
	_, _, err := factoryWorkerCapFromConfig(`{not json`)
	require.Error(t, err, "malformed config JSON must return an error, not silently unbounded")
}

// TestResolveFactoryWorkerCap: the per-op override wins; absent it the global default;
// absent both, 0 = unbounded (a fleet that never set workers_per_chain is unchanged).
func TestResolveFactoryWorkerCap(t *testing.T) {
	require.Equal(t, 2, resolveFactoryWorkerCap(2, 4), "per-op override must win over the global default")
	require.Equal(t, 4, resolveFactoryWorkerCap(0, 4), "absent per-op override falls back to the global default")
	require.Equal(t, 0, resolveFactoryWorkerCap(0, 0), "absent both → unbounded (0)")
	require.Equal(t, 3, resolveFactoryWorkerCap(-1, 3), "a negative override is treated as unset")
}

// TestInjectManufacturingConfig_WorkerCapDefault: the global [manufacturing.siting]
// workers_per_chain is injected (rounded) as the factory default only when set.
func TestInjectManufacturingConfig_WorkerCapDefault(t *testing.T) {
	t.Run("rounds and injects when set", func(t *testing.T) {
		s := &DaemonServer{manufacturingConfig: config.ManufacturingConfig{
			Siting: config.SitingConfig{WorkersPerChain: 3.5},
		}}
		cfg := map[string]interface{}{}
		s.injectManufacturingConfig(cfg)
		require.Equal(t, 4, cfg["factory_worker_cap_default"], "3.5 workers_per_chain rounds to a 4-hull default")
	})

	t.Run("absent when unset → factories stay unbounded", func(t *testing.T) {
		s := &DaemonServer{}
		cfg := map[string]interface{}{}
		s.injectManufacturingConfig(cfg)
		_, present := cfg["factory_worker_cap_default"]
		require.False(t, present, "an unset workers_per_chain must not inject a default (RULINGS #5)")
	})
}

// TestWorkerCapKey_NotAmongReinjectedKeys is the RULINGS #2 guarantee at the config-key
// level: worker_cap (the live per-op override) must NOT be in the set
// resolveManufacturingConfig clears + reinjects from config.yaml, or a live change would
// be wiped on the next daemon restart. The GLOBAL default key, by contrast, MUST be.
func TestWorkerCapKey_NotAmongReinjectedKeys(t *testing.T) {
	require.NotContains(t, manufacturingConfigKeys, "worker_cap",
		"the live per-op worker_cap must survive a restart — it cannot be a config.yaml-reinjected key")
	require.Contains(t, manufacturingConfigKeys, "factory_worker_cap_default",
		"the global default must be re-resolved from config.yaml on every build")
}

// buildFactoryCap rebuilds the goods_factory command from a config map (through the JSON
// round-trip a restart does) and returns its resolved WorkerCap.
func buildFactoryCap(t *testing.T, s *DaemonServer, config map[string]interface{}) int {
	t.Helper()
	const containerID = "goods_factory-FAB_MATS-abcd1234"
	config["container_id"] = containerID
	config["target_good"] = "FAB_MATS"
	config["system_symbol"] = "X1-TEST"

	// RESTART: reload the persisted config through a JSON round-trip (numbers come back
	// as float64), then run the live-config resolve the recovery path runs before build.
	persisted, err := json.Marshal(config)
	require.NoError(t, err)
	var reloaded map[string]interface{}
	require.NoError(t, json.Unmarshal(persisted, &reloaded))

	s.resolveManufacturingConfig(reloaded)

	rebuilt := buildGoodsFactoryCoordinatorCommand(newConfigReader(reloaded), 1, containerID)
	cmd, ok := rebuilt.(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok)
	return cmd.WorkerCap
}

// TestWorkerCap_LiveOverride_SurvivesRestart: a live `goods factory workers` set (the
// persisted worker_cap key) survives a daemon restart intact — NOT wiped by the
// config.yaml re-injection (RULINGS #2).
func TestWorkerCap_LiveOverride_SurvivesRestart(t *testing.T) {
	s := &DaemonServer{} // no global workers_per_chain
	// A live cap of 2 was written into the container config by the RPC.
	got := buildFactoryCap(t, s, map[string]interface{}{"worker_cap": 2})
	require.Equal(t, 2, got, "the live per-op cap must survive the restart rebuild")
}

// TestWorkerCap_GlobalDefault_ReResolvedFromConfigOnRestart: with no per-op override, a
// STALE persisted global default is cleared and replaced by the live config.yaml value.
func TestWorkerCap_GlobalDefault_ReResolvedFromConfigOnRestart(t *testing.T) {
	s := &DaemonServer{manufacturingConfig: config.ManufacturingConfig{
		Siting: config.SitingConfig{WorkersPerChain: 4},
	}}
	// A stale persisted default of 3 must be replaced by the live 4.
	got := buildFactoryCap(t, s, map[string]interface{}{"factory_worker_cap_default": 3})
	require.Equal(t, 4, got, "the stale global default must be re-resolved from live config.yaml")
}

// TestWorkerCap_PerOpOverride_WinsOverGlobalDefault: per-op independence — a factory's
// own live cap overrides the global default (the acceptance's "per-op, not just global").
func TestWorkerCap_PerOpOverride_WinsOverGlobalDefault(t *testing.T) {
	s := &DaemonServer{manufacturingConfig: config.ManufacturingConfig{
		Siting: config.SitingConfig{WorkersPerChain: 4},
	}}
	got := buildFactoryCap(t, s, map[string]interface{}{"worker_cap": 2})
	require.Equal(t, 2, got, "a per-op cap must win over the global default")
}

// TestWorkerCap_Unconfigured_Unbounded: no per-op override and no global default →
// unbounded (0), so a fleet that never set workers_per_chain keeps the pre-sp-ev0n fan-out.
func TestWorkerCap_Unconfigured_Unbounded(t *testing.T) {
	s := &DaemonServer{}
	got := buildFactoryCap(t, s, map[string]interface{}{})
	require.Equal(t, 0, got, "unconfigured factories must remain unbounded (RULINGS #5)")
}
