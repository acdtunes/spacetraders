package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// act reconciles the running portfolio against the desired top-K: launch chains in
// desired-but-not-running THROUGH the guard stack (Launch → the goods_factory container runs
// its own chain-margin, fail-closed sourcing, chain-P&L kill, and input-poison anti-cycle
// guards on its own iterations, guards veto at zero cost), and retire chains
// running-but-not-desired via a clean container stop, with hysteresis (a chain must fall out
// of top-K for RetireHysteresisTicks consecutive ticks before it is retired — anti-thrash).
// Returns (launched, retired) counts.
//
// Per-chain INPUT-rest (an input-poisoned chain resting and self-recovering) is the running
// goods_factory coordinator's own job (container-internal state, not a cross-chain API). This
// coordinator drives PORTFOLIO membership only; it does not reimplement pause/re-attempt.
//
// FAIL-SAFE: if the running set cannot be read, ACT does nothing this tick (never launch a
// duplicate or retire blindly on a stale view). Per-chain launch/retire errors are logged and
// skipped; they do not abort the rest of the reconcile.
func (h *RunSitingCoordinatorHandler) act(ctx context.Context, cmd *RunSitingCoordinatorCommand, cfg sitingRunConfig, desired []ScoredCandidate) (launched, retired int) {
	logger := common.LoggerFromContext(ctx)

	running, err := h.controller.RunningChains(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("siting cannot read running chains — skipping ACT this tick: %v", err), map[string]interface{}{
			"action": "siting_act_skipped", "container_id": cmd.ContainerID,
		})
		return 0, 0
	}

	desiredByKey := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		desiredByKey[d.Key()] = struct{}{}
	}
	runningByKey := make(map[string]RunningChain, len(running))
	for _, r := range running {
		runningByKey[r.Key()] = r
	}

	st := h.coordinatorState(cmd.ContainerID)

	// LAUNCH — desired chains that are not yet running. A desired chain always resets its
	// out-of-top-K hysteresis (it is back in favor), whether or not it is already running.
	for _, d := range desired {
		h.clearOutOfTopK(st, d.Key())
		if _, isRunning := runningByKey[d.Key()]; isRunning {
			continue
		}
		if cfg.DryRun {
			logger.Log("INFO", fmt.Sprintf("[siting dry-run] would launch chain %s@%s (score %.0f)", d.Good, d.System, d.Score), h.actLogFields(cmd, d.Good, d.System))
			continue
		}
		id, err := h.controller.Launch(ctx, d.Good, d.System, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("siting launch failed for %s@%s: %v", d.Good, d.System, err), h.actLogFields(cmd, d.Good, d.System))
			continue
		}
		logger.Log("INFO", fmt.Sprintf("siting launched chain %s@%s (score %.0f, container %s)", d.Good, d.System, d.Score, id), h.actLogFields(cmd, d.Good, d.System))
		metrics.RecordSitingLaunch(d.Good, d.System)
		launched++
	}

	// RETIRE — running chains that fell out of top-K, once they have been out for the full
	// hysteresis window (anti-thrash for a chain flickering at the K boundary).
	for _, r := range running {
		if _, stillDesired := desiredByKey[r.Key()]; stillDesired {
			continue
		}
		n := h.incOutOfTopK(st, r.Key())
		if n < cfg.RetireHysteresisTicks {
			continue // holding — not yet out long enough to retire
		}
		if cfg.DryRun {
			logger.Log("INFO", fmt.Sprintf("[siting dry-run] would retire chain %s@%s (out of top-K %d ticks)", r.Good, r.System, n), h.actLogFields(cmd, r.Good, r.System))
			continue
		}
		if err := h.controller.Retire(ctx, r.FactoryID, cmd.PlayerID); err != nil {
			logger.Log("WARNING", fmt.Sprintf("siting retire failed for %s@%s (%s): %v", r.Good, r.System, r.FactoryID, err), h.actLogFields(cmd, r.Good, r.System))
			continue
		}
		h.clearOutOfTopK(st, r.Key())
		logger.Log("INFO", fmt.Sprintf("siting retired chain %s@%s (%s) — fell out of top-K for %d ticks", r.Good, r.System, r.FactoryID, n), h.actLogFields(cmd, r.Good, r.System))
		metrics.RecordSitingRetire(r.Good, r.System)
		retired++
	}

	return launched, retired
}

func (h *RunSitingCoordinatorHandler) actLogFields(cmd *RunSitingCoordinatorCommand, good, system string) map[string]interface{} {
	return map[string]interface{}{
		"container_id": cmd.ContainerID,
		"good":         good,
		"system":       system,
		"bead":         "sp-vdld",
	}
}

// incOutOfTopK increments and returns a chain's consecutive-ticks-out-of-top-K counter.
func (h *RunSitingCoordinatorHandler) incOutOfTopK(st *sitingCoordinatorState, key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	st.outOfTopK[key]++
	return st.outOfTopK[key]
}

// clearOutOfTopK resets a chain's out-of-top-K counter (it is desired again, or just retired).
func (h *RunSitingCoordinatorHandler) clearOutOfTopK(st *sitingCoordinatorState, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(st.outOfTopK, key)
}
