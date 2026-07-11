package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-r5a6 (Admiral order: automate the input-poison anti-cycle). The a5j7 supply-first selector
// already REFUSES a depleted input source at buy time (SCARCE/LIMITED are structurally
// unselectable), so an input-poisoned chain parks — but it parks and RE-POLLS on the short
// no-work backoff, holding its slot and re-reading the market every ~45s. The captain ran the
// recovery by hand (sp-a5j7 notes ~14:33Z): STOP the poisoned home chains outright so they
// stop polling and stop pressing the market, and re-attempt them only after the supply had
// hours to regenerate. This automates that: a pre-spend guard that PAUSES a chain whose input
// layer has gone ineligible, then holds it OFF the market for a recovery half-life before the
// one-iteration re-attempt.
//
// THREE differences from the sibling guards make this the INPUT-side automation completing the
// self-pruning set (the C2 kill-switch is the OUTPUT side, on realized P&L):
//
//  1. DETECT on SUPPLY, not price/P&L. The a5j7 doctrine is that supply is the LEADING
//     indicator (a source ladders the moment it's over-drawn; the ask is the lagging symptom).
//     Detection asks the same MODERATE+ eligibility the selector picks from
//     (EligibleSourceMedianAsk count==0) — so a chain pauses the instant its input layer has no
//     healthy in-system source, before any spend, before the P&L symptom the margin guard reads.
//
//  2. PAUSE on the RECOVERY CLOCK, not the 45s backoff. Once paused, the container sleeps until
//     a config recovery half-life (~194min, analyst-owned) before it re-reads the market at all:
//     ZERO polling and ZERO buying pressure during early recovery, because early-recovery buys
//     re-poison the just-regenerating well (the T1 finding). The re-attempt is one iteration
//     through the full launch-guard stack (this guard → the sp-2dv4 margin guard → the a5j7
//     supply-first selector at spend): a still-poisoned layer re-pauses at zero cost, a
//     recovered layer proceeds.
//
//  3. PRECEDENCE over C2. This runs BEFORE the margin guard and the C2 kill-switch, so an
//     input-poisoned chain gets the long recovery pause rather than the short margin park or a
//     realized-P&L kill. If both this and C2 would fire on one tick, this wins — it is cheaper
//     (an in-system supply read vs a realized-P&L ledger read) and it is the upstream cause (no
//     healthy inputs) of the downstream symptom (poor realized P&L). The two never fight: each
//     is a returns-early pre-spend gate keyed to its own state map.
//
// Pause ONLY on POSITIVE evidence of depletion (the deliberate distinction from the sibling
// pre-spend guards, which fail CLOSED to a short park). This guard's pause is LONG (a recovery
// half-life), so a false pause is expensive — a healthy chain idled for hours. It therefore arms
// the pause only when a required input has a READABLE in-system EXPORT source that is SCARCE/
// LIMITED (InputSourceEligibility: eligible=false AND hasReadableSource=true) — a depleted market
// that will regenerate on the half-life. It does NOT pause when the market list read fails, when a
// per-waypoint read misses, or when the good has no in-system source at all: none of those is a
// depleted-market-that-recovers (a transient miss would idle a healthy chain for hours; a
// sourceless input needs a re-site, not a wait). All of those fall through to the selector's
// ordinary production-time park — cheaply. RULINGS #4 (pause = the fail-safe direction on spend)
// is honored: this can only STOP a chain, never start one.

const (
	// defaultInputRecoveryReattemptMinutes is how long a chain stays paused before the
	// one-iteration re-attempt (sp-r5a6). Keyed to the analyst's measured input recovery
	// half-life (~194min median for a SCARCE market to regenerate to MODERATE+). A 0/absent
	// config value resolves to this at the point of use — a protective default that turns the
	// anti-cycle ON (it can only STOP spend, RULINGS #5), so a default is correct. Config, not a
	// constant, so the analyst retunes the number live.
	defaultInputRecoveryReattemptMinutes = 194
)

// inputPauseReason is the machine-readable outcome of an input-layer evaluation.
type inputPauseReason string

const (
	inputPauseProceed    inputPauseReason = "proceed"                // input layer eligible, produce
	inputPauseDisabled   inputPauseReason = "anti_cycle_disabled"    // off-switch (RULINGS #5)
	inputPauseNoInputs   inputPauseReason = "no_buy_inputs"          // nothing market-sourced to gate
	inputLayerIneligible inputPauseReason = "input_layer_ineligible" // the PAUSE verdict
)

// inputPauseVerdict is the structured, loggable result of an input-layer evaluation. Every
// number/name goes in the log message TEXT (the container-log renderer drops metadata, sp-iqyq);
// the same fields are exposed for structured consumers.
type inputPauseVerdict struct {
	Paused           bool
	Reason           inputPauseReason
	Good             string   // output good (the chain)
	BlockingInputs   []string // required BUY inputs with no MODERATE+ in-system source
	ReattemptMinutes int      // resolved recovery half-life for this pause
}

