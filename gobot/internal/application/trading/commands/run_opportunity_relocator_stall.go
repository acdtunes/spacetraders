package commands

// run_opportunity_relocator_stall.go — the relocator's tick verdict and the two things that read it
// (sp-j1i49): the per-tick heartbeat, and the stall-escalation seam.
//
// WHY THIS EXISTS. The relocator shipped emitting NOTHING: no metric, no stall observer, and a
// per-reason skip map that nothing read. So a relocator losing every single decision was
// indistinguishable from one with nothing to do — both produce a quiet log. That is sp-biegl
// inverted: there, refusal-only ticks were filed as PROGRESS and a 28-hour placement freeze looked
// healthy; here nothing was filed at all, so a total failure looks like a rest. Both are the same
// property — a broken state that cannot be told apart from a fine one — and it is the property that
// let the claim race eat 3 of the relocator's first 4 decisions with no signal but a hand-joined
// query across daemon.log and the containers table.
//
// THE BLOCKED/IDLE BOUNDARY IS THE WHOLE DESIGN, and it is easy to get wrong in either direction:
//
//   - Too EAGER and a correctly-quiet relocator pages forever. A settled fleet where no ground clears
//     the uplift bar is the COMMON case, not a fault; escalating it produces an alarm that fires every
//     tick, gets muted, and is then ignored when it finally matters.
//   - Too SHY and the racing relocator stays invisible, which is the defect this file is for.
//
// The line drawn here: BLOCKED means a relocation was LICENSED and could not be carried out, or a
// signal the decision needed could not be read. Everything else — nothing to relocate, everything
// busy, everything protected, nothing worth moving for — is IDLE and must stay silent however long it
// persists.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// relocatorStallCoordinator names this coordinator in the stall key, metric labels and event payload.
// A stable identifier, never a formatted string.
const relocatorStallCoordinator = "opportunity_relocator"

const (
	// stallReasonRelocatorTickError is an infra fault the tick could not get past at all — the fleet
	// or the persisted intents were unreadable, so no hull was even considered. Reconcile fails closed
	// on both, which is correct, but a tick that could not read its own inputs is a block, not a rest.
	stallReasonRelocatorTickError health.StallReason = "tick_error"

	// stallReasonRelocatorCommitFailed is THE defect this slice was written for: the economics said
	// YES and the actuation said NO. It covers every way a LICENSED relocation fails to happen — the
	// hull claimed between scoring and actuation, an unprovable re-read, an unpersistable intent, or a
	// failed jump. Distinct from every economic refusal because a licensed-then-lost relocation is
	// wasted opportunity, whereas a refused one is the reconciler working.
	stallReasonRelocatorCommitFailed health.StallReason = "commit_failed"

	// stallReasonRelocatorRegionsUnreadable is the case that MASQUERADES as a poor map: hulls were
	// scored, and the ground around them could not be read. The autosizer names the identical hazard
	// in stallReasonDemandUnreadable — "distinct from 'no shortfall' precisely because it is the case
	// that MASQUERADES as no shortfall". An unreadable region set is not evidence that nowhere is
	// worth going.
	stallReasonRelocatorRegionsUnreadable health.StallReason = "regions_unreadable"
)

// relocatorCommitFailureReasons are the skip reasons that mean a LICENSED relocation did not happen.
// Listed once, read by the verdict, so the set that escalates and the set the heartbeat prints can
// never drift apart.
var relocatorCommitFailureReasons = []string{
	string(actuationTaken),
	string(actuationUnreadable),
	skipReasonIntentPersistFailed,
	skipReasonRelocateFailed,
}

const (
	// skipReasonIntentPersistFailed and skipReasonRelocateFailed name the two commit failures —
	// an unwritable intent and a failed jump — so a relocation lost to either is counted in the
	// tick result instead of leaving no trace at all.
	skipReasonIntentPersistFailed = "intent_persist_failed"
	skipReasonRelocateFailed      = "relocate_failed"
	// skipReasonRegionsUnreadable is recorded per hull whose region set could not be read.
	skipReasonRegionsUnreadable = "regions_unreadable"
	// skipReasonStillInFlight marks a relocation under way — NOT a commit failure; the move is working.
	skipReasonStillInFlight = "still_in_flight"
	// skipReasonRepositionDisabled marks the operator stand-down tick.
	skipReasonRepositionDisabled = "reposition_disabled"
)

