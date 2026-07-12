package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// newWorkerRebalancerFactoryTestServer builds a factory-only DaemonServer whose live
// (boot-loaded) config.yaml worker-rebalancer knobs are `live`. sp-ts82: the
// worker-rebalancer coordinator resolves its [worker_rebalancer] knobs from THIS live
// config on every command build, so a test can drive the restart-recovery rebuild
// path with a stale persisted launch config and assert the live values win.
func newWorkerRebalancerFactoryTestServer(live config.WorkerRebalancerConfig) *DaemonServer {
	s := &DaemonServer{
		containerSpecs:         make(map[string]ContainerSpec),
		workerRebalancerConfig: live,
	}
	s.registerContainerSpecs()
	return s
}

// workerRebalancerLaunchConfig is a coordinator launch config carrying the mandatory
// coordinator identity keys plus whatever stale worker_rebalancer_* keys a case wants
// to plant.
func workerRebalancerLaunchConfig(stale map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"container_id": "wr-1",
		"agent_symbol": "TESTAGENT",
	}
	for k, v := range stale {
		cfg[k] = v
	}
	return cfg
}

// buildRecoveredWorkerRebalancerCoordinator rebuilds a worker_rebalancer_coordinator
// command through the SAME factory the daemon uses at restart recovery
// (recoverContainer -> buildCommandForType), feeding it a stale persisted launch
// config.
func buildRecoveredWorkerRebalancerCoordinator(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *tradingCmd.RunWorkerRebalancerCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("worker_rebalancer_coordinator", persisted, 7, "wr-1")
	require.NoError(t, err)
	cmd, ok := got.(*tradingCmd.RunWorkerRebalancerCoordinatorCommand)
	require.True(t, ok, "expected *RunWorkerRebalancerCoordinatorCommand, got %T", got)
	return cmd
}

// TestWorkerRebalancerCoordinatorResolvesVacancyMinFromLiveConfig is the mandated
// end-to-end round-trip pin test for sp-nivi: a captain configuring
// vacancy_min_minutes: 15 in config.yaml must produce a built coordinator command
// carrying VacancyMinMinutes == 15 — 15 MINUTES, never nanoseconds-as-minutes. This
// exercises the REAL launch path (live config.yaml -> injectWorkerRebalancerConfig's
// launch-config write -> buildCommandForType's registry read -> the built command),
// the pin-test class the lane lacked (prior unit tests set the field directly,
// bypassing the whole config pipeline).
//
// sp-nivi's actual bug — 900,000,000,000 logged where "15m" was meant — lived
// entirely inside Handle()'s startup-log Sprintf (a time.Duration wrongly passed to
// %d), a component this round trip does not exercise, so this test is expected to
// PASS both before and after the sp-nivi source fix. It stands as a permanent guard
// that the CONFIG PIPELINE itself — the component the bug report's suspect chain
// named as the likely source — carries no ns/minutes confusion, now or in the
// future.
func TestWorkerRebalancerCoordinatorResolvesVacancyMinFromLiveConfig(t *testing.T) {
	cases := []struct {
		name        string
		live        config.WorkerRebalancerConfig
		persisted   map[string]interface{}
		wantVacancy int
	}{
		{
			// A fresh captain configures vacancy_min_minutes: 15; no stale
			// persisted key exists yet (first-ever build).
			name:        "fresh build resolves 15 configured minutes as 15, not nanoseconds",
			live:        config.WorkerRebalancerConfig{VacancyMinMinutes: 15},
			persisted:   workerRebalancerLaunchConfig(nil),
			wantVacancy: 15,
		},
		{
			// Restart recovery: a STALE persisted launch config from a prior boot
			// (with some other value) must be discarded in favor of the current
			// config.yaml's 15 — the sp-ts82 live-config discipline.
			name:        "live 15 overrides a stale persisted vacancy_min on recovery",
			live:        config.WorkerRebalancerConfig{VacancyMinMinutes: 15},
			persisted:   workerRebalancerLaunchConfig(map[string]interface{}{"worker_rebalancer_vacancy_min_minutes": 45}),
			wantVacancy: 15,
		},
		{
			// Unset (0) live config defers to the coordinator's documented
			// default (15m, applied downstream by vacancyMinMinutes()) — the
			// registry itself must carry the zero sentinel through untouched,
			// never a Duration or any other transformed value.
			name:        "unset live config clears a stale persisted vacancy_min to the zero sentinel",
			live:        config.WorkerRebalancerConfig{},
			persisted:   workerRebalancerLaunchConfig(map[string]interface{}{"worker_rebalancer_vacancy_min_minutes": 45}),
			wantVacancy: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newWorkerRebalancerFactoryTestServer(tc.live)
			cmd := buildRecoveredWorkerRebalancerCoordinator(t, s, tc.persisted)
			require.Equal(t, tc.wantVacancy, cmd.VacancyMinMinutes)
			// Guard-sanity: whatever lands in the field must be small — minutes,
			// never nanoseconds. 1440 = 24h in minutes, the sane ceiling Handle()
			// enforces; the registry itself should never even approach it here.
			require.Less(t, cmd.VacancyMinMinutes, 1440, "VacancyMinMinutes must be MINUTES, not nanoseconds-as-minutes")
		})
	}
}

