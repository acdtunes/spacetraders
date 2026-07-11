package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-xdk6 (analyst redesign C4, from sp-hzz5): mechanize the export-ask-subsidy REST signal. The
// 8w40 finding is that tours paying a PREMIUM at OUR OWN markets is the leading symptom of a chain
// over-lifting its own output: our draw ladders the factory's ask above what the same good costs at
// healthy cross-source sellers, and tours (and our own C1 lifts) then pay that laddered ask — an
// inversion where we subsidize our own tours to overpay. A human used to notice it; this automates
// it. When the chain's OWN-market ask exceeds the eligible cross-source median (the SAME a5j7
// poison-proof baseline the input ceiling uses — EligibleSourceMedianAsk, reused verbatim), the
// chain RESTS one recovery window before its next lift, letting the over-drawn market regenerate.
//
// It completes the self-pruning set, and slots between its two siblings on DETECTION SIGNAL:
//
//   - the r5a6 input-pause is the INPUT side (no MODERATE+ supply source for a required input),
//   - this is the OUTPUT-LADDER side (our own output market's ask laddered above the eligible
//     median), the LEADING indicator of the same phenomenon the
//   - C2 chain-P&L kill catches LATE on the LAGGING realized-P&L symptom.
//
// PRECEDENCE. The input-pause runs FIRST (run_factory_coordinator Step 2.4, before this at Step
// 2.45): if the chain is already input-paused, the rest is moot — an input-poisoned chain isn't
// lifting anything to over-draw its output market, and the input pause is the upstream cause. The
// input-pause returns pre-spend before this guard ever evaluates, so the two never fight; each is a
// returns-early pre-spend gate keyed to its own state map. This runs BEFORE the sp-2dv4 margin
// guard and the C2 kill for the same reason the input-pause does: an over-lifted chain gets the
// proper recovery-window rest rather than the margin guard's short 45s park or a lagging P&L kill.
//
// REST ONLY on POSITIVE evidence of subsidy — the deliberate parity with the input-pause, whose
// long pause makes a false trigger expensive (a healthy chain idled for a window). It rests only
// when our own producing factory's ask is READABLE and STRICTLY exceeds a NON-EMPTY eligible
// cross-source median. It does NOT rest when the market list read fails, when our own factory can't
// be identified, or when there is no eligible (MODERATE+) cross-source baseline at all (count==0):
// the signal is DEFINED relative to a cross-source median, so with none there is simply no signal,
// and a false rest on a transient miss would idle a healthy chain. All of those fall through to
// ordinary production. RULINGS #4 (rest = the fail-safe direction on spend) is honored: this can
// only STOP a chain from lifting, never start one, and it weakens no money guard.

const (
	// defaultRestWindowMinutes is how long a chain rests before its next lift is re-attempted
	// (sp-xdk6). Keyed to the K2 rotation math from the redesign brief: chains sustain ~1 visit per
	// 1.5h recovery, so "one recovery window" for an over-drawn OUTPUT market is 90min. This is a
	// DIFFERENT quantity from the input-market regeneration half-life the input-pause uses (~194min,
	// InputRecoveryReattemptMinutes) — that is how long a SCARCE input source takes to regenerate;
	// this is the OUTPUT rotation cadence. A 0/absent config value resolves to this at the point of
	// use — a protective default that turns the rest signal ON (it can only STOP a lift, RULINGS
	// #5), so a default is correct. Config, not a constant, so the analyst retunes it live (e.g. to
	// a fitted output half-life if one is measured).
	defaultRestWindowMinutes = 90
)

// exportRestReason is the machine-readable outcome of an export-ask-subsidy evaluation.
type exportRestReason string