// inputPauseEntry is one container's active-pause state: when the chain may re-attempt and the
// cached pause line to re-report while it sleeps. Keyed by ContainerID (one container = one
// chain), the same singleton-across-containers reason as noWorkState / chainPnLKillState.
type inputPauseEntry struct {
	reattemptAt    time.Time
	message        string
	good           string
	blockingInputs []string
}

// evaluateInputLayerPause decides whether this chain's market-sourced input layer has gone
// ineligible (sp-r5a6). It NEVER returns an error and pauses ONLY on positive evidence of
// depletion — a required input with a readable in-system EXPORT source that is SCARCE/LIMITED
// (see file header on why an unreadable/absent source fails toward production, not the pause).
// The verdict is pure; arming the recovery clock + the metric/log is recordInputLayerPause's job.
func (h *RunFactoryCoordinatorHandler) evaluateInputLayerPause(ctx context.Context, cmd *RunFactoryCoordinatorCommand, nodes []*goods.SupplyChainNode) inputPauseVerdict {
	v := inputPauseVerdict{Good: cmd.TargetGood}

	if cmd.AntiCycleDisabled {
		v.Reason = inputPauseDisabled
		return v
	}

	inputs := buyInputGoods(nodes, cmd.TargetGood)
	if len(inputs) == 0 {
		// A pure-buy or input-free tree has no market-sourced input layer to gate — the margin
		// guard's chainGuardNoFabrication path owns those. Nothing to pause on.
		v.Reason = inputPauseNoInputs
		return v
	}

	var blocking []string
	for _, good := range inputs {
		eligible, hasReadableSource, err := h.marketLocator.InputSourceEligibility(ctx, good, cmd.SystemSymbol, cmd.PlayerID)
		if err != nil {
			// Market-list read failure — NOT an ineligibility signal. Do not pause on a transient
			// read blip (the long-pause false-positive is the expensive error); the margin guard's
			// fail-closed park covers a genuinely unpriceable chain one step downstream.
			continue
		}
		if eligible || !hasReadableSource {
			// eligible: a MODERATE+ source exists → produce. !hasReadableSource: no EXPORT source
			// for this good was readable in-system — a cold/partial cache OR a good with no
			// in-system source at all; NEITHER is a depleted-market-that-regenerates, so it must
			// not arm the recovery pause (a transient miss would idle a healthy chain for hours;
			// a sourceless input needs a re-site, not a wait). Both fall through to the selector's
			// ordinary production-time park.
			continue
		}
		// POSITIVE evidence of depletion: readable EXPORT source(s) exist, every one SCARCE/LIMITED.
		// One blocked required input blocks the whole chain — a factory can't produce without it.
		blocking = append(blocking, good)
	}

	if len(blocking) > 0 {
		v.Paused = true
		v.Reason = inputLayerIneligible
		v.BlockingInputs = blocking
		v.ReattemptMinutes = resolveReattemptMinutes(cmd)
		return v
	}

	v.Reason = inputPauseProceed
	return v
}

// resolveReattemptMinutes resolves the recovery half-life for a pause: the command's configured
// value, or the 194min default at the point of use for a 0/absent value (RULINGS #5).
func resolveReattemptMinutes(cmd *RunFactoryCoordinatorCommand) int {
	if cmd.InputRecoveryReattemptMinutes > 0 {
		return cmd.InputRecoveryReattemptMinutes
	}
	return defaultInputRecoveryReattemptMinutes
}

// buyInputGoods collects the distinct market-sourced (BUY-leaf) input goods in a dependency
// tree, excluding the chain's own output good (defensive — a resale root is a FABRICATE node,
// never a BUY leaf). Deterministically ordered so the pause message is stable.
func buyInputGoods(nodes []*goods.SupplyChainNode, targetGood string) []string {
	seen := map[string]bool{}
	var inputs []string
	for _, n := range nodes {
		if n == nil || !n.IsLeaf() || n.AcquisitionMethod != goods.AcquisitionBuy {
			continue
		}
		if n.Good == targetGood || seen[n.Good] {
			continue
		}
		seen[n.Good] = true
		inputs = append(inputs, n.Good)
	}
	sort.Strings(inputs)
	return inputs
}

// inputPauseWithinWindow reports whether a container is mid-pause (recovery clock not yet
// elapsed), returning the cached pause line to re-report. TRUE means "still recovering — do
// zero work, do not even read the market this tick"; the coordinator short-circuits on it. FALSE
// means either no active pause or the clock has elapsed (the re-attempt is due).
func (h *RunFactoryCoordinatorHandler) inputPauseWithinWindow(containerID string) (string, bool) {
	h.inputPauseMu.Lock()
	defer h.inputPauseMu.Unlock()
	entry, ok := h.inputPauseState[containerID]
	if !ok {
		return "", false
	}
	if h.clock.Now().Before(entry.reattemptAt) {
		return entry.message, true
	}
	return "", false
}

