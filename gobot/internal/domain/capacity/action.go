package capacity

// Tier is the escalation-ladder rung an action sits on (spec: DIFF + CONVERGE
// — cheapest-lever-first; this ordering is what preserves per-hull-$/hr).
// Tiers 1-3 are cheap/reversible and execute autonomously; tier 4 is capital
// and files a proposal for approval (tiered autonomy).
type Tier int

const (
	// TierReuseIdle reassigns idle hulls — free → auto.
	TierReuseIdle Tier = 1
	// TierRebalance repositions/rebalances existing capacity (drives the
	// worker-rebalancer / depot-rebalance primitives) — free → auto.
	TierRebalance Tier = 2
	// TierBufferAdjust adjusts the buffer whitelist + caps — cheap → auto.
	// The spec's "gated if it forces a NEW stocker" is implemented by the
	// DIFF lane as WITHHOLDING: while a hub's desired StockerCount exceeds
	// its actual stockers, demand-EXPANDING adjustments (whitelist adds, cap
	// raises) are not emitted at all — they self-heal after the stocker
	// capacity lands (itself tier-4-gated where capital). Relabeling the
	// buffer verbs to tier 4 is NOT the mechanism: converge's canonical
	// verb→tier check refuses a mislabeled action. Demand-shedding
	// adjustments (cap reductions, de-whitelists) always flow.
	TierBufferAdjust Tier = 3
	// TierCapital adds a cluster / autobuys a hull — capital → proposal for
	// approval; executed only post-approval via Actuator.ExecuteCapital.
	TierCapital Tier = 4
)

// Autonomous reports whether actions of this tier execute without approval.
func (t Tier) Autonomous() bool {
	return t >= TierReuseIdle && t <= TierBufferAdjust
}

// RequiresApproval reports whether actions of this tier must go through the
// proposal channel before execution.
func (t Tier) RequiresApproval() bool {
	return t == TierCapital
}

// String names the tier for logs and proposals.
func (t Tier) String() string {
	switch t {
	case TierReuseIdle:
		return "tier1_reuse_idle"
	case TierRebalance:
		return "tier2_rebalance"
	case TierBufferAdjust:
		return "tier3_buffer_adjust"
	case TierCapital:
		return "tier4_capital"
	}
	return "tier_unknown"
}

// ActionVerb names the concrete operation an Action performs. Each verb maps
// to exactly one Actuator method (by its tier); the DIFF lane (st-zr0) emits
// them and the actuator lane (st-5ig) implements them over the existing
// primitives (fleet autosizer, launch siting, worker-rebalancer,
// depot-rebalance) — the reconciler never reinvents buy/move.
type ActionVerb string

const (
	// VerbReassignHull (tier 1): dedicate an idle, poachable hull to a role.
	VerbReassignHull ActionVerb = "reassign_hull"
	// VerbRepositionHull (tier 2): move an owned hull to a target waypoint.
	VerbRepositionHull ActionVerb = "reposition_hull"
	// VerbRebalanceWorkers (tier 2): drive the worker-rebalancer toward the
	// desired per-hub worker counts.
	VerbRebalanceWorkers ActionVerb = "rebalance_workers"
	// VerbAdjustBufferWhitelist (tier 3): add/remove a good from a hub
	// warehouse's buffer whitelist.
	VerbAdjustBufferWhitelist ActionVerb = "adjust_buffer_whitelist"
	// VerbAdjustBufferCap (tier 3): change one buffered good's unit cap.
	VerbAdjustBufferCap ActionVerb = "adjust_buffer_cap"
	// VerbAddCluster (tier 4): stand up capacity on an uncovered hub.
	VerbAddCluster ActionVerb = "add_cluster"
	// VerbBuyHull (tier 4): autobuy a hull for a role.
	VerbBuyHull ActionVerb = "buy_hull"
)

// Gap is one desired-vs-actual divergence DIFF found. The DIFF lane (st-zr0)
// turns gaps into ordered Actions (cheapest tier that closes the gap first).
type Gap struct {
	Kind      GapKind
	HubSymbol string
	// Good is set for buffer-good gaps.
	Good string
	// Want/Have quantify the divergence in the gap kind's natural unit
	// (hull counts, unit caps, coverage 0/1).
	Want int
	Have int
	// Detail is the human-readable audit line.
	Detail string
}

// GapKind classifies a desired-vs-actual divergence.
type GapKind string

const (
	GapHubUncovered      GapKind = "hub_uncovered"
	GapBufferGoodMissing GapKind = "buffer_good_missing"
	GapBufferGoodExtra   GapKind = "buffer_good_extra"
	GapBufferCapWrong    GapKind = "buffer_cap_wrong"
	GapWarehouseShort    GapKind = "warehouse_short"
	GapStockerShort      GapKind = "stocker_short"
	GapWorkerShort       GapKind = "worker_short"
	GapHullMisplaced     GapKind = "hull_misplaced"
)

// Action is one convergence step. Tiers 1-3 dispatch straight to the Actuator;
// tier 4 becomes a Proposal (GOVERN) and reaches the Actuator only
// post-approval.
type Action struct {
	Tier Tier
	Verb ActionVerb

	// Routing fields — which verb reads which is documented per verb; unused
	// fields stay zero.
	HubSymbol      string
	ShipSymbol     string
	Good           string
	UnitsCap       int
	TargetWaypoint string

	// EstimatedCostCredits is the capital an action consumes (tier 4; 0 for
	// the free/cheap tiers). GOVERN budgets on it.
	EstimatedCostCredits int64
	// HullDelta is the net hull count the action adds to the fleet (buy_hull:
	// 1; add_cluster: its warehouse+stocker+worker counts; 0 for the free
	// tiers). Pure topology arithmetic — the DIFF lane (st-zr0) fills it; the
	// governor's ROI evidence derives the gain rate from it (see proposal.go).
	HullDelta int
	// ProjectedPerHullCrHr is the projected fleet-wide per-hull sustained
	// credits/hr AFTER the action — the ROI-gate input (an add must raise it).
	ProjectedPerHullCrHr float64

	// Machine-readable routing (ADDITIVE, st-zr0 fills) — the actuator
	// (st-5ig) reads these instead of parsing Reason prose.
	//
	// GapKind classifies the gap this action closes. Role-bearing for hull
	// actions: a reassign_hull/buy_hull with GapWarehouseShort /
	// GapStockerShort / GapWorkerShort is a warehouse/stocker/worker hull.
	GapKind GapKind
	// WarehouseDelta/StockerDelta/WorkerDelta decompose HullDelta per role:
	// add_cluster carries its cluster composition, buy_hull stamps 1 on the
	// bought role, the free tiers stay 0. Invariant:
	// WarehouseDelta+StockerDelta+WorkerDelta == HullDelta.
	WarehouseDelta int
	StockerDelta   int
	WorkerDelta    int
	// Count is the verb's subject quantity where the verb moves N of
	// something (rebalance_workers: workers drawn from the fleet surplus
	// toward the hub; 0 for other verbs).
	Count int

	// Reason is the human-readable audit trail (which gap, which arithmetic).
	Reason string
}