const (
	exportRestProceed    exportRestReason = "proceed"               // own ask at/below the eligible median, lift
	exportRestDisabled   exportRestReason = "rest_signal_disabled"  // off-switch (RULINGS #5)
	exportRestNoFactory  exportRestReason = "no_own_factory"        // own producing market unreadable — fail toward production
	exportRestNoBaseline exportRestReason = "no_eligible_baseline"  // no MODERATE+ cross-source median to judge against
	exportRestSubsidized exportRestReason = "export_ask_subsidized" // the REST verdict
)

// exportRestVerdict is the structured, loggable result of an export-ask-subsidy evaluation. Every
// number/name goes in the log message TEXT (the container-log renderer drops metadata, sp-iqyq);
// the same fields are exposed for structured consumers.
type exportRestVerdict struct {
	Rested         bool
	Reason         exportRestReason
	Good           string // output good (the chain)
	OwnAsk         int    // our producing factory's current ask for the output good
	OwnWaypoint    string // our producing factory's waypoint
	EligibleMedian int    // median ask across eligible (MODERATE+) cross-source sellers
	EligibleCount  int    // how many eligible sources formed the median
	WindowMinutes  int    // resolved rest window for this rest
}

// exportRestEntry is one container's active-rest state: when the chain may lift again and the cached
// rest line to re-report while it rests. Keyed by ContainerID (one container = one chain), the same
// singleton-across-containers reason as noWorkState / inputPauseState / chainPnLKillState.
type exportRestEntry struct {
	liftAllowedAt  time.Time
	message        string
	good           string
	ownAsk         int
	eligibleMedian int
}

// evaluateExportRest decides whether this chain's OWN output market has laddered above the eligible
// cross-source median (sp-xdk6). It NEVER returns an error and rests ONLY on positive evidence of
// subsidy — our factory's ask readable AND strictly above a non-empty eligible median (see file
// header on why every other path falls toward production). The verdict is pure; arming the recovery
// clock + the metric/log is recordExportRest's job.
func (h *RunFactoryCoordinatorHandler) evaluateExportRest(ctx context.Context, cmd *RunFactoryCoordinatorCommand, root *goods.SupplyChainNode) exportRestVerdict {
	v := exportRestVerdict{Good: cmd.TargetGood}

	if cmd.RestSignalDisabled {
		v.Reason = exportRestDisabled
		return v
	}

	// OWN-market lift ask: our producing factory's CURRENT ask, identified by the imports-input
	// identity (FindFactoryForProduction — the market that EXPORTs the output AND imports its
	// inputs), NOT the cheapest export. A laddered factory is EXPENSIVE, so "cheapest export" would
	// never surface it; keying on "the factory that consumes our delivered inputs" pins OUR market
	// even when its ask has run away above every healthy seller. directInputGoods is the good's
	// recipe inputs (the tree's direct children); an empty list still resolves to the best in-system
	// exporter for a pure-buy target.
	own, err := h.marketLocator.FindFactoryForProduction(ctx, cmd.TargetGood, directInputGoods(root), cmd.SystemSymbol, cmd.PlayerID)
	if err != nil || own == nil {
		// Can't read our own market → can't judge the ladder. Fail toward production (no rest); a
		// genuinely un-producible chain is caught by the downstream margin guard, not idled here.
		v.Reason = exportRestNoFactory
		return v
	}
	v.OwnAsk = own.Price
	v.OwnWaypoint = own.WaypointSymbol

	// Eligible cross-source median — the a5j7 poison-proof baseline, reused verbatim (sp-xdk6 does
	// NOT reinvent it): the median ask across MODERATE+ EXPORT sellers of the output good in-system.
	// A laddered own factory degrades out of MODERATE+ and drops OUT of this median, so it cannot
	// poison the baseline it is judged against.
	median, count, err := h.marketLocator.EligibleSourceMedianAsk(ctx, cmd.TargetGood, cmd.SystemSymbol, cmd.PlayerID)
	if err != nil {
		// Median read failure — NOT a subsidy signal. Fail toward production (a rest on a transient
		// blip is the expensive error, exactly as the input-pause treats a read failure).
		v.Reason = exportRestNoBaseline
		return v
	}
	v.EligibleMedian = median
	v.EligibleCount = count
	if count == 0 {
		// No eligible cross-source baseline (our factory is the only source, or every other seller is
		// depleted). The 8w40 signal is defined as a PREMIUM over eligible sources; with none, there
		// is no premium to measure — proceed.
		v.Reason = exportRestNoBaseline
		return v
	}

	if own.Price > median {
		// POSITIVE evidence of subsidy: our own market's ask is strictly above the healthy
		// cross-source median. Rest one recovery window so the over-drawn market regenerates.
		v.Rested = true
		v.Reason = exportRestSubsidized
		v.WindowMinutes = resolveRestWindowMinutes(cmd)
		return v
	}

	v.Reason = exportRestProceed
	return v
}

