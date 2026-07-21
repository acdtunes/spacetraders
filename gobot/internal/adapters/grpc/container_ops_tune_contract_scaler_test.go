package grpc

import (
	"testing"

	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The single operator lever on the contract scaler is contract_fleet_max_hulls, live-tunable via
// `tune --operation contractscaler` and hot-reloaded each tick (Pattern-C). This pins the registry
// wiring: the operation alias resolves to the container type, and the ceiling knob carries the
// documented bounds + the coordinator's own default (so the two can never drift).
func TestTune_ContractScaler_OperationResolvesAndCeilingKnobRegistered(t *testing.T) {
	if got := tuneOperationCoordinatorTypes["contractscaler"]; got != string(container.ContainerTypeContractScaler) {
		t.Fatalf("operation alias \"contractscaler\" resolves to %q, want %q", got, container.ContainerTypeContractScaler)
	}

	knobs, ok := tunableKnobsByContainerType()[string(container.ContainerTypeContractScaler)]
	if !ok {
		t.Fatal("the contract scaler must have a tunable-knob block")
	}
	bound, ok := knobs["contract_fleet_max_hulls"]
	if !ok {
		t.Fatal("contract_fleet_max_hulls must be a tunable knob of the contract scaler")
	}
	if bound.Min != 0 || bound.Max != 16 || bound.Default != contractScalerCmd.DefaultContractFleetMaxHulls {
		t.Fatalf("contract_fleet_max_hulls bounds = {Min:%d Max:%d Default:%d}, want {0 16 %d}",
			bound.Min, bound.Max, bound.Default, contractScalerCmd.DefaultContractFleetMaxHulls)
	}
	if bound.Type != "int" || bound.Unit == "" || bound.Description == "" {
		t.Fatalf("contract_fleet_max_hulls must carry type/unit/description: %+v", bound)
	}
}
