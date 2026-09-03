package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// remainingBill returns how many more units of the task's material the construction site still
// needs — the pipeline material target minus what has been delivered. It bounds the
// executor's hull-fill so a trip never over-buys past demand. Returns 0 (no bill cap → the
// executor fills to full hull capacity) whenever the pipeline or material is unavailable: a
// supply is never harmful (the site accepts only what it needs and the next tick re-polls), so an
// unreadable bill safely falls back to a full-hull fill rather than blocking the trip.
func (h *RunConstructionCoordinatorHandler) remainingBill(ctx context.Context, task *manufacturing.ManufacturingTask) int {
	if task.PipelineID() == "" {
		return 0
	}
	// Read the shared material counter under recordMu: a concurrent worker's
	// recordDelivery mutates deliveredQuantity, so this read must be serialized with that write to
	// stay race-free when several workers drain the SAME pipeline object. Value-identical to an
	// unlocked read — the lock only removes the data race, not any behavior.
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		return 0
	}
	material := pipeline.GetMaterial(task.Good())
	if material == nil {
		return 0
	}
	if remaining := material.RemainingQuantity(); remaining > 0 {
		return remaining
	}
	return 0
}

// reconcilePipelinesFromSite corrects each EXECUTING pipeline's delivered counters against the LIVE
// construction site — one site read per distinct pipeline per tick, and only on a tick that already
// has work, so an idle drain adds no API cost.
//
// The counters are a cache written only AFTER the server accepts a supply, so every interruption in
// that gap leaves them permanently BEHIND: the drain then sources material the site no longer needs
// and the surplus can never be delivered, the server-side requirement already being met. The same
// row drives the operator's gate percentage.
//
// A read failure is logged and skipped, never fatal — a stale counter over-sources, but a drain that
// refuses to run delivers nothing at all.
func (h *RunConstructionCoordinatorHandler) reconcilePipelinesFromSite(ctx context.Context, tasks []*manufacturing.ManufacturingTask, playerID int) {
	logger := common.LoggerFromContext(ctx)
	if h.siteSource == nil {
		logger.Log("WARNING", "Construction drain: site reconciliation is DISABLED (no construction-site source wired) — delivered counters can only drift BEHIND the server, over-sourcing the gate", nil)
		return
	}
	for _, site := range distinctConstructionSites(tasks) {
		if h.siteFullyDeliveredLocally(ctx, site.pipelineID) {
			logger.Log("DEBUG", "Construction drain: site fully delivered locally; live reconcile skipped", map[string]interface{}{
				"pipeline_id": site.pipelineID, "construction_site": site.waypoint,
			})
			continue
		}
		live, err := h.siteSource.FindByWaypoint(ctx, site.waypoint, playerID)
		if err != nil || live == nil {
			logger.Log("WARNING", fmt.Sprintf("Construction drain: could not read live construction site %s to reconcile delivered counters (this tick sizes buys off the cached row): %v", site.waypoint, err), nil)
			continue
		}
		h.applySiteTruth(ctx, site.pipelineID, live)
	}
}

// siteFullyDeliveredLocally reports whether every material this pipeline carries already reads
// delivered >= target locally. applySiteTruth is raise-only, so such a pipeline's counters cannot be
// moved by a live reading — the site read is pure API cost and is skipped. Anything unreadable (load
// error, missing pipeline, no materials) reports false so the read fires exactly as before.
func (h *RunConstructionCoordinatorHandler) siteFullyDeliveredLocally(ctx context.Context, pipelineID string) bool {
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	pipeline, err := h.pipelineRepo.FindByID(ctx, pipelineID)
	if err != nil || pipeline == nil {
		return false
	}
	materials := pipeline.Materials()
	if len(materials) == 0 {
		return false
	}
	for _, material := range materials {
		if material.DeliveredQuantity() < material.TargetQuantity() {
			return false
		}
	}
	return true
}