// RelocatorMetricsSink is the counter seam (sp-j1i49 item 1). WRITE-ONLY: every method returns
// nothing, so no relocation decision can read a counter — the same discipline the stall seam keeps.
//
// It is a RELOCATOR-SPECIFIC series, deliberately, not the tour's RecordTourReposition. Two
// hull-relocating engines already got separate series with the reason written down in
// adapters/metrics/tour_metrics.go: "the legacy reposition keeps tour_repositions_total, so the two
// engines' telemetry never conflate". A third sharing the first's would contradict that one file away
// — and would not fit regardless, because this reconciler's reason vocabulary
// (claimed_at_actuation, region_rate_unreadable, within_cooldown, ...) has nothing to do with the
// tour's success|failed|no_candidate, and folding it in would also pollute that series' deliberately
// curated no_candidate denominator.
type RelocatorMetricsSink interface {
	// RecordTick counts one tick by its three-way verdict, so the REFUSAL FRACTION is computable.
	RecordTick(playerID int, verdict string)
	// RecordDecision counts one hull actually moved, by how it moved.
	RecordDecision(playerID int, outcome string)
	// RecordSkip counts one tick's exclusions for a reason. The COUNT is passed, not incremented per
	// call, because the tick already aggregated it.
	RecordSkip(playerID int, reason string, count int)
}

const (
	// relocatorDecisionRelocated and relocatorDecisionResumed name how a hull moved: a fresh decision,
	// or an interrupted move finished after a restart. Kept apart because a fleet whose only movement
	// is resumptions is not relocating anything new.
	relocatorDecisionRelocated = "relocated"
	relocatorDecisionResumed   = "resumed"
)

// SetMetricsSink wires the counter seam. Optional and nil-safe: an unwired relocator behaves exactly
// as before, because observability is never a precondition for running a tick.
func (h *RunOpportunityRelocatorHandler) SetMetricsSink(sink RelocatorMetricsSink) { h.metrics = sink }

// recordTickMetrics emits ONE tick's counters: its verdict, each hull it moved, and every exclusion
// reason with its count.
//
// This is what makes the relocator's behaviour a RATE rather than an anecdote. "3 of the first 4
// decisions lost to the claim race" had to be hand-derived by joining daemon.log against the containers
// table; with these three series it is a query, and the before/after of a mitigation is readable
// instead of re-derived.
func (h *RunOpportunityRelocatorHandler) recordTickMetrics(cmd *RunOpportunityRelocatorCommand, result *RelocatorTickResult, verdict health.TickOutcome) {
	if h.metrics == nil || result == nil {
		return
	}
	h.metrics.RecordTick(cmd.PlayerID, string(verdict.Outcome))
	for range result.Relocated {
		h.metrics.RecordDecision(cmd.PlayerID, relocatorDecisionRelocated)
	}
	for range result.Resumed {
		h.metrics.RecordDecision(cmd.PlayerID, relocatorDecisionResumed)
	}
	for reason, count := range result.Skipped {
		h.metrics.RecordSkip(cmd.PlayerID, reason, count)
	}
}

// SetStallObserver wires the coordinator-stall escalation seam, mirroring the fleet autosizer's setter
// of the same name. Optional and nil-safe: an unwired relocator behaves exactly as before, because
// observability is never a precondition for running a tick.
//
// The seam is WRITE-ONLY by type (health.StallObserver.Observe returns nothing), which is load-bearing
// rather than stylistic: no decision path can read the streak, so the escalation counter can never
// become an input to a relocation.
func (h *RunOpportunityRelocatorHandler) SetStallObserver(o health.StallObserver) { h.stall = o }

