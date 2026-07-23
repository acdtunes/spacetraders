package grpc

import (
	"context"
	"fmt"
	"testing"

	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
)

// contractScalerTargetFor is the sp-gm7r GATE-entry bar's PRODUCER: the contract auto-scaler's live
// achievable fleet target = min(scaler plan slots, the scaler's live contract_fleet_max_hulls ceiling), and
// 0 (FAIL-CLOSED) when no scaler is running. These are integration tests against a real container repo
// (adapters are integration-tested, never mock-the-infra) — the consumer (gateFunded reading the target) is
// covered separately at the phase-machine layer.

// contractScalerPlanSlots is the fixed plan size the target clamps against — the same sum the producer
// computes, traced to the scaler domain constants so the expectations below are not magic values.
var contractScalerPlanSlots = contractscaler.MaxDeliveryHulls + contractscaler.WarehouseUnits + contractscaler.StockerUnits

// No scaler running ⇒ the target is 0, so gateFunded never enters GATE on an unknown target.
func TestContractScalerTargetFor_NoScaler_FailsClosedToZero(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	if got := contractScalerTargetFor(context.Background(), s.containerRepo, playerID); got != 0 {
		t.Fatalf("no scaler running must fail closed to target 0, got %d", got)
	}
}

// A scaler with no ceiling config falls back to the default ceiling, which is below the plan size, so the
// target is the default ceiling (min(planSlots, default)).
func TestContractScalerTargetFor_ScalerWithDefaultCeiling_ClampsPlanToCeiling(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "scaler-1", "RunContractScaler", string(container.ContainerTypeContractScaler), "", playerID, nil)

	if contractScalerPlanSlots <= contractScalerCmd.DefaultContractFleetMaxHulls {
		t.Fatalf("test precondition: plan slots (%d) must exceed the default ceiling (%d) for this clamp case", contractScalerPlanSlots, contractScalerCmd.DefaultContractFleetMaxHulls)
	}
	if got := contractScalerTargetFor(context.Background(), s.containerRepo, playerID); got != contractScalerCmd.DefaultContractFleetMaxHulls {
		t.Fatalf("scaler with no ceiling config → min(planSlots %d, default ceiling %d) = %d, got %d", contractScalerPlanSlots, contractScalerCmd.DefaultContractFleetMaxHulls, contractScalerCmd.DefaultContractFleetMaxHulls, got)
	}
}

// The target tracks a live contract_fleet_max_hulls tune on the scaler container: a ceiling below the plan
// clamps to the ceiling; a ceiling above the plan clamps to the plan slots.
func TestContractScalerTargetFor_LiveCeiling_TracksTunedValue(t *testing.T) {
	cases := []struct {
		name    string
		ceiling int
		want    int
	}{
		{"below_plan_clamps_to_ceiling", 12, 12},                         // min(15, 12) = 12
		{"above_plan_clamps_to_plan_slots", 16, contractScalerPlanSlots}, // min(15, 16) = 15
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			cfg := fmt.Sprintf(`{"contract_fleet_max_hulls": %d}`, tc.ceiling)
			insertRunningContainer(t, db, "scaler-1", "RunContractScaler", string(container.ContainerTypeContractScaler), cfg, playerID, nil)
			if got := contractScalerTargetFor(context.Background(), s.containerRepo, playerID); got != tc.want {
				t.Fatalf("ceiling %d → min(planSlots %d, ceiling) = %d, got %d", tc.ceiling, contractScalerPlanSlots, tc.want, got)
			}
		})
	}
}