// resolveRestWindowMinutes resolves the rest window for a rest: the command's configured value, or
// the 90min default at the point of use for a 0/absent value (RULINGS #5).
func resolveRestWindowMinutes(cmd *RunFactoryCoordinatorCommand) int {
	if cmd.RestWindowMinutes > 0 {
		return cmd.RestWindowMinutes
	}
	return defaultRestWindowMinutes
}

// directInputGoods collects the distinct DIRECT input goods of a chain — the recipe inputs the
// producing factory imports (the root fabricate node's direct children). Deterministically ordered
// so FindFactoryForProduction's lookup and any logging are stable. A nil/childless root yields an
// empty list (a pure-buy target has no imported inputs).
func directInputGoods(root *goods.SupplyChainNode) []string {
	if root == nil {
		return nil
	}
	seen := map[string]bool{}
	var inputs []string
	for _, c := range root.Children {
		if c == nil || c.Good == "" || seen[c.Good] {
			continue
		}
		seen[c.Good] = true
		inputs = append(inputs, c.Good)
	}
	sort.Strings(inputs)
	return inputs
}

// exportRestWithinWindow reports whether a container is mid-rest (recovery window not yet elapsed),
// returning the cached rest line to re-report. TRUE means "still resting — do zero work, do not even
// read the market this tick"; the coordinator short-circuits on it. FALSE means either no active
// rest or the window has elapsed (the next lift is due).
func (h *RunFactoryCoordinatorHandler) exportRestWithinWindow(containerID string) (string, bool) {
	h.exportRestMu.Lock()
	defer h.exportRestMu.Unlock()
	entry, ok := h.exportRestState[containerID]
	if !ok {
		return "", false
	}
	if h.clock.Now().Before(entry.liftAllowedAt) {
		return entry.message, true
	}
	return "", false
}