// relocatorTickVerdict maps ONE tick to its three-way verdict. PURE: it judges facts the tick already
// produced, so it cannot influence what the tick did.
//
// Order matters and each step earns its place:
//
//  1. An operator stand-down is IDLE. A kill-switch tick is an INTENDED no-op, and escalating it would
//     page continuously for exactly as long as an operator chooses to keep auto-relocation off. The
//     tour metrics make the same call in writing: the kill-switch is "NOT counted ... not [an]
//     evaluation".
//  2. A tick that could not read its inputs is BLOCKED. It never got to decide.
//  3. Any relocation or resumption is PROGRESS — even alongside a loss in the same tick. Partial
//     success clears the streak, because a relocator that IS moving hulls must not escalate merely
//     because some other hull was contended; on a busy fleet that streak would never clear.
//  4. A LICENSED relocation that did not happen is BLOCKED. The bead's case.
//  5. Ground that could not be read is BLOCKED, because it masquerades as ground not worth going to.
//  6. Everything else is IDLE, and must stay silent forever.
func relocatorTickVerdict(result *RelocatorTickResult, err error, disabled bool) health.TickOutcome {
	if disabled {
		return health.TickIdle()
	}
	if err != nil {
		return health.TickBlocked(stallReasonRelocatorTickError, err.Error())
	}
	if result == nil {
		// Reconcile can return (nil, nil) only if it were changed to; treat an absent result as
		// nothing-happened rather than panicking the reporting path.
		return health.TickIdle()
	}
	if len(result.Relocated) > 0 || len(result.Resumed) > 0 {
		return health.TickProgress()
	}
	if lost := relocatorCommitFailures(result); lost > 0 {
		return health.TickBlocked(stallReasonRelocatorCommitFailed,
			fmt.Sprintf("%d licensed relocation(s) did not happen; %s", lost, renderSkipCounts(result.Skipped)))
	}
	if result.Skipped[skipReasonRegionsUnreadable] > 0 {
		return health.TickBlocked(stallReasonRelocatorRegionsUnreadable,
			fmt.Sprintf("%d hull(s) had unreadable ground; %s", result.Skipped[skipReasonRegionsUnreadable], renderSkipCounts(result.Skipped)))
	}
	return health.TickIdle()
}

// relocatorCommitFailures counts the licensed-but-unmade relocations in one tick.
func relocatorCommitFailures(result *RelocatorTickResult) int {
	lost := 0
	for _, reason := range relocatorCommitFailureReasons {
		lost += result.Skipped[reason]
	}
	return lost
}

// observeTickStall reports the tick's verdict to the escalator EXACTLY ONCE, keyed to this coordinator
// and this container. The streak IS the tick count, so a second report inflates it and a skipped one
// stalls it — which is why this has one caller (tick) and no other path may report.
//
// Scope is deliberately empty: unlike the autosizer, which sizes several hull classes independently and
// must not let a healthy light close out a blocked heavy, the relocator makes ONE fleet-wide decision
// per tick. A per-hull scope would key the streak on a hull symbol, and a hull that stops being
// contended would silently abandon its own streak rather than clearing it.
func (h *RunOpportunityRelocatorHandler) observeTickStall(ctx context.Context, cmd *RunOpportunityRelocatorCommand, result *RelocatorTickResult, err error) {
	// Computed ONCE and shared with both consumers, so the verdict the escalator streaks on and the
	// verdict the counter labels can never disagree about the same tick.
	verdict := relocatorTickVerdict(result, err, cmd.RepositionDisabled)
	h.recordTickMetrics(cmd, result, verdict)
	if h.stall == nil {
		return
	}
	h.stall.Observe(ctx, health.StallKey{
		Coordinator: relocatorStallCoordinator,
		ContainerID: cmd.ContainerID,
		PlayerID:    cmd.PlayerID,
	}, verdict)
}

// logTickHeartbeat is the consumer RelocatorTickResult.Skipped was always documented to have and never
// had. Every exclusion the tick counted is named here, with the relocations and the evaluated count, so
// an operator can tell a REFUSAL tick from an IDLE one — the exact distinction whose absence meant "3
// of 4 decisions lost to the race" had to be hand-derived from a log/table join.
//
// The counts go in the MESSAGE TEXT, not only the metadata map, because `container logs` drops the map
// (the sp-149h/sp-iqyq renderer defect): anything that lives only in the structured fields is invisible
// to whoever is actually reading the logs.
func (h *RunOpportunityRelocatorHandler) logTickHeartbeat(ctx context.Context, cmd *RunOpportunityRelocatorCommand, result *RelocatorTickResult) {
	if result == nil {
		return
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Opportunity relocator tick: %d relocated, %d resumed, %d evaluated, %s",
		len(result.Relocated), len(result.Resumed), result.Evaluated, renderSkipCounts(result.Skipped),
	), map[string]interface{}{
		"container_id": cmd.ContainerID, "relocated": len(result.Relocated), "resumed": len(result.Resumed),
		"evaluated": result.Evaluated, "skipped": result.Skipped, "trigger": relocatorStallCoordinator,
	})
}

// renderSkipCounts renders the per-reason skip map as a stable, greppable token list. SORTED by reason:
// Go map iteration is randomised, so an unsorted render would reshuffle the line every tick and defeat
// both grep and diff.
func renderSkipCounts(skipped map[string]int) string {
	if len(skipped) == 0 {
		return "no exclusions"
	}
	reasons := make([]string, 0, len(skipped))
	for reason := range skipped {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, skipped[reason]))
	}
	return "skipped[" + strings.Join(parts, ",") + "]"
}
