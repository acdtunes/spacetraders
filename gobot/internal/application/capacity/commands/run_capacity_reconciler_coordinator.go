// Package commands: the capacity reconciler's reconcile-loop coordinator
// (design spec docs/superpowers/specs/2026-07-15-capacity-reconciler-design.md).
//
// A standing daemon coordinator that continuously drives the contract-delivery
// machine's ACTUAL capacity topology toward a computed DESIRED topology,
// maximizing per-hull-sustained credits/hr with cycle-time as the lever. Each
// tick runs SENSE → PLAN → DIFF → GOVERN → CONVERGE through the
// dependency-injected components of internal/domain/capacity. The loop is
// STATELESS PER TICK — desired state is recomputed from live state every pass
// — so it is idempotent, restart-safe, and self-healing: a failed action or a
// drifted hull simply reappears as gap on the next pass.
//
// The production wiring (main.go) is the FULLY ARMED engine — real
// SENSE/PLAN/DIFF/GOVERN components and a cheap-tier actuator (tier-1 reassign,
// tier-2 reposition + worker-rebalance, tier-3 depot buffer writes); only tier-4
// capital stays gated behind the human-approved proposal path. It is DEPLOY-INERT
// only in that it is NOT boot-standing-armed (contrast: the market-freshness sizer
// in bootStandingCoordinatorTypes); it runs only when explicitly started via
// `spacetraders workflow capacity-reconciler` / the CapacityReconcilerCoordinator
// RPC, and then survives restarts through the persisted-container recovery idiom
// (RULINGS #2). Stopping it is a complete decommission (sp-2jrz): the stop reaps its
// buffer containers and releases their role-fleet dedications back to the pool.
//
// The captain/DISABLED kill switch is honored at the TOP OF EVERY TICK, not
// just at startup: an engaged switch idles the tick without invoking a single
// phase component — the exact mechanism the watchkeeper supervisor's Tick
// uses (internal/captain/workspace.go, wired here as capacity.KillSwitch).
package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunCapacityReconcilerCoordinatorCommand launches the standing capacity
// reconciler for a player. Like the other standing coordinators it runs an
// infinite reconcile loop inside a single Handle() call. Every calibration
// knob is a launch-config key (RULINGS #5); a zero value falls back to the
// documented default in capacity.DefaultCalibration, and an invalid explicit
// value fails the launch loudly.
type RunCapacityReconcilerCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string

	// TickIntervalSecs is the reconcile cadence. 0 → 300s.
	TickIntervalSecs int

	// DryRun runs the loop observe-only: SENSE/PLAN/DIFF/GOVERN execute exactly
	// as normal, but CONVERGE actuates NOTHING — it calls no actuator verb and
	// files no proposal. Instead it LOGS what it WOULD do and records the planned
	// set on each TickOutcome (WouldExecute / WouldFile), so a captain can watch a
	// live cycle before arming the engine. NOT dark-shipping: every skipped
	// decision is logged loudly per tick.
	//
	// CAVEAT for a future arming lane: GOVERN still runs under DryRun, and with the
	// thin capex emitter that means EmitCapitalDemand STILL publishes
	// contract-delivery demand to the autosizer's bridge every tick (DryRun gates
	// only CONVERGE, not the emit — see capex_emitter.go Govern). DryRun is
	// therefore NOT the thing keeping capital from being bought; the DORMANT
	// contract_delivery class is (it stays inert until an arming lane wires it
	// into the autosizer's classDisabled / guards).
	DryRun bool

	// Calibration params (spec: Calibration section). 0 → documented default.
	ReserveFloorCredits      int64   // hard treasury floor; 0 → 50000
	SurplusFraction          float64 // f in deployable = f × (treasury − floor); 0 → 0.25
	PerDecisionCapPct        int     // single-decision % of deployable; 0 → 25
	ROIPaybackHorizonHours   float64 // capital payback window; 0 → 24h
	AddThresholdPerHullCrHr  float64 // per-hull $/hr floor for adds; 0 → none
	StockerCapacityBudget    int     // per-hub stocker budget; 0 → planner default
	ApprovalThresholdCredits int64   // tier-4 cost needing approval; 0 → ALL tier-4
}