// applySiteTruth folds one live site reading into one pipeline's material counters and persists it.
// The load-modify-store runs under recordMu, the SAME lock recordDelivery holds, so a worker's
// delivery and this correction cannot interleave mid-update. The site READ deliberately happens
// outside the lock (an HTTP round trip must not block every worker's bill read), which is precisely
// why the correction is raise-only: a reading is already stale when it lands, so it may only ever
// close a gap, never erase a delivery recorded while it was in flight.
func (h *RunConstructionCoordinatorHandler) applySiteTruth(ctx context.Context, pipelineID string, live *manufacturing.ConstructionSite) {
	logger := common.LoggerFromContext(ctx)
	h.recordMu.Lock()
	defer h.recordMu.Unlock()

	pipeline, err := h.pipelineRepo.FindByID(ctx, pipelineID)
	if err != nil || pipeline == nil {
		return
	}
	corrected := false
	for _, liveMaterial := range live.Materials() {
		target := pipeline.GetMaterial(liveMaterial.TradeSymbol())
		if target == nil {
			continue // a site material this pipeline does not carry (another pipeline's leg)
		}
		// The planner sizes targetQuantity to the site's REMAINING units at planning time, so this
		// pipeline's own delivered total is its target minus what the site still wants — NOT the
		// site's Fulfilled, which also counts units delivered before this pipeline existed.
		observed := target.TargetQuantity() - liveMaterial.Remaining()
		if observed < 0 {
			observed = 0
		}
		recorded := target.DeliveredQuantity()
		if target.ReconcileDelivered(observed) {
			corrected = true
			logger.Log("INFO", fmt.Sprintf("Construction drain: reconciled %s from %d to %d delivered against the live site (%d/%d) — the cached counter was behind the server", liveMaterial.TradeSymbol(), recorded, observed, liveMaterial.Fulfilled(), liveMaterial.Required()), map[string]interface{}{
				"good": liveMaterial.TradeSymbol(), "was": recorded, "now": observed, "construction_site": live.WaypointSymbol(),
			})
			continue
		}
		if observed < recorded {
			logger.Log("WARNING", fmt.Sprintf("Construction drain: %s records %d delivered but the live site accounts for only %d — the local counter is ahead of the live site and was LEFT ALONE (lowering it would drop delivered units); investigate a double-count", liveMaterial.TradeSymbol(), recorded, observed), map[string]interface{}{
				"good": liveMaterial.TradeSymbol(), "recorded": recorded, "observed": observed, "construction_site": live.WaypointSymbol(),
			})
		}
	}
	if !corrected {
		return
	}
	if err := h.pipelineRepo.Update(ctx, pipeline); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist reconciled construction pipeline %s: %v", pipelineID, err), nil)
	}
}

// constructionSiteRef pairs a pipeline with the waypoint whose live state governs it.
type constructionSiteRef struct{ pipelineID, waypoint string }

// distinctConstructionSites reduces the tick's ready tasks to one entry per pipeline, so a fan-out
// of several tasks on one gate costs a single site read.
func distinctConstructionSites(tasks []*manufacturing.ManufacturingTask) []constructionSiteRef {
	seen := make(map[string]bool, len(tasks))
	refs := make([]constructionSiteRef, 0, len(tasks))
	for _, task := range tasks {
		pipelineID := task.PipelineID()
		if pipelineID == "" || task.ConstructionSite() == "" || seen[pipelineID] {
			continue
		}
		seen[pipelineID] = true
		refs = append(refs, constructionSiteRef{pipelineID: pipelineID, waypoint: task.ConstructionSite()})
	}
	return refs
}

// recordDelivery advances the pipeline's construction progress by the delivered units and
// persists it, so a supply moves the pipeline past 0%. A missing pipeline/material is a
// warning, never a task failure — the supply already succeeded. Returns the updated pipeline
// (with the just-recorded delivery applied to its persisted bill) so the caller can decide
// whether the material still needs refilling; nil on any path where progress was not recorded.
func (h *RunConstructionCoordinatorHandler) recordDelivery(ctx context.Context, task *manufacturing.ManufacturingTask, delivered int) *manufacturing.ManufacturingPipeline {
	logger := common.LoggerFromContext(ctx)
	if task.PipelineID() == "" || delivered <= 0 {
		return nil
	}
	// Serialize the load-add-store of pipeline progress across the concurrent workers: two
	// workers delivering to the SAME pipeline must not both read the old material total and
	// store a sum that drops the other's units. Cheap relative to the parallel hauling it guards.
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	pipeline, err := h.pipelineRepo.FindByID(ctx, task.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("WARNING", fmt.Sprintf("Could not load pipeline %s to record construction delivery", task.PipelineID()), nil)
		return nil
	}
	if err := pipeline.RecordMaterialDelivery(task.Good(), delivered); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not record construction delivery of %s: %v", task.Good(), err), nil)
		return nil
	}
	if err := h.pipelineRepo.Update(ctx, pipeline); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not persist construction pipeline progress %s: %v", task.PipelineID(), err), nil)
	}
	return pipeline
}