// TestWorkerRebalancerCoordinatorResolvesAllSiblingsFromLiveConfig proves the live
// resolution is ONE shared mechanism across every worker-rebalancer sibling knob, not
// a per-key special case (sp-ts82 pattern): a single live config.yaml drives them all
// on the recovery rebuild, and every stale persisted sibling is discarded.
func TestWorkerRebalancerCoordinatorResolvesAllSiblingsFromLiveConfig(t *testing.T) {
	live := config.WorkerRebalancerConfig{
		TickSeconds:          30,
		VacancyMinMinutes:    15,
		SourceMinIdle:        3,
		FerryCooldownSeconds: 300,
		MaxConcurrentFerries: 4,
		MaxLightsPerSystem:   6,
		EffectSelfcheckTicks: 8,
	}
	// Stale persisted launch config from a PRIOR boot: every sibling holds an
	// outdated value the recovery rebuild must discard in favor of `live`.
	persisted := workerRebalancerLaunchConfig(map[string]interface{}{
		"worker_rebalancer_tick_secs":              60,
		"worker_rebalancer_vacancy_min_minutes":    45,
		"worker_rebalancer_source_min_idle":        1,
		"worker_rebalancer_ferry_cooldown_secs":    600,
		"worker_rebalancer_max_concurrent_ferries": 2,
		"worker_rebalancer_max_lights_per_system":  2,
		"worker_rebalancer_effect_selfcheck_ticks": 3,
	})

	s := newWorkerRebalancerFactoryTestServer(live)
	cmd := buildRecoveredWorkerRebalancerCoordinator(t, s, persisted)

	require.Equal(t, 30, cmd.TickIntervalSecs)
	require.Equal(t, 15, cmd.VacancyMinMinutes)
	require.Equal(t, 3, cmd.SourceMinIdle)
	require.Equal(t, 300, cmd.FerryCooldownSecs)
	require.Equal(t, 4, cmd.MaxConcurrentFerries)
	require.Equal(t, 6, cmd.MaxLightsPerSystem)
	require.Equal(t, 8, cmd.EffectSelfcheckTicks, "the sp-57g9 self-check horizon resolves live and discards the stale persisted copy")
}

// TestWorkerRebalancerCoordinatorResolvesDisabledToggleFromLiveConfig: the on/off
// switch also resolves live (sp-ts82 pattern). A coordinator persisted disabled must
// re-enable when config.yaml drops `enabled: false` and the daemon restarts, and
// symmetrically a live `enabled: false` must take effect on the recovery rebuild.
func TestWorkerRebalancerCoordinatorResolvesDisabledToggleFromLiveConfig(t *testing.T) {
	// live: coordinator ON (Enabled nil -> default true) but a stale key says it
	// was turned off.
	s := newWorkerRebalancerFactoryTestServer(config.WorkerRebalancerConfig{})
	cmd := buildRecoveredWorkerRebalancerCoordinator(t, s, workerRebalancerLaunchConfig(map[string]interface{}{
		"worker_rebalancer_disabled": true,
	}))
	require.True(t, cmd.Enabled, "stale disabled=true must not survive a live re-enable")

	// live: coordinator OFF -> the toggle takes effect on the recovery rebuild too.
	off := false
	s = newWorkerRebalancerFactoryTestServer(config.WorkerRebalancerConfig{Enabled: &off})
	cmd = buildRecoveredWorkerRebalancerCoordinator(t, s, workerRebalancerLaunchConfig(nil))
	require.False(t, cmd.Enabled, "live enabled=false must take effect on the recovery rebuild")
}
