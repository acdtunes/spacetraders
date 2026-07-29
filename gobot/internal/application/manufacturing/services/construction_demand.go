package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ConstructionDemandReader answers common.ConstructionDemandReader from the pipeline store: the
// capital budget's honest "does construction still need money?" signal.
//
// It reads the BILL rather than the tick. The construction drain is a standing coordinator whose
// liveness says nothing about demand — it keeps polling after its gate is finished — so the
// capital budget must ask what is still OWED, not whether something is running. The bill lives in
// the pipeline row (material target vs delivered), which makes this signal:
//
//   - restart-safe: it survives a daemon bounce, unlike any in-memory tick counter;
//   - immune to activation timing: a coordinator between ticks, or one whose tasks are queued
//     PENDING rather than promoted READY, still has an unfilled target and still reports demand.
//     Promotion counts are a symptom and are deliberately not consulted here.
//
// Only EXECUTING pipelines count. A CANCELLED or COMPLETED pipeline may still carry an unfilled
// target and orphaned tasks — the live era-5 state had exactly that, two CANCELLED pipelines with
// FAB_MATS at 1474/1600 and 0/126 plus a stranded READY task — and none of it is fundable work.
type ConstructionDemandReader struct {
	pipelines manufacturing.PipelineRepository
}

// NewConstructionDemandReader builds the pipeline-backed demand reader the daemon attaches to the
// capital work sensor.
func NewConstructionDemandReader(pipelines manufacturing.PipelineRepository) *ConstructionDemandReader {
	return &ConstructionDemandReader{pipelines: pipelines}
}

// HasOutstandingConstructionDemand reports whether any EXECUTING pipeline still owes material.
//
// Every uncertain answer resolves toward "yes, reserve the capital" (RULINGS #4). Unwired reader,
// a pipeline type whose bill this cannot evaluate, and an executing construction pipeline with no
// materials recorded all report demand; a read failure is SURFACED so the caller applies its own
// fail-conservative resolution rather than having "idle" invented for it. Only a complete,
// successful pass over every executing pipeline finding every target filled returns false.
func (r *ConstructionDemandReader) HasOutstandingConstructionDemand(ctx context.Context, playerID int) (bool, error) {
	if r == nil || r.pipelines == nil {
		return true, nil // wiring bug, not a supported mode — never release the reservation blind
	}
	executing, err := r.pipelines.FindByStatus(ctx, playerID,
		[]manufacturing.PipelineStatus{manufacturing.PipelineStatusExecuting})
	if err != nil {
		return false, fmt.Errorf("failed to read executing construction pipelines for player %d: %w", playerID, err)
	}
	for _, pipeline := range executing {
		if pipeline == nil {
			continue
		}
		// A non-construction pipeline has no material bill to measure. It is executing and it
		// may well be spending, so it counts as demand rather than being silently ignored.
		if pipeline.PipelineType() != manufacturing.PipelineTypeConstruction {
			return true, nil
		}
		materials := pipeline.Materials()
		if len(materials) == 0 {
			return true, nil // executing with no recorded bill — cannot prove it is satisfied
		}
		for _, material := range materials {
			if material == nil {
				return true, nil // an unreadable line item is an unproven one
			}
			if material.DeliveredQuantity() < material.TargetQuantity() {
				return true, nil
			}
		}
	}
	return false, nil
}
