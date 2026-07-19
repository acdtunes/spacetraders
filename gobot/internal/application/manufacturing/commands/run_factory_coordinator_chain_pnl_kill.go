package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
)

// This is the chain-level P&L kill-switch that makes the portfolio self-pruning: value realizes
// through tours, not just at the factory, so a pre-spend margin check alone cannot catch a chain
// that is a hidden net loser once tour costs are counted. It is a pre-spend gate (beside the
// chain-margin guard, run_factory_coordinator Step 2.6) that auto-pauses a chain whose REALIZED
// P&L/hr over the rolling window has fallen below the kill threshold, and resumes it
// automatically (via the -1 container's re-invocation loop) when the window recovers. The P&L
// math is in services/chain_pnl.go; the DB reader is injected.
//
// FAIL OPEN, unlike the pre-spend guards: the chain-margin guard and the input-price ceiling
// fail CLOSED (can't price it → don't spend). This guard fails OPEN: an unwired reader, a
// disabled flag, an unreadable ledger, or a chain with no realized output yet all PROCEED (no
// pause). Its power is only to STOP an already-running chain, and halting production on an
// accounting outage is the worse error. It can only stop spend, never cause it.

const (
	// defaultChainPnLKillThresholdPerHour is the realized-P&L/hr floor below which a chain
	// auto-pauses (30000/hr). A 0/absent config value resolves to this at the point of use — a
	// protective default that turns the kill-switch ON, since it can only stop spend.
	defaultChainPnLKillThresholdPerHour = 30000

	// defaultChainPnLWindowHours is the trailing window the realized P&L is measured over: long
	// enough to smooth a single slow rotation, short enough to prune a genuine loser within an
	// era. 0/absent resolves to this at the point of use.
	defaultChainPnLWindowHours = 6
)

// chainPnLKillReason is the machine-readable outcome of a kill evaluation.
type chainPnLKillReason string

const (
	chainPnLProceed        chainPnLKillReason = "proceed"                   // above threshold, produce
	chainPnLDisabled       chainPnLKillReason = "kill_switch_disabled"      // off-switch (fail open)
	chainPnLReaderUnwired  chainPnLKillReason = "reader_unwired"            // optional port unset (fail open)
	chainPnLUnreadable     chainPnLKillReason = "pnl_unreadable_fail_open"  // ledger read failed (fail open)
	chainPnLNoRealization  chainPnLKillReason = "no_realization"            // no realized output yet (fail open)
	chainPnLBelowThreshold chainPnLKillReason = "chain_pnl_below_threshold" // the KILL verdict
)

// chainPnLKillVerdict is the structured, loggable result of a kill evaluation. Every number
// goes in the log message TEXT, since the container-log renderer drops metadata; the same
// fields are exposed for structured consumers.
type chainPnLKillVerdict struct {
	Killed    bool
	Reason    chainPnLKillReason
	Good      string
	Threshold int                        // resolved kill threshold (credits/hr)
	Result    mfgServices.ChainPnLResult // the computed P&L (zero-valued for the non-computed reasons)
	Detail    string                     // extra context (e.g. the ledger-read error) for fail-open
}

// SetChainPnLReader wires the DB-backed realized-P&L ledger the chain kill-switch judges. The
// daemon calls this after construction with the DB-backed reader; leaving it unset keeps the
// kill-switch fail-open (disabled), which is exactly what every non-daemon caller (the
// package's test fixtures) wants — the same setter-injection idiom as SetPriceHistoryReader /
// SetSpendLedger.
func (h *RunFactoryCoordinatorHandler) SetChainPnLReader(reader mfgServices.ChainPnLReader) {
	h.chainPnLReader = reader
}

// evaluateChainPnLKill computes this chain's realized P&L over the window and returns the kill
// verdict. It NEVER returns an error and NEVER kills on missing/blind data — every failure mode
// is a fail-open PROCEED (see file header). It emits the realized-P&L/hr gauge whenever the
// P&L is readable, so the dashboard sees exactly the number the verdict was made on.
func (h *RunFactoryCoordinatorHandler) evaluateChainPnLKill(ctx context.Context, cmd *RunFactoryCoordinatorCommand) chainPnLKillVerdict {
	v := chainPnLKillVerdict{Good: cmd.TargetGood}

	if cmd.ChainPnLKillDisabled {
		v.Reason = chainPnLDisabled
		return v
	}
	if h.chainPnLReader == nil {
		v.Reason = chainPnLReaderUnwired
		return v // fail open: guard unavailable (optional-port test-fixture contract)
	}

	windowHours := cmd.ChainPnLWindowHours
	if windowHours <= 0 {
		windowHours = defaultChainPnLWindowHours
	}
	threshold := cmd.ChainPnLKillThresholdPerHour
	if threshold <= 0 {
		threshold = defaultChainPnLKillThresholdPerHour
	}
	v.Threshold = threshold

	since := h.clock.Now().Add(-time.Duration(windowHours) * time.Hour)
	raw, err := h.chainPnLReader.ReadRealizedPnL(ctx, cmd.PlayerID, since)
	if err != nil {
		// FAIL OPEN: the kill-switch can only STOP spend, so an accounting outage must not halt
		// production. WARNING because a blind guard is an operational fault, not a routine
		// decline — greppable so a recurring read failure surfaces.
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Could not read chain P&L for %s — NOT pausing the chain (fail-open): %v",
			cmd.TargetGood, err,
		), map[string]interface{}{
			"good": cmd.TargetGood, "action": "chain_pnl_fail_open", "reason": string(chainPnLUnreadable), "error": err.Error(),
		})
		v.Reason = chainPnLUnreadable
		v.Detail = err.Error()
		return v
	}

	results := mfgServices.ComputeChainPnL(raw, float64(windowHours))
	res, ok := results[cmd.TargetGood]
	if !ok {
		// No ledger activity for this good in the window — no P&L signal to judge.
		res = mfgServices.ChainPnLResult{Good: cmd.TargetGood, WindowHours: float64(windowHours)}
	}
	v.Result = res

	// Emit the verdict-input gauge whenever P&L is readable (fresh every check).
	metrics.RecordChainPnLRealizedPerHour(cmd.TargetGood, res.NetPerHour)

	if !res.HasRealization {
		// Pre-realization chain (bought inputs, nothing realized yet): realization lags
		// production, so there is no signal to kill on. Fail open — a zero-realization
		// pure-input-bleed is prevented upstream by the chain-margin guard + demand-gated
		// harvest, so leaving it to run here cannot start an unbounded bleed.
		v.Reason = chainPnLNoRealization
		return v
	}

	if res.NetPerHour < float64(threshold) {
		v.Killed = true
		v.Reason = chainPnLBelowThreshold
		return v
	}

	v.Reason = chainPnLProceed
	return v
}

