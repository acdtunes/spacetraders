package capacity

import "context"

// The GOVERN-phase capex EMITTER (st-x00, re-scoped by st-5le, confirmed by the
// economy-analyst). This lane deliberately does NOT build a standalone capex
// governor / guard stack. The capacity reconciler already owns SENSE→PLAN→DIFF;
// its CAPITAL tier (the tier-4 Actions DIFF produced) is translated here into
// contract-delivery capacity demand and EMITTED to the already-built sp-1txd
// Fleet Autosizer, which owns the SINGLE guard stack (reserve-floor-net,
// 25%-treasury, era payback, fleet ceilings, per-tick cap, API-util,
// dedicate-at-purchase) that actually executes the buy.
//
// So CapexEmitter implements capacity.Governor as a THIN EMITTER, not a budget
// calculator:
//   - cheap/autonomous tiers (1-3) pass through to GovernResult.Approved
//     verbatim — the reconciler's converge dispatches them to its actuator;
//   - the CAPITAL tier (4) is summed per role into one CapitalDemand snapshot
//     and handed to the CapitalDemandSink; those actions are NOT approved and
//     NOT proposed here, so the reconciler's converge never executes or files
//     them (safety invariant 4 preserved structurally — the ACTUAL buy is
//     sp-1txd's guarded path). Whether the emit is later gated behind an
//     approved proposal is st-0h8's job; this lane only builds the emit seam.
//
// NO second budget/guard is computed here: CapexBudget stays a type but is left
// zero-valued, and no reserve-floor / surplus-fraction / payback arithmetic
// runs — all money-gating lives in sp-1txd (verified: it evaluates its absolute
// fleet ceiling + per-tick cap over the COMBINED post-buy fleet count across
// ALL registered demand providers, so a second provider cannot over-buy).

// CapitalDemand is one reconcile tick's contract-delivery capital demand — the
// hulls the desired topology wants but does not yet have, decomposed into the
// st-7zk-owned roles (contract-depot warehouses, contract-depot stockers,
// delivery hulls) plus the reconciler's ROI projection for sp-1txd's guards.
//
// Hulls is a DELTA (the DIFF gap), not an absolute standing count: the reconcile
// loop is stateless-per-tick, so the gap is recomputed from fresh fleet state
// every pass and shrinks as sp-1txd buys against it. It is published every tick
// (Present=true, including an explicit zero gap) so sp-1txd never reads a stale
// non-zero demand.
type CapitalDemand struct {
	// Hulls is the net tier-4 hull gap this tick (sum of the capital Actions'
	// HullDelta). WarehouseHulls+StockerHulls+DeliveryHulls == Hulls.
	Hulls          int
	WarehouseHulls int
	StockerHulls   int
	DeliveryHulls  int
	// MarginalProjectedCrHr is the marginal (lowest) projected per-hull $/hr
	// across the capital Actions (Action.ProjectedPerHullCrHr) — the reconciler's
	// own ROI projection, handed to sp-1txd as the marginal-rate evidence its
	// era-payback + realized-rate guards judge. 0 with RateReadable=false when no
	// action carried a positive projection ⇒ those guards fail closed.
	MarginalProjectedCrHr float64
	// FleetPerHullCrHr is the current fleet-wide per-hull $/hr
	// (EconomicsSignals.FleetPerHullCrHr) — the fleet-average reference sp-1txd's
	// realized-rate floor is a fraction of.
	FleetPerHullCrHr float64
	// RateReadable reports whether MarginalProjectedCrHr is a real projection.
	RateReadable bool
	// Present marks a real emit — distinguishes an emitted zero gap from the
	// never-written zero value (the bridge fails closed on the latter).
	Present bool
}

// CapitalDemandSink is the driven port the emitter publishes to each tick. The
// production impl is the adapter-layer ContractDeliveryDemandBridge, which the
// sp-1txd autosizer also reads as a registered ClassDemandProvider.
type CapitalDemandSink interface {
	// EmitCapitalDemand publishes this tick's contract-delivery capital demand.
	// Called once per reconcile tick (including an explicit zero-gap emit).
	EmitCapitalDemand(demand CapitalDemand)
}

