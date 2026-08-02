package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// TestResolveWorkerCap_ReflectsLiveMaxWorkersChange_NoRestart is the live-change proof at
// the CONSUMER seam. resolveWorkerCap computes the "dispatching ... (worker cap N)" bound by reading
// the pipeline's MaxWorkers() off the repo EVERY tick (FindByID), so a live write to the stored cap
// — what MutateConstructionMaxWorkers does — is picked up on the very next tick by the SAME running
// handler, with no coordinator/daemon restart. Amending the persisted cap in place (exactly what the
// write path does via SetMaxWorkers) and re-resolving on the unchanged handler proves it.
func TestResolveWorkerCap_ReflectsLiveMaxWorkersChange_NoRestart(t *testing.T) {
	pipeline := newDrainPipelineWithWorkers(t, "FAB_MATS", 100000, 1)
	tasks := []*manufacturing.ManufacturingTask{readyConstructionTask(t, pipeline, "FAB_MATS")}
	pipelineRepo := &drainStubPipelineRepo{
		pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline},
	}

	// The running coordinator — built ONCE, never restarted.
	handler := NewRunConstructionCoordinatorHandler(nil, pipelineRepo, nil, nil, nil, nil)
	ctx := context.Background()

	require.Equal(t, 1, handler.resolveWorkerCap(ctx, tasks), "cap starts at the launch value")

	// The live write path amends the persisted cap in place; no restart, no new handler.
	pipeline.SetMaxWorkers(10)

	require.Equal(t, 10, handler.resolveWorkerCap(ctx, tasks),
		"the next tick's worker cap must reflect the live-set 10 with no restart")
}