// RunCapacityReconcilerCoordinatorResponse reports reconcile progress. Because
// the loop is infinite it is only observed on context cancellation (shutdown).
type RunCapacityReconcilerCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunCapacityReconcilerCoordinatorHandler owns the reconcile loop. It is a
// registered singleton holding no per-player mutable state — every decision is
// derived fresh from the injected components each pass (RULINGS #2).
type RunCapacityReconcilerCoordinatorHandler struct {
	domain     capacity.CapacityDomain
	differ     capacity.Differ
	governor   capacity.Governor
	actuator   capacity.Actuator
	proposals  capacity.ProposalChannel
	killSwitch capacity.KillSwitch
	clock      shared.Clock

	// observer receives every tick's outcome — the harness/scenario seam.
	// Optional; production runs without one.
	observer capacity.TickObserver

	// captainEvents emits the coordinator error-loop event when a reconcile
	// pass fails identically for DefaultStreakThreshold consecutive ticks.
	// Optional-injection.
	captainEvents captain.EventRecorder

	// graduation reports the durable per-player era-scoped contract-graduation flag (sp-difa.1).
	// This reconciler is CONTRACT-DELIVERY-domain-only, so when a player is graduated the whole tick
	// idles cleanly — no contract-delivery desired topology, no idle-hull reassignment — which is the
	// durable fix for the boot-standing relaunch re-stranding hulls from contract HISTORY after a manual
	// decommission. Optional-injection; nil (or a read error) ⇒ NOT graduated ⇒ reconcile as today
	// (fail-OPEN, byte-identical). It does NOT touch trade — the trade fleet + autosizer LIGHTS class
	// are a different domain, unaffected by idling this reconciler.
	graduation capacity.ContractGraduationReader
}

// NewRunCapacityReconcilerCoordinatorHandler wires the loop. clock defaults to
// the real clock when nil (production). A nil killSwitch is treated as ENGAGED
// (fail-closed): a mis-wired engine idles rather than running unsupervised.
func NewRunCapacityReconcilerCoordinatorHandler(
	domain capacity.CapacityDomain,
	differ capacity.Differ,
	governor capacity.Governor,
	actuator capacity.Actuator,
	proposals capacity.ProposalChannel,
	killSwitch capacity.KillSwitch,
	clock shared.Clock,
) *RunCapacityReconcilerCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunCapacityReconcilerCoordinatorHandler{
		domain:     domain,
		differ:     differ,
		governor:   governor,
		actuator:   actuator,
		proposals:  proposals,
		killSwitch: killSwitch,
		clock:      clock,
	}
}

// SetTickObserver wires the per-tick outcome observer (the harness seam).
// Call before Handle; the loop reads it without further synchronization.
func (h *RunCapacityReconcilerCoordinatorHandler) SetTickObserver(o capacity.TickObserver) {
	h.observer = o
}

// SetEventRecorder wires the captain outbox for the reconcile error-loop event.
func (h *RunCapacityReconcilerCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetContractGraduationReader wires the durable per-player era-scoped contract-graduation read
// (sp-difa.1). Call before Handle; the loop reads it at the top of every tick. Left unset (nil),
// the reconciler never idles for graduation — byte-identical to pre-sp-difa.1.
func (h *RunCapacityReconcilerCoordinatorHandler) SetContractGraduationReader(r capacity.ContractGraduationReader) {
	h.graduation = r
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunCapacityReconcilerCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunCapacityReconcilerCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	if err := h.validateWiring(); err != nil {
		return nil, err
	}
	cal, err := resolveCalibration(cmd)
	if err != nil {
		return nil, fmt.Errorf("capacity reconciler calibration invalid: %w", err)
	}

	result := &RunCapacityReconcilerCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Capacity reconciler starting (domain %s, tick %s, per-decision cap %d%%)", h.domain.Name(), cal.TickInterval, cal.PerDecisionCapPct), map[string]interface{}{
		"action":       "capacity_reconciler_start",
		"container_id": cmd.ContainerID,
		"domain":       h.domain.Name(),
	})

	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	seq := 0

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		seq++
		outcome := h.reconcileTick(ctx, cmd, cal, seq)
		h.noteOutcome(ctx, cmd, errMon, outcome)
		if h.observer != nil {
			h.observer.ObserveTick(outcome)
		}
		result.Ticks++
		if outcome.Error != "" {
			result.Errors = append(result.Errors, outcome.Error)
		}

		h.sleepTick(ctx, cal.TickInterval)
	}
}

