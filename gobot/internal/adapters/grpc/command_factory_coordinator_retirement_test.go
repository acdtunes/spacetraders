package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-jav2 X2: two factory-coordinator designs were a maintenance tax (GOODS_FACTORY_GAP lineage).
// The shipwright-designated SURVIVOR is the per-good goods_factory_coordinator — what runs in prod.
// The parallel task-style coordinator ("manufacturing_coordinator") and its task worker
// ("manufacturing_task_worker") are retired. These pins guard the retirement through the REAL
// container-spec registry (registerContainerSpecs): the survivor still registers and builds, and
// the retired designs are gone with NO dangling registration — buildCommandForType reports them as
// unknown command types, the same failure a never-existent type produces.

func TestGoodsFactoryCoordinatorSurvivesRetirement(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	got, err := s.buildCommandForType("goods_factory_coordinator", goodsFactoryLaunchConfig(nil), 3, "goods-jav2")
	require.NoError(t, err)
	_, ok := got.(*goodsCmd.RunFactoryCoordinatorCommand)
	require.True(t, ok, "survivor goods_factory_coordinator must still build through the registry, got %T", got)
}

func TestRetiredParallelManufacturingCoordinatorHasNoRegistration(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	_, err := s.buildCommandForType("manufacturing_coordinator", goodsFactoryLaunchConfig(nil), 3, "mfg-jav2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command type", "the retired parallel coordinator must have no dangling registration")
}

func TestRetiredManufacturingTaskWorkerHasNoRegistration(t *testing.T) {
	s := newManufacturingFactoryTestServer(config.ManufacturingConfig{})
	_, err := s.buildCommandForType("manufacturing_task_worker", goodsFactoryLaunchConfig(nil), 3, "worker-jav2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown command type", "the retired task worker must have no dangling registration")
}