// capexEmitReason is the audit note on every emitted capital action's decision:
// it was handed to sp-1txd, never executed or proposed by the reconciler.
const capexEmitReason = "emitted as contract-delivery capital demand to the sp-1txd fleet autosizer (its guard stack executes the buy); the reconciler neither executes nor proposes it"

// CapexEmitter implements capacity.Governor as a thin emitter (see file doc).
type CapexEmitter struct {
	sink CapitalDemandSink
}

// NewCapexEmitter wires the emitter to its demand sink (the shared bridge the
// sp-1txd autosizer consumes as a demand provider).
func NewCapexEmitter(sink CapitalDemandSink) *CapexEmitter {
	return &CapexEmitter{sink: sink}
}

// Govern passes cheap tiers through to Approved and emits the summed tier-4
// capital demand to the sink. It never mints proposals and never computes a
// capex budget — the whole money-gating job is sp-1txd's.
//
// DryRun DOES NOT SUPPRESS THIS EMIT. Govern runs in the GOVERN phase, BEFORE and
// independent of CONVERGE's DryRun branch (run_capacity_reconciler_coordinator.go:
// reconcileTick calls Govern, THEN converge checks DryRun) — so the reconciler
// publishes contract-delivery demand to the sp-1txd bridge every tick even in
// observe-only mode. DryRun only silences CONVERGE's own actuation + proposal
// filing; it is NOT what stops a capital buy. The REAL inertness is the DORMANT
// contract_delivery class: HullClassContractDelivery sits outside the set sp-1txd's
// classDisabled recognizes, so its "unknown class: never act" default skips this
// demand every tick (see internal/adapters/capacity/contract_delivery_bridge.go).
// A future arming lane MUST wire that class in deliberately — and MUST NOT rely on
// DryRun as the safety once it does.
func (e *CapexEmitter) Govern(_ context.Context, actions []Action, economics EconomicsSignals, _ Calibration) (GovernResult, error) {
	result := GovernResult{}
	capital := newCapitalAccumulator(economics.FleetPerHullCrHr)
	for _, action := range actions {
		if action.Tier != TierCapital {
			result.Approved = append(result.Approved, action)
			continue
		}
		capital.add(action)
		result.Decisions = append(result.Decisions, CapexDecision{Action: action, Approved: false, Reason: capexEmitReason})
	}
	e.sink.EmitCapitalDemand(capital.demand())
	return result, nil
}

// capitalAccumulator folds the tick's tier-4 Actions into one CapitalDemand,
// tracking the marginal (lowest positive) per-hull projection as it goes.
type capitalAccumulator struct {
	demandValue  CapitalDemand
	haveMarginal bool
}

func newCapitalAccumulator(fleetPerHullCrHr float64) *capitalAccumulator {
	return &capitalAccumulator{demandValue: CapitalDemand{FleetPerHullCrHr: fleetPerHullCrHr, Present: true}}
}

// add merges one capital Action's per-role hull deltas and its ROI projection.
func (a *capitalAccumulator) add(action Action) {
	a.demandValue.Hulls += action.HullDelta
	a.demandValue.WarehouseHulls += action.WarehouseDelta
	a.demandValue.StockerHulls += action.StockerDelta
	a.demandValue.DeliveryHulls += action.WorkerDelta
	if action.ProjectedPerHullCrHr <= 0 {
		return
	}
	if !a.haveMarginal || action.ProjectedPerHullCrHr < a.demandValue.MarginalProjectedCrHr {
		a.demandValue.MarginalProjectedCrHr = action.ProjectedPerHullCrHr
		a.haveMarginal = true
	}
}

// demand finalizes the accumulated snapshot (stamping rate readability).
func (a *capitalAccumulator) demand() CapitalDemand {
	finalized := a.demandValue
	finalized.RateReadable = a.haveMarginal
	return finalized
}