// inputPauseReattemptDelay returns how long a paused container should sleep before its next
// iteration — the remaining time until the recovery clock elapses — so backoffNoWork rests a
// paused chain for the half-life instead of re-polling every 45s. ok=false when the container
// has no active pause (use the normal no-work backoff) or the clock has already elapsed (let the
// re-attempt run promptly).
func (h *RunFactoryCoordinatorHandler) inputPauseReattemptDelay(containerID string) (time.Duration, bool) {
	h.inputPauseMu.Lock()
	defer h.inputPauseMu.Unlock()
	entry, ok := h.inputPauseState[containerID]
	if !ok {
		return 0, false
	}
	remaining := entry.reattemptAt.Sub(h.clock.Now())
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// recordInputLayerPause arms (or re-arms) the recovery clock for a paused chain and emits the
// pause signal with once-per-EPISODE dedup: on the running→paused transition it increments the
// pause counter and logs one WARNING with the numbers in the text; a re-attempt that finds the
// layer still ineligible re-arms the clock but is SILENT (same episode — the chain never
// resumed), mirroring the C2 kill-switch's episode dedup. Returns whether this call was the
// running→paused transition (emitted). Keyed by ContainerID.
func (h *RunFactoryCoordinatorHandler) recordInputLayerPause(ctx context.Context, cmd *RunFactoryCoordinatorCommand, v inputPauseVerdict) bool {
	reattemptAt := h.clock.Now().Add(time.Duration(v.ReattemptMinutes) * time.Minute)

	h.inputPauseMu.Lock()
	_, already := h.inputPauseState[cmd.ContainerID]
	h.inputPauseState[cmd.ContainerID] = &inputPauseEntry{
		reattemptAt:    reattemptAt,
		message:        v.PauseMessage(),
		good:           v.Good,
		blockingInputs: v.BlockingInputs,
	}
	h.inputPauseMu.Unlock()

	if already {
		return false // still in the same paused episode — re-armed the clock, but no re-emit
	}

	metrics.RecordChainInputPause(v.Good)
	common.LoggerFromContext(ctx).Log("WARNING", v.PauseMessage(), v.logFields(cmd.ContainerID))
	return true
}

// clearInputLayerPause lifts an active pause on the paused→running transition (input layer
// recovered, or the anti-cycle went disabled), logging one INFO naming the recovered inputs.
// While the chain is already running it is a silent no-op. Returns whether this call was the
// transition.
func (h *RunFactoryCoordinatorHandler) clearInputLayerPause(ctx context.Context, cmd *RunFactoryCoordinatorCommand) bool {
	h.inputPauseMu.Lock()
	entry, was := h.inputPauseState[cmd.ContainerID]
	if was {
		delete(h.inputPauseState, cmd.ContainerID)
	}
	h.inputPauseMu.Unlock()

	if !was {
		return false // was already running — nothing to lift
	}

	common.LoggerFromContext(ctx).Log("INFO", resumeMessage(entry), map[string]interface{}{
		"action":          "chain_input_resume",
		"reason":          string(inputPauseProceed),
		"container_id":    cmd.ContainerID,
		"good":            entry.good,
		"recovered_input": entry.blockingInputs,
	})
	return true
}

// PauseMessage renders the human/greppable pause reason with every name/number in the TEXT (the
// container-log renderer drops metadata, sp-iqyq). It doubles as the response NoWorkReason.
func (v inputPauseVerdict) PauseMessage() string {
	return fmt.Sprintf(
		"PAUSED chain %s: input layer ineligible — no MODERATE+ supply source in-system for [%s]. Anti-cycle holds the chain OFF the market for %dmin (recovery half-life) so early-recovery buys can't re-poison the well; worker released, one-iteration re-attempt through the launch guards after the clock (sp-r5a6).",
		v.Good, strings.Join(v.BlockingInputs, ", "), v.ReattemptMinutes,
	)
}

// resumeMessage renders the pause-lifted line (recovered inputs named in text for parity).
func resumeMessage(entry *inputPauseEntry) string {
	return fmt.Sprintf(
		"RESUMED chain %s: input layer recovered — MODERATE+ supply restored for previously-blocked [%s]. Anti-cycle pause lifted, producing again (sp-r5a6).",
		entry.good, strings.Join(entry.blockingInputs, ", "),
	)
}

// logFields exposes the verdict as structured metadata (mirrors the "factory_parked" action
// idiom used by the sibling guards; the pause is the input-side sibling of chain_pnl_kill).
func (v inputPauseVerdict) logFields(containerID string) map[string]interface{} {
	return map[string]interface{}{
		"action":            "chain_input_pause",
		"reason":            string(v.Reason),
		"container_id":      containerID,
		"good":              v.Good,
		"blocking_inputs":   v.BlockingInputs,
		"reattempt_minutes": v.ReattemptMinutes,
	}
}