// validateWiring refuses to run a partially-assembled engine — fail loud at
// launch, never dark at converge time.
func (h *RunCapacityReconcilerCoordinatorHandler) validateWiring() error {
	missing := []string{}
	if h.domain == nil {
		missing = append(missing, "domain")
	}
	if h.differ == nil {
		missing = append(missing, "differ")
	}
	if h.governor == nil {
		missing = append(missing, "governor")
	}
	if h.actuator == nil {
		missing = append(missing, "actuator")
	}
	if h.proposals == nil {
		missing = append(missing, "proposal channel")
	}
	if len(missing) > 0 {
		return fmt.Errorf("capacity reconciler not wired: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// resolveCalibration merges the launch config over the documented defaults
// and validates the result. A zero field defers to the default (RULINGS #5);
// an invalid explicit value is an error, never a silent clamp.
func resolveCalibration(cmd *RunCapacityReconcilerCoordinatorCommand) (capacity.Calibration, error) {
	cal := capacity.DefaultCalibration()
	if cmd.TickIntervalSecs != 0 {
		cal.TickInterval = time.Duration(cmd.TickIntervalSecs) * time.Second
	}
	if cmd.ReserveFloorCredits != 0 {
		cal.ReserveFloorCredits = cmd.ReserveFloorCredits
	}
	if cmd.SurplusFraction != 0 {
		cal.SurplusFraction = cmd.SurplusFraction
	}
	if cmd.PerDecisionCapPct != 0 {
		cal.PerDecisionCapPct = cmd.PerDecisionCapPct
	}
	if cmd.ROIPaybackHorizonHours != 0 {
		cal.ROIPaybackHorizon = time.Duration(cmd.ROIPaybackHorizonHours * float64(time.Hour))
	}
	if cmd.AddThresholdPerHullCrHr != 0 {
		cal.AddThresholdPerHullCrHr = cmd.AddThresholdPerHullCrHr
	}
	if cmd.StockerCapacityBudget != 0 {
		cal.StockerCapacityBudget = cmd.StockerCapacityBudget
	}
	if cmd.ApprovalThresholdCredits != 0 {
		cal.ApprovalThresholdCredits = cmd.ApprovalThresholdCredits
	}
	return cal, cal.Validate()
}

// reconcileTick is one SENSE → PLAN → DIFF → GOVERN → CONVERGE pass — the unit
// the tests drive. A failing phase ends the tick (outcome carries which and
// why); the loop itself never stops.
func (h *RunCapacityReconcilerCoordinatorHandler) reconcileTick(ctx context.Context, cmd *RunCapacityReconcilerCoordinatorCommand, cal capacity.Calibration, seq int) capacity.TickOutcome {
	outcome := capacity.TickOutcome{Sequence: seq, At: h.clock.Now()}

	// captain/DISABLED, re-read EVERY tick: engaged (or unwired) ⇒ idle
	// without invoking a single phase component.
	if h.killSwitch == nil || h.killSwitch.Disabled() {
		outcome.Idle = true
		return outcome
	}

	// Contract graduation (sp-difa.1), re-read EVERY tick after the kill switch: a graduated player has
	// DURABLY retired the contract-delivery op (the operator's manual decision, persisted era-scoped). This
	// reconciler is contract-delivery-domain-only, so idling the whole tick emits NO contract-delivery
	// desired topology and does NO idle-hull reassignment — the durable fix for the boot-standing relaunch
	// re-stranding hulls from contract HISTORY. Fail-OPEN: a nil reader or a read error is treated as
	// UN-graduated, so the reconciler runs exactly as today (a mis-wire / transient DB hiccup never
	// silently suppresses the funding floor). Trade is untouched — a different domain.
	if h.graduation != nil {
		if graduated, gerr := h.graduation.IsContractGraduated(ctx, cmd.PlayerID.Value()); gerr == nil && graduated {
			outcome.Idle = true
			outcome.Graduated = true
			common.LoggerFromContext(ctx).Log("INFO", "Capacity reconciler idle: player is contract-graduated — contract-delivery reconciliation OFF (durable, sp-difa.1); trade + lights unaffected", map[string]interface{}{
				"action":       "capacity_reconciler_contract_graduated_idle",
				"container_id": cmd.ContainerID,
				"tick":         seq,
			})
			return outcome
		}
	}

	signals, err := h.domain.Sensor().Sense(ctx, cmd.PlayerID.Value())
	if err != nil {
		return failTick(outcome, capacity.PhaseSense, err)
	}
	desired, err := h.domain.Planner().ComputeDesired(ctx, signals, cal)
	if err != nil {
		return failTick(outcome, capacity.PhasePlan, err)
	}
	actions, err := h.differ.Diff(ctx, desired, signals.Topology, cal)
	if err != nil {
		return failTick(outcome, capacity.PhaseDiff, err)
	}
	governed, err := h.governor.Govern(ctx, actions, signals.Economics, cal)
	if err != nil {
		return failTick(outcome, capacity.PhaseGovern, err)
	}
	h.converge(ctx, cmd.PlayerID.Value(), cal, governed, cmd.DryRun, &outcome)
	return outcome
}

func failTick(outcome capacity.TickOutcome, phase capacity.Phase, err error) capacity.TickOutcome {
	outcome.FailedPhase = phase
	outcome.Error = err.Error()
	return outcome
}

// converge executes the approved actions through the actuator (each verb
// dispatched by its tier) and files the proposals. Per-item failures are
// collected and reported but never abort the rest — statelessness means a
// failed item reappears as gap next tick and is re-converged.
//
// DryRun short-circuits to observeConverge: SENSE/PLAN/DIFF/GOVERN already ran
// (all read-only), but here NOTHING is actuated and NO proposal is filed — the
// observer logs what it WOULD do and records the planned set on the outcome
// instead. The armed path below is byte-identical when DryRun is off.
//
// Two structural backstops live here, independent of the governor's
// correctness (safety invariant 4 must never rest on a single component):
//
//   - An Approved tier-4 action costing >= cal.ApprovalThresholdCredits is
//     REFUSED (recorded as a CONVERGE failure), never executed — a governor
//     contradicting its own gate is loud, not a silent treasury drain. Under
//     the v1 default threshold (0) NO tier-4 action can execute from Approved;
//     under a raised threshold, graduated auto-approval below it passes.
//   - A proposal the governor left unattributed (PlayerID zero — its Govern
//     inputs carry no player identity) is stamped with the reconciling
//     player's ID before Submit; a non-zero PlayerID passes verbatim.
func (h *RunCapacityReconcilerCoordinatorHandler) converge(ctx context.Context, playerID int, cal capacity.Calibration, governed capacity.GovernResult, dryRun bool, outcome *capacity.TickOutcome) {
	if dryRun {
		h.observeConverge(ctx, playerID, cal, governed, outcome)
		return
	}
	failures := []string{}
	for _, action := range governed.Approved {
		if exceedsApprovalGate(action, cal) {
			failures = append(failures, fmt.Sprintf(
				"unapproved capital refused: %s %s cost %d >= approval threshold %d (invariant 4: tier-4 executes only via an approved proposal)",
				action.Tier, action.Verb, action.EstimatedCostCredits, cal.ApprovalThresholdCredits))
			continue
		}
		if err := h.executeAction(ctx, action); err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: %v", action.Tier, action.Verb, err))
			continue
		}
		outcome.ActionsExecuted = append(outcome.ActionsExecuted, action)
	}
	for _, proposal := range governed.Proposals {
		if proposal.PlayerID == 0 {
			proposal.PlayerID = playerID
		}
		if err := h.proposals.Submit(ctx, proposal); err != nil {
			failures = append(failures, fmt.Sprintf("proposal %s: %v", proposal.ID, err))
			continue
		}
		outcome.ProposalsFiled = append(outcome.ProposalsFiled, proposal)
	}
	if len(failures) > 0 {
		outcome.FailedPhase = capacity.PhaseConverge
		outcome.Error = strings.Join(failures, "; ")
	}
}

// verbTiers pins the documented verb → canonical-tier mapping (action.go:
// "each verb maps to exactly one Actuator method (by its tier)"). Dispatch
// verifies it: a mislabeled tier (e.g. buy_hull claiming tier-2) would
// otherwise sail past the capital gate as a free cheap-tier action.
var verbTiers = map[capacity.ActionVerb]capacity.Tier{
	capacity.VerbReassignHull:          capacity.TierReuseIdle,
	capacity.VerbRepositionHull:        capacity.TierRebalance,
	capacity.VerbRebalanceWorkers:      capacity.TierRebalance,
	capacity.VerbAdjustBufferWhitelist: capacity.TierBufferAdjust,
	capacity.VerbAdjustBufferCap:       capacity.TierBufferAdjust,
	capacity.VerbAddCluster:            capacity.TierCapital,
	capacity.VerbBuyHull:               capacity.TierCapital,
}

// executeAction dispatches one approved action to the actuator verb its tier
// owns, after verifying the verb/tier pairing is the canonical one — tier
// mislabeling is the cheapest way to defeat the escalation ladder, so it is
// refused loudly instead of dispatched. Tier 4 reaches ExecuteCapital only
// past converge's approval-threshold backstop.
func (h *RunCapacityReconcilerCoordinatorHandler) executeAction(ctx context.Context, action capacity.Action) error {
	if err := verbTierError(action); err != nil {
		return err
	}
	switch action.Tier {
	case capacity.TierReuseIdle:
		return h.actuator.ReuseIdleHull(ctx, action)
	case capacity.TierRebalance:
		return h.actuator.Rebalance(ctx, action)
	case capacity.TierBufferAdjust:
		return h.actuator.AdjustBuffer(ctx, action)
	case capacity.TierCapital:
		return h.actuator.ExecuteCapital(ctx, action)
	}
	return fmt.Errorf("unknown action tier %d (%s)", action.Tier, action.Verb)
}

// exceedsApprovalGate reports whether an Approved action is a tier-4 capital
// action at or over the approval threshold — the structural invariant-4 backstop
// (CONTRACTS safety invariant 4): such an action must NEVER execute from Approved
// (only via an approved proposal). Shared by armed CONVERGE (which records it as a
// failure) and the DryRun observer (which flags it as a would-be refusal), so the
// observed plan can never claim to execute what the engine would refuse.
func exceedsApprovalGate(action capacity.Action, cal capacity.Calibration) bool {
	return action.Tier == capacity.TierCapital && action.EstimatedCostCredits >= cal.ApprovalThresholdCredits
}

// verbTierError validates one action's verb against the canonical verb→tier
// mapping (verbTiers): an unknown verb, or a verb labeled with the wrong tier, is
// refused so tier mislabeling cannot smuggle a capital action past the escalation
// ladder as a "free" cheap one. nil ⇒ the pairing is canonical. Shared by the
// armed dispatch (executeAction) and the DryRun observer so both judge the verb
// identically.
func verbTierError(action capacity.Action) error {
	canonical, known := verbTiers[action.Verb]
	if !known {
		return fmt.Errorf("unknown action verb %q (tier %s) — refusing dispatch", action.Verb, action.Tier)
	}
	if canonical != action.Tier {
		return fmt.Errorf("verb/tier mismatch: %s is canonically %s but the action claims %s — refused (a mislabeled tier would bypass the capital gate)",
			action.Verb, canonical, action.Tier)
	}
	return nil
}

// observeConverge is CONVERGE's DryRun twin. It makes every decision armed
// CONVERGE would — the invariant-4 capital backstop (exceedsApprovalGate) and the
// canonical verb/tier check (verbTierError) still judge each approved action, so
// the observed WouldExecute set never claims the engine would execute what it
// would in fact refuse — but instead of calling an actuator verb or
// ProposalChannel.Submit it LOGS the decision and records the planned set on the
// outcome (WouldExecute / WouldFile). NOT dark-shipping: every decision is logged
// loudly (a would-be refusal is a WARNING). No side effect leaves the process and
// FailedPhase stays clear — observing is not failing.
func (h *RunCapacityReconcilerCoordinatorHandler) observeConverge(ctx context.Context, playerID int, cal capacity.Calibration, governed capacity.GovernResult, outcome *capacity.TickOutcome) {
	for _, action := range governed.Approved {
		if exceedsApprovalGate(action, cal) {
			h.logWouldRefuse(ctx, action, outcome, fmt.Sprintf("unapproved capital (cost %d >= approval threshold %d)", action.EstimatedCostCredits, cal.ApprovalThresholdCredits))
			continue
		}
		if err := verbTierError(action); err != nil {
			h.logWouldRefuse(ctx, action, outcome, err.Error())
			continue
		}
		h.logWouldExecute(ctx, action, outcome)
	}
	for _, proposal := range governed.Proposals {
		// Stamp the reconciling player exactly as the armed path would, so the
		// observed proposal is what WOULD actually be filed (Submit never sees a
		// zero player).
		if proposal.PlayerID == 0 {
			proposal.PlayerID = playerID
		}
		h.logWouldFile(ctx, proposal, outcome)
	}
}

// logWouldExecute records — on the outcome and in the log — one approved action a
// DryRun tick WOULD have executed, in the canonical shape
// "DRY-RUN would <tier> <verb> <hub/ship/good> (est cost <n>)".
func (h *RunCapacityReconcilerCoordinatorHandler) logWouldExecute(ctx context.Context, action capacity.Action, outcome *capacity.TickOutcome) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("DRY-RUN would %s %s %s (est cost %d)",
		action.Tier, action.Verb, actionTarget(action), action.EstimatedCostCredits), map[string]interface{}{
		"action": "capacity_reconciler_dry_run_would_execute", "tick": outcome.Sequence,
		"tier": action.Tier.String(), "verb": string(action.Verb), "target": actionTarget(action),
	})
	outcome.WouldExecute = append(outcome.WouldExecute, action)
}