// recordChainPnLKill records a kill verdict with once-per-EPISODE dedup: on the running→paused
// transition it increments the kill counter and logs one WARNING with the numbers in the text;
// while the chain stays paused, re-checks are silent (the coordinator's backoffNoWork still
// re-logs the NoWorkReason on its own slow heartbeat). Returns whether this call was the
// transition (emitted). Keyed by ContainerID (one container = one chain).
func (h *RunFactoryCoordinatorHandler) recordChainPnLKill(ctx context.Context, cmd *RunFactoryCoordinatorCommand, v chainPnLKillVerdict) bool {
	h.chainPnLKillMu.Lock()
	already := h.chainPnLKillState[cmd.ContainerID]
	if !already {
		h.chainPnLKillState[cmd.ContainerID] = true
	}
	h.chainPnLKillMu.Unlock()

	if already {
		return false // still in the same killed episode — deduped
	}

	metrics.RecordChainPnLKill(cmd.TargetGood)
	common.LoggerFromContext(ctx).Log("WARNING", v.KillMessage(), v.logFields(cmd.ContainerID))
	return true
}

// clearChainPnLKill lifts the auto-pause on the paused→running transition (P&L recovered, or
// the switch went disabled/blind), logging one INFO with the recovered number. While the chain
// is already running it is a silent no-op. Returns whether this call was the transition.
func (h *RunFactoryCoordinatorHandler) clearChainPnLKill(ctx context.Context, cmd *RunFactoryCoordinatorCommand, v chainPnLKillVerdict) bool {
	h.chainPnLKillMu.Lock()
	wasKilled := h.chainPnLKillState[cmd.ContainerID]
	if wasKilled {
		delete(h.chainPnLKillState, cmd.ContainerID)
	}
	h.chainPnLKillMu.Unlock()

	if !wasKilled {
		return false // was already running — nothing to lift
	}

	common.LoggerFromContext(ctx).Log("INFO", v.ResumeMessage(), v.logFields(cmd.ContainerID))
	return true
}

// KillMessage renders the human/greppable pause reason with every number in the TEXT, since the
// container-log renderer drops metadata. It doubles as the response NoWorkReason.
func (v chainPnLKillVerdict) KillMessage() string {
	r := v.Result
	return fmt.Sprintf(
		"PAUSED chain %s: realized P&L %.0f/hr below kill threshold %d/hr over %.0fh — net %d (sells %d + tour %d − input %d − lift %d). Auto-paused pre-spend; resumes when realized P&L recovers (sp-rh2z).",
		v.Good, r.NetPerHour, v.Threshold, r.WindowHours, r.Net, r.FactorySell, r.TourNet, -r.FactoryInputCost, r.LiftCost,
	)
}

// ResumeMessage renders the auto-pause-lifted line (numbers in text for parity).
func (v chainPnLKillVerdict) ResumeMessage() string {
	return fmt.Sprintf(
		"RESUMED chain %s: chain P&L auto-pause lifted (%s) — realized P&L %.0f/hr, threshold %d/hr. Producing again (sp-rh2z).",
		v.Good, v.Reason, v.Result.NetPerHour, v.Threshold,
	)
}

// logFields exposes the verdict as structured metadata for consumers that keep it (mirrors the
// "factory_parked" action idiom used by the sibling guards).
func (v chainPnLKillVerdict) logFields(containerID string) map[string]interface{} {
	action := "chain_pnl_resume"
	if v.Killed {
		action = "chain_pnl_kill"
	}
	return map[string]interface{}{
		"action":             action,
		"reason":             string(v.Reason),
		"container_id":       containerID,
		"good":               v.Good,
		"net_per_hour":       v.Result.NetPerHour,
		"threshold_per_hr":   v.Threshold,
		"net":                v.Result.Net,
		"factory_sell":       v.Result.FactorySell,
		"tour_net":           v.Result.TourNet,
		"factory_input_cost": v.Result.FactoryInputCost,
		"lift_cost":          v.Result.LiftCost,
		"window_hours":       v.Result.WindowHours,
	}
}
