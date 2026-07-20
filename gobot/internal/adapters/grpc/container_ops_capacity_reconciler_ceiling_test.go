package grpc

// sp-rt3b6 PART 2 (no-drift source) — the capacity reconciler's planner cap must read
// the SAME contract-delivery ceiling the fleet autosizer uses, from ONE source, so the
// two can never drift (RULINGS #5). The reconciler build derives its ceiling from the
// autosizer's OWN config field (s.fleetAutosizerConfig.FleetCeilingContractDelivery)
// and the autosizer's OWN exported default — never a second, independently-tunable knob.

import (
	"testing"

	"github.com/stretchr/testify/require"

	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// Behavior: the reconciler command's contract-delivery hull ceiling is derived from the
// autosizer's live config value — set the autosizer ceiling, the reconciler cap matches
// it exactly. This is the shared-source guarantee: one config value, two consumers.
func TestCapacityReconcilerCeilingSharesAutosizerConfigValue(t *testing.T) {
	s := newFactoryTestServer()
	s.fleetAutosizerConfig = config.FleetAutosizerConfig{FleetCeilingContractDelivery: 12}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator",
		jsonRoundTrip(t, map[string]interface{}{"container_id": "capacity-reconciler-7"}), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.Equal(t, 12, cmd.ContractDeliveryHullCeiling,
		"the reconciler cap must equal the autosizer's configured fleet_ceiling_contract_delivery (one source, no drift)")
}

// Behavior: when the autosizer ceiling is unset (0), the reconciler falls back to the
// autosizer's OWN exported default — the SAME default the autosizer applies — so the
// two agree even with nothing in config.yaml. No independent reconciler default exists
// to drift.
func TestCapacityReconcilerCeilingFallsBackToAutosizerDefault(t *testing.T) {
	s := newFactoryTestServer() // fleetAutosizerConfig zero: contract-delivery ceiling unset

	built, err := s.buildCommandForType("capacity_reconciler_coordinator",
		jsonRoundTrip(t, map[string]interface{}{"container_id": "capacity-reconciler-7"}), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.Equal(t, fleetCmd.DefaultFleetCeilingContractDelivery, cmd.ContractDeliveryHullCeiling,
		"an unset autosizer ceiling must fall back to the autosizer's OWN default — the reconciler never defines a second default that could drift")
}

// Behavior (live-config discipline): a stale persisted ceiling from a prior boot is
// cleared and re-derived from the current autosizer config on every build, so a config
// edit + restart retunes even a recovered reconciler and a stale copy can never shadow.
func TestCapacityReconcilerCeilingResolvesLiveClearingStaleCopy(t *testing.T) {
	s := newFactoryTestServer()
	s.fleetAutosizerConfig = config.FleetAutosizerConfig{FleetCeilingContractDelivery: 8}

	persisted := map[string]interface{}{
		"container_id": "capacity-reconciler-7",
		"capacity_fleet_ceiling_contract_delivery": 99, // stale from a prior boot
	}

	built, err := s.buildCommandForType("capacity_reconciler_coordinator", jsonRoundTrip(t, persisted), 7, "capacity-reconciler-7")
	require.NoError(t, err)

	cmd := built.(*capacityCmd.RunCapacityReconcilerCoordinatorCommand)
	require.Equal(t, 8, cmd.ContractDeliveryHullCeiling,
		"the stale persisted 99 must be cleared and re-derived from the live autosizer config (8)")
}
