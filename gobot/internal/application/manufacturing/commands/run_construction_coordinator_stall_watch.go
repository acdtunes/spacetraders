package commands

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
)

// watchGateProgress escalates when an ACTIVE pipeline's unmet material has received no delivered
// units for gate.StallThreshold (sp-63r4f).
//
// WHY THIS EXISTS AT ALL. Three separate defects this week produced one symptom: the gate stopped
// and every log line stayed true and reassuring. A head-of-line block, an affordability wall and a
// hysteresis deadlock — roughly 10 of 14 hours stalled, and EVERY ONE was found by a human asking
// why, not by anything saying so. Each of those is now fixed, and each fix bounds only its own
// cause. This detects the ABSENCE OF PROGRESS, which is what the next cause will also produce.
//
// IT LIVES IN THE COORDINATOR, AND ITS SILENCE IS NOT PROOF OF HEALTH. A check hosted inside the
// thing it watches goes quiet when its host wedges, stops being scheduled, or dies — precisely when
// an operator most wants to hear from it. That case is DELIBERATELY out of scope here and is
// covered by detectStaleHeartbeats (internal/captain/detectors.go), which watches for a coordinator
// that has stopped ticking. The two grains stack and neither subsumes the other:
//
//   - detectStaleHeartbeats: the coordinator is dead or wedged.
//   - THIS: the coordinator is ticking and heartbeating perfectly while one pipeline makes no
//     progress. That is the failure all three incidents actually had, and it is best seen from
//     inside the healthy path where the problem lives.
//   - sp-16zxm: the agent is transacting nothing at all while coordinators look healthy.
//
// So a reader who sees nothing from this function has learned that no pipeline is stalled ONLY IF
// the coordinator is also known to be ticking. Do not read its silence as health on its own — that
// mistake is the exact failure mode this bead exists to fight.
//
// It reports and never acts: no task is retried, no floor is moved, nothing is bought. Three
// different causes produced identical silence, so any automatic remedy would be right at most a
// third of the time, and a wrong remedy on a money path is worse than a stalled gate.
func (h *RunConstructionCoordinatorHandler) watchGateProgress(
	ctx context.Context,
	pipeline *manufacturing.ManufacturingPipeline,
	now time.Time,
) []gate.StallVerdict {
	if pipeline == nil || h.taskRepo == nil {
		return nil
	}

	completed, err := h.taskRepo.FindByPipelineAndStatus(ctx, pipeline.ID(), manufacturing.TaskStatusCompleted)
	if err != nil {
		// A read failure is NOT a stall. Reporting one here would cry wolf on a database hiccup and
		// teach the reader to ignore the line, which is the failure this watchdog exists to end.
		common.LoggerFromContext(ctx).Log("WARNING", "Gate progress watchdog could not read the completed-task history this tick, so it reports nothing rather than guessing at a stall: "+err.Error(), map[string]interface{}{
			"pipeline": pipeline.ID(), "action": "gate_stall_watch_unreadable",
		})
		return nil
	}

	// THE PERSISTED CLOCK (criterion 6). Last-delivery times come from COMPLETED task rows, which
	// outlive the process, so a daemon that restarted a second ago computes the same duration as one
	// that has been up for hours. A restart LOOP therefore cannot hide a stall — the sp-20eyn shape,
	// where 34,279 restarts looked like recovery. There is no in-memory clock to reset because there
	// is no in-memory clock at all.
	lastDelivery := make(map[string]time.Time, len(completed))
	deliveredInWindow := make(map[string]int, len(completed))
	windowOpened := now.Add(-gate.StallThreshold)
	earliest := now
	for _, task := range completed {
		if task == nil || task.ActualQuantity() <= 0 {
			continue // a completed task that moved nothing is not a delivery
		}
		at := task.CompletedAt()
		if at == nil {
			continue
		}
		if at.After(lastDelivery[task.Good()]) {
			lastDelivery[task.Good()] = *at
		}
		if at.After(windowOpened) {
			deliveredInWindow[task.Good()] += task.ActualQuantity()
		}
		if at.Before(earliest) {
			earliest = *at
		}
	}

	// measuredFrom is the earliest instant this pipeline can be spoken for. A material that has
	// NEVER been delivered is judged from the pipeline's start rather than from the epoch, so a
	// young pipeline is not accused of stalling before it has had a chance to deliver anything.
	measuredFrom := earliest
	if started := pipeline.StartedAt(); started != nil && started.Before(measuredFrom) {
		measuredFrom = *started
	}

	observed := make([]gate.MaterialProgress, 0, len(pipeline.Materials()))
	for _, material := range pipeline.Materials() {
		good := material.TradeSymbol()
		observed = append(observed, gate.MaterialProgress{
			Good:           good,
			Remaining:      material.RemainingQuantity(),
			UnitsDelivered: deliveredInWindow[good],
			LastDeliveryAt: lastDelivery[good],
		})
	}

	stalls := gate.DetectStalls(now, measuredFrom, observed, gate.StallThreshold)

	// THE GAUGE IS WRITTEN FOR EVERY MATERIAL, INCLUDING THE HEALTHY ZEROES. A gauge only written
	// when something is wrong keeps its last bad value forever after a recovery, so the alarm never
	// clears and the next real stall is indistinguishable from the stale one.
	stalled := make(map[string]time.Duration, len(stalls))
	for _, v := range stalls {
		stalled[v.Good] = v.StalledFor
	}
	for _, m := range observed {
		metrics.RecordGateStallSeconds(m.Good, stalled[m.Good].Seconds())
	}

	logger := common.LoggerFromContext(ctx)
	site := pipeline.ConstructionSite()
	for _, v := range stalls {
		// ERROR, not WARNING (criterion 3). A WARNING among thousands is exactly what let all three
		// incidents hide: the routine pause lines are WARNINGs and read as healthy patience. This
		// has to be distinguishable at a glance from them, so it leaves the band they occupy.
		logger.Log("ERROR", v.LogLine(site), map[string]interface{}{
			"good": v.Good, "site": site, "remaining": v.Remaining,
			"units_delivered": v.UnitsDelivered, "stalled_minutes": int(v.StalledFor.Minutes()),
			"source_supply": v.SourceSupply, "action": "gate_stalled",
		})
	}
	return stalls
}

// watchAllGateProgress runs the progress watchdog over every EXECUTING construction pipeline.
//
// ONLY EXECUTING PIPELINES (criterion 1). A pipeline that is complete, failed or not yet started is
// SUPPOSED to be receiving nothing, and reporting it would train the reader to ignore the line —
// which is how the routine WARNING lines came to be scrolled past during the real stalls.
func (h *RunConstructionCoordinatorHandler) watchAllGateProgress(ctx context.Context, playerID int) {
	if h.pipelineRepo == nil {
		return
	}
	pipelines, err := h.pipelineRepo.FindByStatus(ctx, playerID, []manufacturing.PipelineStatus{manufacturing.PipelineStatusExecuting})
	if err != nil {
		return // a read failure is not a stall; watchGateProgress explains the same reasoning
	}
	now := time.Now().UTC()
	for _, pipeline := range pipelines {
		if pipeline == nil || pipeline.ConstructionSite() == "" {
			continue // not a gate pipeline
		}
		h.watchGateProgress(ctx, pipeline, now)
	}
}
