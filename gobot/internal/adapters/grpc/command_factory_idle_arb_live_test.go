package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// newIdleArbFactoryTestServer builds a factory-only DaemonServer whose live
// (boot-loaded) config.yaml idle-arb knobs are `live`. sp-ts82: the contract
// coordinator resolves its idle-arb knobs from THIS live config on every command
// build, so a test can drive the restart-recovery rebuild path with a stale
// persisted launch config and assert the live values win.
func newIdleArbFactoryTestServer(live config.IdleArbSettings) *DaemonServer {
	s := &DaemonServer{
		containerSpecs: make(map[string]ContainerSpec),
		contractConfig: config.ContractConfig{IdleArb: live},
	}
	s.registerContainerSpecs()
	return s
}

// idleArbLaunchConfig is a coordinator launch config carrying the two mandatory
// coordinator keys plus whatever stale idle-arb keys a case wants to plant.
func idleArbLaunchConfig(stale map[string]interface{}) map[string]interface{} {
	cfg := map[string]interface{}{
		"ship_symbols": []interface{}{},
		"container_id": "fleet-1",
	}
	for k, v := range stale {
		cfg[k] = v
	}
	return cfg
}

// buildRecoveredCoordinator rebuilds a contract_fleet_coordinator command through
// the SAME factory the daemon uses at restart recovery (recoverContainer ->
// buildCommandForType), feeding it a stale persisted launch config.
func buildRecoveredCoordinator(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *contractCmd.RunFleetCoordinatorCommand {
	t.Helper()
	got, err := s.buildCommandForType("contract_fleet_coordinator", persisted, 7, "fleet-1")
	require.NoError(t, err)
	cmd, ok := got.(*contractCmd.RunFleetCoordinatorCommand)
	require.True(t, ok, "expected *RunFleetCoordinatorCommand, got %T", got)
	return cmd
}

// TestContractCoordinatorResolvesLeashFromLiveConfig locks sp-ts82 with the
// money-guard leash as the representative numeric sibling: the coordinator
// resolves its idle-arb leash from the LIVE config.yaml every time the command is
// built — crucially including the restart RECOVERY rebuild, which re-adopts a
// stale persisted launch config. Before the fix, recovery read the persisted
// idle_arb_leash_radius verbatim, so a config.yaml retune + daemon restart (the
// documented path, sp-uohe) silently no-op'd on the already-running coordinator —
// the sp-nw9v incident.
func TestContractCoordinatorResolvesLeashFromLiveConfig(t *testing.T) {
	cases := []struct {
		name      string
		live      config.IdleArbSettings
		persisted map[string]interface{}
		wantLeash float64
	}{
		{
			// THE INCIDENT (sp-nw9v): the coordinator was persisted with NO leash
			// key (it ran WithDefaults -> 80); the harbormaster retuned leash -> 150
			// in config.yaml and restarted. Recovery MUST rebuild with the live 150,
			// not re-adopt the key-less stale config (-> 0 -> default 80).
			name:      "incident: retune reaches a key-less recovered coordinator",
			live:      config.IdleArbSettings{LeashRadius: 150},
			persisted: idleArbLaunchConfig(nil),
			wantLeash: 150,
		},
		{
			// Retune 80 -> 150: the live value overrides the stale persisted copy.
			name:      "live overrides stale persisted leash",
			live:      config.IdleArbSettings{LeashRadius: 150},
			persisted: idleArbLaunchConfig(map[string]interface{}{"idle_arb_leash_radius": 80}),
			wantLeash: 150,
		},
		{
			// config.yaml section absent -> the stale persisted key is a DEAD key,
			// cleared so the command carries 0 and the contract package's
			// WithDefaults applies the CLOSED default (80) downstream. Proves a
			// stale persisted copy can never shadow an absent live section.
			name:      "absent live section clears stale persisted leash to the sentinel",
			live:      config.IdleArbSettings{},
			persisted: idleArbLaunchConfig(map[string]interface{}{"idle_arb_leash_radius": 80}),
			wantLeash: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newIdleArbFactoryTestServer(tc.live)
			cmd := buildRecoveredCoordinator(t, s, tc.persisted)
			require.Equal(t, tc.wantLeash, cmd.IdleArbLeashRadius)
		})
	}
}

// TestContractCoordinatorResolvesBlacklistFromLiveConfig locks the blacklist
// money guard's live resolution (sp-ts82), including the two non-numeric edges:
// an absent list must fall through to the default guard, and an explicit empty
// list must survive as the captain's deliberate disable.
func TestContractCoordinatorResolvesBlacklistFromLiveConfig(t *testing.T) {
	cases := []struct {
		name          string
		live          config.IdleArbSettings
		persisted     map[string]interface{}
		wantBlacklist []string
	}{
		{
			name:          "live blacklist overrides stale persisted blacklist",
			live:          config.IdleArbSettings{Blacklist: []string{"ELECTRONICS", "FUEL"}},
			persisted:     idleArbLaunchConfig(map[string]interface{}{"idle_arb_blacklist": []interface{}{"STALE_GOOD"}}),
			wantBlacklist: []string{"ELECTRONICS", "FUEL"},
		},
		{
			// Section absent -> stale cleared to nil; WithDefaults applies
			// [ELECTRONICS] downstream. A stale list can never shadow the default
			// guard (money guards fail closed, RULINGS #4).
			name:          "absent live blacklist clears stale to nil",
			live:          config.IdleArbSettings{},
			persisted:     idleArbLaunchConfig(map[string]interface{}{"idle_arb_blacklist": []interface{}{"STALE_GOOD"}}),
			wantBlacklist: nil,
		},
		{
			// Explicit empty list is a deliberate captain disable (sp-uohe): the
			// non-nil [] must survive as [] (not collapse to nil -> [ELECTRONICS]).
			name:          "explicit empty live blacklist disables the guard",
			live:          config.IdleArbSettings{Blacklist: []string{}},
			persisted:     idleArbLaunchConfig(map[string]interface{}{"idle_arb_blacklist": []interface{}{"STALE_GOOD"}}),
			wantBlacklist: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newIdleArbFactoryTestServer(tc.live)
			cmd := buildRecoveredCoordinator(t, s, tc.persisted)
			require.Equal(t, tc.wantBlacklist, cmd.IdleArbBlacklist)
		})
	}
}