// logWouldFile records one capital proposal a DryRun tick WOULD have filed, with
// its ROI evidence, in the shape
// "DRY-RUN would file proposal <verb> <hub/ship/good> (ROI evidence: ...)".
func (h *RunCapacityReconcilerCoordinatorHandler) logWouldFile(ctx context.Context, proposal capacity.Proposal, outcome *capacity.TickOutcome) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("DRY-RUN would file proposal %s %s (ROI evidence: cost %d, +%.0f cr/hr, payback %s; %s)",
		proposal.Action.Verb, actionTarget(proposal.Action),
		proposal.Evidence.CostCredits, proposal.Evidence.ProjectedGainPerHour, proposal.Evidence.PaybackHorizon, proposal.Evidence.Narrative), map[string]interface{}{
		"action": "capacity_reconciler_dry_run_would_file", "tick": outcome.Sequence,
		"proposal_id": proposal.ID, "verb": string(proposal.Action.Verb),
	})
	outcome.WouldFile = append(outcome.WouldFile, proposal)
}

// logWouldRefuse warns that a DryRun tick WOULD have refused an approved action
// the governor should never have approved (invariant-4 capital, or a mislabeled
// verb) — surfacing the would-be refusal to the observer without letting it land
// in WouldExecute. Deliberately a WARNING: a governor contradicting its own gate
// is loud even in observe mode.
func (h *RunCapacityReconcilerCoordinatorHandler) logWouldRefuse(ctx context.Context, action capacity.Action, outcome *capacity.TickOutcome, reason string) {
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("DRY-RUN would REFUSE %s %s %s: %s",
		action.Tier, action.Verb, actionTarget(action), reason), map[string]interface{}{
		"action": "capacity_reconciler_dry_run_would_refuse", "tick": outcome.Sequence,
		"tier": action.Tier.String(), "verb": string(action.Verb),
	})
}

