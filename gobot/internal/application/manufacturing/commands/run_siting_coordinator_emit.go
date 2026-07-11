package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// emit posts scout-demand for stale-but-promising sites (EMIT step, sp-vdld M6). A site the
// coordinator WANTS to run (in the desired top-K) whose market data has aged past EmitStaleness
// is worth refreshing: siting decided on old data, so we ask the scout system to re-cover that
// system, closing the discovery loop (the sp-k7q5 scout-post-proposal channel). Launching is
// unaffected — the child goods_factory coordinator re-reads live markets through its guards
// before it spends, so a launch on stale SCAN data is not a money risk; the scout-demand only
// sharpens the NEXT decision. Emission is deduped per system over the cooldown (the emitter's
// HasSince), and collapsed to one demand per system per tick here. Returns the count emitted.
//
// A nil emitter disables EMIT. On a per-system emit error the coordinator logs and continues.
func (h *RunSitingCoordinatorHandler) emit(ctx context.Context, cmd *RunSitingCoordinatorCommand, cfg sitingRunConfig, desired []ScoredCandidate) int {
	if h.emitter == nil {
		return 0
	}
	logger := common.LoggerFromContext(ctx)

	// Distinct stale-but-promising systems, carrying the worst age seen for the payload.
	staleAge := make(map[string]float64)
	for _, d := range desired {
		if d.DataAgeSecs <= cfg.EmitStaleness.Seconds() {
			continue
		}
		if age, seen := staleAge[d.System]; !seen || d.DataAgeSecs > age {
			staleAge[d.System] = d.DataAgeSecs
		}
	}

	emitted := 0
	for system, age := range staleAge {
		payload := fmt.Sprintf(`{"system":%q,"reason":"siting_stale_promising","data_age_secs":%d}`, system, int(age))
		ok, err := h.emitter.EmitScoutDemand(ctx, cmd.PlayerID, system, cfg.ScoutCooldown, payload)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("siting scout-demand emit failed for %s: %v", system, err), map[string]interface{}{
				"action": "siting_emit_failed", "container_id": cmd.ContainerID, "system": system,
			})
			continue
		}
		if ok {
			emitted++
			metrics.RecordSitingScoutDemand(system)
			logger.Log("INFO", fmt.Sprintf("siting emitted scout-demand for stale-but-promising system %s (data age %ds)", system, int(age)), map[string]interface{}{
				"action": "siting_scout_demand", "container_id": cmd.ContainerID, "system": system, "bead": "sp-vdld",
			})
		}
	}
	return emitted
}

// runSelfCheck is the effect self-check (sp-vdld M6, the reposition sp-686e edge-triggered
// idiom): it watches for the coordinator scanning real candidates yet producing NO effect,
// sustained over EffectSelfcheckTicks ticks, and then emits ONE WARN naming the cause. It fires
// on the two genuine no-effect pathologies — never on a healthy satisfied portfolio (which has
// scored candidates and running chains it simply doesn't need to change):
//
//	(a) every candidate is vetoed/unpriceable (Scored == 0) — the "17 attempts, 0 survivors"
//	    pattern: margins negative or inputs ineligible across the board.
//	(b) dry-run is suppressing a real desired portfolio (Desired > 0, zero actions) — the
//	    operator likely forgot the watch flag is on.
//
// Any productive/steady tick resets the streak and re-arms the one-shot WARN.
func (h *RunSitingCoordinatorHandler) runSelfCheck(ctx context.Context, cmd *RunSitingCoordinatorCommand, cfg sitingRunConfig, res reconcileResult) {
	allVetoed := res.Candidates > 0 && res.Scored == 0
	dryRunSuppressing := cfg.DryRun && res.Desired > 0 && res.Actions() == 0
	noEffect := res.Actions() == 0 && (allVetoed || dryRunSuppressing)

	st := h.coordinatorState(cmd.ContainerID)
	h.mu.Lock()
	if !noEffect {
		st.noEffectStreak = 0
		st.noEffectPaged = false
		h.mu.Unlock()
		return
	}
	st.noEffectStreak++
	streak := st.noEffectStreak
	fire := streak >= cfg.EffectSelfcheckTicks && !st.noEffectPaged
	if fire {
		st.noEffectPaged = true
	}
	h.mu.Unlock()
	if !fire {
		return
	}

	var reason string
	switch {
	case dryRunSuppressing:
		reason = fmt.Sprintf("dry_run is suppressing %d desired launch/retire action(s) — clear siting_dry_run to arm", res.Desired)
	default:
		reason = fmt.Sprintf("all %d scanned candidate(s) were vetoed/unpriceable by the launch guard — chains are margin-negative or have ineligible inputs; check chain margins and [manufacturing.siting]", res.Candidates)
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("siting produced no effect for %d consecutive ticks: %s (sp-vdld)", streak, reason), map[string]interface{}{
		"action":       "siting_no_effect",
		"container_id": cmd.ContainerID,
		"candidates":   res.Candidates,
		"scored":       res.Scored,
		"desired":      res.Desired,
		"dry_run":      cfg.DryRun,
		"bead":         "sp-vdld",
	})
}