// exportRestReattemptDelay returns how long a resting container should sleep before its next
// iteration — the remaining time until the rest window elapses — so backoffNoWork rests a chain for
// the window instead of re-polling every 45s. ok=false when the container has no active rest (use
// the normal no-work backoff) or the window has already elapsed (let the next lift run promptly).
func (h *RunFactoryCoordinatorHandler) exportRestReattemptDelay(containerID string) (time.Duration, bool) {
	h.exportRestMu.Lock()
	defer h.exportRestMu.Unlock()
	entry, ok := h.exportRestState[containerID]
	if !ok {
		return 0, false
	}
	remaining := entry.liftAllowedAt.Sub(h.clock.Now())
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// recordExportRest arms (or re-arms) the recovery window for a resting chain and emits the rest
// signal with once-per-EPISODE dedup: on the lifting→resting transition it increments the rest
// counter and logs one WARNING with the numbers in the text; a re-attempt that finds the market
// still subsidized re-arms the window but is SILENT (same episode — the chain never resumed lifting),
// mirroring the input-pause / C2 episode dedup. Returns whether this call was the lifting→resting
// transition (emitted). Keyed by ContainerID.
func (h *RunFactoryCoordinatorHandler) recordExportRest(ctx context.Context, cmd *RunFactoryCoordinatorCommand, v exportRestVerdict) bool {
	liftAllowedAt := h.clock.Now().Add(time.Duration(v.WindowMinutes) * time.Minute)

	h.exportRestMu.Lock()
	_, already := h.exportRestState[cmd.ContainerID]
	h.exportRestState[cmd.ContainerID] = &exportRestEntry{
		liftAllowedAt:  liftAllowedAt,
		message:        v.RestMessage(),
		good:           v.Good,
		ownAsk:         v.OwnAsk,
		eligibleMedian: v.EligibleMedian,
	}
	h.exportRestMu.Unlock()

	if already {
		return false // still in the same resting episode — re-armed the window, but no re-emit
	}

	metrics.RecordChainExportRest(v.Good)
	common.LoggerFromContext(ctx).Log("WARNING", v.RestMessage(), v.logFields(cmd.ContainerID))
	return true
}

// clearExportRest lifts an active rest on the resting→lifting transition (own market recovered to at
// or below the eligible median, or the signal went disabled), logging one INFO naming the recovered
// numbers. While the chain is already lifting it is a silent no-op. Returns whether this call was
// the transition.
func (h *RunFactoryCoordinatorHandler) clearExportRest(ctx context.Context, cmd *RunFactoryCoordinatorCommand) bool {
	h.exportRestMu.Lock()
	entry, was := h.exportRestState[cmd.ContainerID]
	if was {
		delete(h.exportRestState, cmd.ContainerID)
	}
	h.exportRestMu.Unlock()

	if !was {
		return false // was already lifting — nothing to lift
	}

	common.LoggerFromContext(ctx).Log("INFO", exportResumeMessage(entry), map[string]interface{}{
		"action":          "chain_export_resume",
		"reason":          string(exportRestProceed),
		"container_id":    cmd.ContainerID,
		"good":            entry.good,
		"own_ask":         entry.ownAsk,
		"eligible_median": entry.eligibleMedian,
	})
	return true
}

// RestMessage renders the human/greppable rest reason with every name/number in the TEXT (the
// container-log renderer drops metadata, sp-iqyq). It doubles as the response NoWorkReason, and is
// the Guard-1-style verdict the brief calls for: chain, own-ask, eligible-median, rest-until window.
func (v exportRestVerdict) RestMessage() string {
	return fmt.Sprintf(
		"RESTING chain %s: own-market export ask %d at %s exceeds the eligible cross-source median %d (%d healthy sources) — our own over-lifting has subsidized the market (8w40). Resting %dmin (one recovery window) before the next lift so the market regenerates; worker released, one-iteration re-attempt through the launch guards after the window (sp-xdk6).",
		v.Good, v.OwnAsk, v.OwnWaypoint, v.EligibleMedian, v.EligibleCount, v.WindowMinutes,
	)
}

// exportResumeMessage renders the rest-lifted line (recovered numbers named in text for parity).
func exportResumeMessage(entry *exportRestEntry) string {
	return fmt.Sprintf(
		"RESUMED chain %s: own-market export ask %d recovered to at or below the eligible cross-source median %d. Export-ask-subsidy rest lifted, lifting again (sp-xdk6).",
		entry.good, entry.ownAsk, entry.eligibleMedian,
	)
}

// logFields exposes the verdict as structured metadata (mirrors the "factory_parked" action idiom
// used by the sibling guards; the rest is the output-ladder sibling of chain_input_pause).
func (v exportRestVerdict) logFields(containerID string) map[string]interface{} {
	return map[string]interface{}{
		"action":          "chain_export_rest",
		"reason":          string(v.Reason),
		"container_id":    containerID,
		"good":            v.Good,
		"own_ask":         v.OwnAsk,
		"own_waypoint":    v.OwnWaypoint,
		"eligible_median": v.EligibleMedian,
		"eligible_count":  v.EligibleCount,
		"window_minutes":  v.WindowMinutes,
	}
}
