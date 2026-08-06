package commands

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	playerID int,
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

	// THE SIGNAL IS THE MATERIAL'S OWN REMAINING COUNT, NOT TASK BOOKKEEPING (sp-zx0tu).
	//
	// This used to key "did units arrive" on completed tasks' actual_quantity. That was wrong twice
	// over: the field was dead (sp-1f0ex fixed the write), and even repaired it only sees deliveries
	// that finish through completeSupply. The delivery that exposed this landed 28 units and
	// completed NO task, so the repaired write never fired and the alarm kept climbing.
	//
	// The authoritative figure was already in hand and already being printed: the live remaining
	// requirement, re-read from the pipeline every tick. Across that delivery the watchdog's own
	// ERROR line went "still needs 165" -> "still needs 137" while claiming zero units received. A
	// falling remaining count IS a delivery, whatever path moved the cargo and whatever bookkeeping
	// did or did not happen — which is exactly the "regardless of why" property this watchdog is
	// supposed to have.
	//
	// completed is still read, but only so an unreadable task history keeps its own honest log line
	// above; nothing below depends on it.
	_ = completed

	measuredFrom := now
	if started := pipeline.StartedAt(); started != nil && started.Before(measuredFrom) {
		measuredFrom = *started
	}
	if created := pipeline.CreatedAt(); !created.IsZero() && created.Before(measuredFrom) {
		measuredFrom = created
	}

	observed := make([]gate.MaterialProgress, 0, len(pipeline.Materials()))
	for _, material := range pipeline.Materials() {
		good := material.TradeSymbol()
		remaining := material.RemainingQuantity()
		delivered, lastChanged := h.observeRemaining(pipeline.ID(), good, remaining, now)
		observed = append(observed, gate.MaterialProgress{
			Good:           good,
			Remaining:      remaining,
			UnitsDelivered: delivered,
			LastDeliveryAt: lastChanged,
			SourceSupply:   h.sourceSupplyFor(ctx, good, pipeline, playerID),
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
		h.watchGateProgress(ctx, pipeline, playerID, now)
	}
}

// materialObservation is the last remaining count this process saw for one material, and when it
// last CHANGED. Keyed per pipeline+material in the handler's stallSeen map.
type materialObservation struct {
	remaining   int
	lastChanged time.Time
}

// observeRemaining records this tick's remaining count and reports what changed since the last one:
// the units the requirement FELL by (0 when flat), and when it last moved.
//
// A FALLING REMAINING COUNT IS A DELIVERY, whatever path moved the cargo. That is the whole point of
// re-sourcing here: the watchdog is supposed to detect the absence of progress "regardless of why",
// and keying on task bookkeeping made it blind to any delivery that did not complete a task — which
// is the delivery that exposed it.
//
// THE FIRST OBSERVATION REPORTS PROGRESS, NOT SILENCE (criterion 3). With no prior tick there is no
// basis to claim anything has been quiet, and the honest answer is "no evidence of a stall" rather
// than "stalled since the epoch". Returning `now` as the change time makes the elapsed quiet zero,
// so the first tick after a daemon restart cannot raise an alarm from a missing memory.
//
// A RESTART LOOP CAN THEREFORE HIDE A STALL, and that is a real cost, stated rather than buried: a
// process that bounces faster than gate.StallThreshold re-seeds this map every time and never
// accumulates. The alternative was persisting the snapshot, which needs a schema change the
// manufacturing models cannot take (they are absent from the startup AutoMigrate list). The
// coordinator-liveness case is covered separately by detectStaleHeartbeats, which is what notices a
// daemon that keeps restarting.
//
// A RISING remaining count (the site's bill grew, or a correction raised it) is NOT progress and is
// deliberately not treated as one — but it does reset the baseline, so the next fall is measured
// from the new figure rather than reported as a huge phantom delivery.
func (h *RunConstructionCoordinatorHandler) observeRemaining(pipelineID, good string, remaining int, now time.Time) (delivered int, lastChanged time.Time) {
	key := pipelineID + "|" + good

	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallSeen == nil {
		h.stallSeen = make(map[string]materialObservation)
	}

	prior, seen := h.stallSeen[key]
	if !seen {
		h.stallSeen[key] = materialObservation{remaining: remaining, lastChanged: now}
		return 0, now // no prior tick: no basis to call anything quiet
	}
	if remaining < prior.remaining {
		delivered = prior.remaining - remaining
		h.stallSeen[key] = materialObservation{remaining: remaining, lastChanged: now}
		return delivered, now
	}
	if remaining > prior.remaining {
		// The bill grew. Not a delivery, but re-baseline so the next fall is not a phantom.
		h.stallSeen[key] = materialObservation{remaining: remaining, lastChanged: prior.lastChanged}
		return 0, prior.lastChanged
	}
	return 0, prior.lastChanged // flat: the quiet continues from when it last moved
}

// sourceSupplyFor reports the supply level at the material's source, purely so the escalation can
// print it. It is NOT part of the stall decision.
//
// Best-effort by design: the ERROR line already renders an empty result as "unreadable", and a
// watchdog must never fail to report a stall because a market lookup was unavailable. Before
// sp-zx0tu this was never populated at all, so every escalation said "Source supply reads
// unreadable" whether or not the market was readable — a field that always reads the same is one a
// reader learns to skip.
func (h *RunConstructionCoordinatorHandler) sourceSupplyFor(ctx context.Context, good string, pipeline *manufacturing.ManufacturingPipeline, playerID int) string {
	if h.gate == nil || h.gate.topology == nil {
		return ""
	}
	system := shared.ExtractSystemSymbol(pipeline.ConstructionSite())
	if system == "" {
		return ""
	}
	source, err := h.gate.topology.TerminalFactory(ctx, good, system, playerID)
	if err != nil || source == nil {
		return ""
	}
	return source.Supply
}