// actionTarget renders an action's primary subject — the hub, ship, and/or good
// it touches — as "hub/ship/good" for the DryRun log (unused fields stay out; a
// target-less action reads "-").
func actionTarget(action capacity.Action) string {
	parts := []string{}
	if action.HubSymbol != "" {
		parts = append(parts, action.HubSymbol)
	}
	if action.ShipSymbol != "" {
		parts = append(parts, action.ShipSymbol)
	}
	if action.Good != "" {
		parts = append(parts, action.Good)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "/")
}

// noteOutcome logs the tick and records it at the error-streak checkpoint: a
// clean tick resets the streak; an identical error repeating for
// DefaultStreakThreshold passes emits the coordinator error-loop captain
// event. Edge-triggered and nil-safe on the recorder.
func (h *RunCapacityReconcilerCoordinatorHandler) noteOutcome(ctx context.Context, cmd *RunCapacityReconcilerCoordinatorCommand, errMon *health.Monitor, outcome capacity.TickOutcome) {
	logger := common.LoggerFromContext(ctx)
	if outcome.Idle {
		// Diagnose the idle honestly: a nil switch is UNWIRED (fail-closed),
		// not an engaged DISABLED file — don't send an operator hunting for a
		// file that does not exist.
		msg := "Capacity reconciler idle: captain/DISABLED engaged"
		if h.killSwitch == nil {
			msg = "Capacity reconciler idle: kill switch UNWIRED — failing closed (wire capacity.KillSwitch)"
		}
		logger.Log("INFO", msg, map[string]interface{}{
			"action": "capacity_reconciler_idle", "tick": outcome.Sequence,
		})
		return
	}
	if outcome.Error != "" {
		logger.Log("ERROR", fmt.Sprintf("Capacity reconcile tick %d failed at %s: %s", outcome.Sequence, outcome.FailedPhase, outcome.Error), map[string]interface{}{
			"action": "capacity_reconciler_tick_failed", "tick": outcome.Sequence, "phase": string(outcome.FailedPhase),
		})
	} else {
		msg := fmt.Sprintf("Capacity reconcile tick %d: %d actions executed, %d proposals filed", outcome.Sequence, len(outcome.ActionsExecuted), len(outcome.ProposalsFiled))
		if len(outcome.WouldExecute) > 0 || len(outcome.WouldFile) > 0 {
			msg = fmt.Sprintf("Capacity reconcile tick %d [DRY-RUN]: would execute %d actions, would file %d proposals — nothing actuated", outcome.Sequence, len(outcome.WouldExecute), len(outcome.WouldFile))
		}
		logger.Log("INFO", msg, map[string]interface{}{
			"action": "capacity_reconciler_tick", "tick": outcome.Sequence,
			"actions_executed": len(outcome.ActionsExecuted), "proposals_filed": len(outcome.ProposalsFiled),
			"would_execute": len(outcome.WouldExecute), "would_file": len(outcome.WouldFile),
		})
	}
	if streak, crossed := errMon.Note("reconcile", outcome.Error); crossed {
		health.RecordErrorLoop(h.captainEvents, logger, cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", fmt.Errorf("%s", outcome.Error), streak)
	}
}

// sleepTick waits one tick on the injected clock, honoring cancellation. A
// real clock sleeps out the interval (the goroutine drains harmlessly if the
// context wins); a test clock's Sleep returns immediately after advancing.
// An already-cancelled context returns at once without spawning a sleeper —
// the common shutdown path (cancel lands during the phases) leaks nothing.
func (h *RunCapacityReconcilerCoordinatorHandler) sleepTick(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	slept := make(chan struct{})
	go func() {
		h.clock.Sleep(d)
		close(slept)
	}()
	select {
	case <-slept:
	case <-ctx.Done():
	}
}