// TestContractCoordinatorResolvesAllIdleArbSiblingsFromLiveConfig proves the
// resolution is ONE shared mechanism across every idle-arb sibling knob, not a
// per-key special case (sp-ts82): a single live config.yaml drives them all on the
// recovery rebuild, and every stale persisted sibling is discarded.
func TestContractCoordinatorResolvesAllIdleArbSiblingsFromLiveConfig(t *testing.T) {
	live := config.IdleArbSettings{
		ReserveHulls:    2,
		HubRadius:       300,
		LeashRadius:     150,
		MaxLegSeconds:   600,
		MaxSpend:        120000,
		MinMargin:       3,
		MarginVerifyPct: 90,
		IntervalSeconds: 120,
		Blacklist:       []string{"ELECTRONICS", "FUEL"},
		// sp-u4tv per-trip profitability floor siblings.
		MinNetProfitPerUnit: 150,
		NetProfitPct:        25,
		FuelCostPerUnit:     40,
	}
	// Stale persisted launch config from a PRIOR boot: every sibling holds an
	// outdated value the recovery rebuild must discard in favor of `live`.
	persisted := idleArbLaunchConfig(map[string]interface{}{
		"idle_arb_reserve_hulls":      1,
		"idle_arb_hub_radius":         250,
		"idle_arb_leash_radius":       80,
		"idle_arb_max_leg_secs":       480,
		"idle_arb_max_spend":          100000,
		"idle_arb_min_margin":         1,
		"idle_arb_margin_verify_pct":  80,
		"idle_arb_interval_secs":      90,
		"idle_arb_blacklist":          []interface{}{"STALE_GOOD"},
		"idle_arb_min_net_profit":     100,
		"idle_arb_net_profit_pct":     20,
		"idle_arb_fuel_cost_per_unit": 35,
	})

	s := newIdleArbFactoryTestServer(live)
	cmd := buildRecoveredCoordinator(t, s, persisted)

	require.Equal(t, 2, cmd.IdleArbReserveHulls)
	require.Equal(t, float64(300), cmd.IdleArbHubRadius)
	require.Equal(t, float64(150), cmd.IdleArbLeashRadius)
	require.Equal(t, 600, cmd.IdleArbMaxLegSecs)
	require.Equal(t, 120000, cmd.IdleArbMaxSpend)
	require.Equal(t, 3, cmd.IdleArbMinMargin)
	require.Equal(t, 90, cmd.IdleArbMarginVerifyPct)
	require.Equal(t, 120, cmd.IdleArbIntervalSecs)
	require.Equal(t, []string{"ELECTRONICS", "FUEL"}, cmd.IdleArbBlacklist)
	// sp-u4tv: the profitability floor knobs resolve live too (stale 100/20/35 discarded).
	require.Equal(t, 150, cmd.IdleArbMinNetProfit)
	require.Equal(t, 25, cmd.IdleArbNetProfitPct)
	require.Equal(t, 40, cmd.IdleArbFuelCostPerUnit)
}

// TestContractCoordinatorResolvesDisabledToggleFromLiveConfig: the harvest escape
// hatch also resolves live (sp-ts82). A coordinator persisted disabled must
// re-enable when config.yaml drops `disabled: true` and the daemon restarts — the
// stale idle_arb_disabled key can never keep the harvest off — and, symmetrically,
// a live `disabled: true` must take effect on the recovery rebuild.
func TestContractCoordinatorResolvesDisabledToggleFromLiveConfig(t *testing.T) {
	// live: harvest ON (Disabled false) but a stale key says it was turned off.
	s := newIdleArbFactoryTestServer(config.IdleArbSettings{})
	cmd := buildRecoveredCoordinator(t, s, idleArbLaunchConfig(map[string]interface{}{
		"idle_arb_disabled": true,
	}))
	require.False(t, cmd.IdleArbDisabled, "stale disabled=true must not survive a live re-enable")

	// live: harvest OFF -> the toggle takes effect on the recovery rebuild too.
	s = newIdleArbFactoryTestServer(config.IdleArbSettings{Disabled: true})
	cmd = buildRecoveredCoordinator(t, s, idleArbLaunchConfig(nil))
	require.True(t, cmd.IdleArbDisabled, "live disabled=true must take effect on the recovery rebuild")
}