// enqueueReplenishmentIfNeeded is the continuous refill. One supplyTask delivers a single hauler
// cargo-load and the planner stages only one DELIVER_TO_CONSTRUCTION task per material, so without
// this the pipeline stalls EXECUTING below 100% after that first load. When the delivered material's
// bill is not yet met it enqueues the next single-load task, left READY for the drain to pick up
// next tick, so the pipeline self-re-stages one load at a time. Remaining is read from the
// pipeline's PERSISTED material bill, so no new cross-restart state is introduced, and the follow-on
// reuses this task's resolved delivery spec via the same domain factory the planner uses, so the two
// paths cannot drift. At remaining <= 0 nothing is queued and the chain settles.
func (h *RunConstructionCoordinatorHandler) enqueueReplenishmentIfNeeded(ctx context.Context, task *manufacturing.ManufacturingTask, pipeline *manufacturing.ManufacturingPipeline) {
	logger := common.LoggerFromContext(ctx)
	if pipeline == nil {
		return
	}
	remaining := h.remainingForGoodLocked(pipeline, task.Good())
	if remaining <= 0 {
		return // material bill met — stop cleanly, no further task
	}

	next := nextConstructionDeliveryTask(task)
	if err := next.MarkReady(); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Construction refill: could not ready replenishment task for %s: %v", task.Good(), err), nil)
		return
	}
	// Fresh context, not the passed ctx: ctx is the supply task's (cancelled when the delivery ends);
	// the replenishment must outlive it or a timed-out create silently kills the material's task chain.
	var createErr error
	for attempt := 1; attempt <= 3; attempt++ {
		createCtx, createCancel := context.WithTimeout(context.Background(), constructionRefillCreateTimeout)
		createErr = h.taskRepo.Create(createCtx, next)
		createCancel()
		if createErr == nil {
			break
		}
		logger.Log("WARNING", fmt.Sprintf("Construction refill: enqueue replenishment for %s attempt %d/3 failed: %v", task.Good(), attempt, createErr), nil)
	}
	if createErr != nil {
		logger.Log("ERROR", fmt.Sprintf("Construction refill: could not enqueue replenishment task for %s after 3 attempts — chain stalled until re-plan: %v", task.Good(), createErr), nil)
		return
	}
	logger.Log("INFO", fmt.Sprintf("Construction refill: queued next %s delivery (%d remaining)", task.Good(), remaining), map[string]interface{}{
		"good": task.Good(), "construction_site": task.ConstructionSite(), "remaining": remaining, "next_task": next.ID(), "pipeline_id": task.PipelineID(),
	})
}

// remainingForGood returns how many units of good the pipeline's construction bill still needs,
// from the just-updated persisted material target. A nil pipeline (recordDelivery could not
// load/persist it) or a material absent from the pipeline reports 0 — nothing to refill. The nil
// guard lets the deliver-on-hand path treat an unrecordable delivery as "bill met" exactly as the
// success tail does (complete, no replenishment).
func remainingForGood(pipeline *manufacturing.ManufacturingPipeline, good string) int {
	if pipeline == nil {
		return 0
	}
	material := pipeline.GetMaterial(good)
	if material == nil {
		return 0
	}
	return material.RemainingQuantity()
}

// remainingForGoodLocked reads pipeline's remaining bill for good under recordMu, so the read is
// race-free against a concurrent worker's recordDelivery write when several workers drain the SAME
// pipeline object. Value-identical to remainingForGood — the lock only removes the data race.
// Callers must NOT already hold recordMu (recordDelivery releases it before its result is read
// here), so there is no reentrancy.
func (h *RunConstructionCoordinatorHandler) remainingForGoodLocked(pipeline *manufacturing.ManufacturingPipeline, good string) int {
	h.recordMu.Lock()
	defer h.recordMu.Unlock()
	return remainingForGood(pipeline, good)
}
