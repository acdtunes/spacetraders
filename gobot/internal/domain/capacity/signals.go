// Package capacity is the shared contract surface of the capacity reconciler
// (epic st-7zk, design spec docs/superpowers/specs/2026-07-15-capacity-reconciler-design.md).
//
// It holds the pure types and port interfaces every lane of the epic builds
// against: SENSE signal families (st-7ee), the desired-topology PLAN output
// (st-hlw), DIFF gap/action/tier types (st-zr0), the actuator seam (st-5ig),
// capex governor budget/decision types (st-x00), and the tiered-autonomy
// proposal channel (st-0h8). The reconcile loop itself lives in
// internal/application/capacity/commands. See CONTRACTS.md in this directory
// for the per-lane ownership map — sibling lanes fill these structs and
// implement these interfaces; they do not rename or redefine them.
//
// Everything here is data + minimal interfaces: no I/O, no daemon imports.
package capacity

import "time"

// Signals is the complete read-only measurement set one SENSE pass collects
// (spec: SENSE — all read-only: daemon DB + live API). The planner recomputes
// the desired topology from a fresh Signals every tick, which is what makes
// the loop stateless-per-tick, idempotent, and restart-safe.
//
// The SENSE lane (st-7ee) fills these families from DB reads; fields may gain
// siblings but existing names are frozen — the planner (st-hlw) reads them.
type Signals struct {
	PlayerID    int
	CollectedAt time.Time

	Demand      DemandSignals
	Performance PerformanceSignals
	Topology    TopologySignals
	Utilization UtilizationSignals
	Economics   EconomicsSignals
}

// DemandSignals: per-hub contract frequency + good-mix, derived from contract
// history (spec SENSE: Demand).
type DemandSignals struct {
	Hubs []HubDemand
}

// HubDemand is one contract hub's observed demand.
type HubDemand struct {
	// HubSymbol is the hub's waypoint symbol (contracts are accepted/delivered here).
	HubSymbol string
	// ContractFrequency is contracts/hour observed at this hub.
	ContractFrequency float64
	// AvgPaymentCredits is the mean total payment per contract at this hub —
	// the `payment` factor in the planner's hub ranking
	// (frequency × cycle_penalty × payment).
	AvgPaymentCredits float64
	// GoodMix is the per-good demand distribution at this hub.
	GoodMix []GoodDemand
}

// GoodDemand is one good's demand share at a hub — the numerator inputs of the
// planner's buffer-selection score (frequency ÷ (avg_units × source_distance)).
type GoodDemand struct {
	Good string
	// Frequency is contracts/hour requiring this good at the hub.
	Frequency float64
	// AvgUnits is the mean units per contract for this good.
	AvgUnits float64
}

// PerformanceSignals: per-hub accept→fulfill cycle-time — THE control lever —
// plus stall events (spec SENSE: Performance).
type PerformanceSignals struct {
	Hubs []HubPerformance
}

// HubPerformance is one hub's measured delivery performance.
type HubPerformance struct {
	HubSymbol string
	// CycleTimeSeconds is the mean accept→fulfill wall time at this hub.
	CycleTimeSeconds float64
	// StallEvents counts deliveries that stalled (waited on sourcing) in the
	// measurement window — the buffer-selection pressure signal.
	StallEvents int
}

// TopologySignals: the ACTUAL capacity topology — current clusters with their
// hubs/warehouses/stockers/workers, positions, buffer contents, and per-good
// caps (spec SENSE: Topology). DIFF compares this against the DesiredTopology.
type TopologySignals struct {
	Clusters []ClusterState

	// IdleHulls is the poachable idle-hull set the DIFF lane's tier-1
	// reuse-first rung may reassign (ADDITIVE, st-zr0 — Diff receives only
	// TopologySignals, so the reuse-eligible subset of Utilization.Hulls
	// travels here). The SENSE lane (st-7ee) fills it with the hulls whose
	// Idle is true, alongside Clusters. The ladder differ re-verifies
	// eligibility per hull (Idle && DedicatedFleet == "" && not already
	// serving a cluster role), so an over-filled slice fails SAFE: an
	// ineligible entry is skipped, never reassigned. Empty ⇒ tier 1 has no
	// free lever and gaps escalate up the ladder (approval-gated at tier 4).
	IdleHulls []HullUtilization
}

// ClusterState is one live cluster: a covered hub and the capacity co-located
// on it.
type ClusterState struct {
	HubSymbol  string
	Warehouses []WarehouseState
	Stockers   []StockerState
	Workers    []WorkerState
}

// WarehouseState is one live warehouse hull: position, buffer contents, and
// the per-good caps currently configured on it.
type WarehouseState struct {
	ShipSymbol string
	Waypoint   string
	Buffer     []BufferedStock
	// GoodCaps is the per-good unit cap currently configured (the whitelist:
	// a good present here is buffered; its value is its cap).
	GoodCaps map[string]int
}

// BufferedStock is one good's current buffered quantity in a warehouse.
type BufferedStock struct {
	Good  string
	Units int
}

// StockerState is one live stocker hull and its position.
type StockerState struct {
	ShipSymbol string
	Waypoint   string
}

// WorkerState is one live delivery-worker hull and its position.
type WorkerState struct {
	ShipSymbol string
	Waypoint   string
}

// UtilizationSignals: per-hull duty-cycle / idle state — what drives the
// reuse-first tier of the escalation ladder (spec SENSE: Utilization).
type UtilizationSignals struct {
	Hulls []HullUtilization
}

// HullUtilization is one hull's observed utilization.
type HullUtilization struct {
	ShipSymbol string
	// DedicatedFleet is the hull's fleet dedication tag ("" = undedicated).
	// Reuse (tier 1) may only reassign undedicated or own-fleet hulls — never
	// poach another operation's pinned hull.
	DedicatedFleet string
	Waypoint       string
	// DutyCyclePct is the hull's earning share of the sample window (0-100).
	DutyCyclePct float64
	// Idle reports the hull currently has no container flying it.
	Idle bool
}

// EconomicsSignals: treasury, income velocity, per-good source distances, and
// stocker load (spec SENSE: Economics). The GOVERN phase paces capital spend
// from these.
type EconomicsSignals struct {
	// TreasuryCredits is the live agent credit balance.
	TreasuryCredits int64
	// IncomeVelocityPerHour is the observed credits/hour income rate.
	IncomeVelocityPerHour float64
	// FleetPerHullCrHr is the current fleet-wide sustained per-hull credits/hr
	// — the north-star metric every capacity add is ROI-gated on.
	FleetPerHullCrHr float64
	// FleetHullCount is the number of hulls in the player's fleet (keep
	// consistent with len(Utilization.Hulls)). The SENSE lane (st-7ee) fills
	// it; the governor (st-x00) needs it to derive a capital action's
	// ProjectedGainPerHour without dividing by a cold-start zero
	// (see proposal.go's ROIEvidence derivation).
	FleetHullCount int
	// SourceDistances is the per-good distance from each hub to its nearest
	// source market — the denominator of the buffer-selection score.
	SourceDistances []GoodSourceDistance
	// StockerLoad is the per-hub stocker utilization.
	StockerLoad []StockerLoad
}

// GoodSourceDistance is the sourcing distance for one good relative to a hub.
type GoodSourceDistance struct {
	HubSymbol string
	Good      string
	Distance  float64
}

// StockerLoad is one hub's stocker restock pressure.
type StockerLoad struct {
	HubSymbol string
	// ActiveStockers is the count of stocker hulls serving the hub.
	ActiveStockers int
	// LoadPct is the observed restock utilization (0-100); sustained values
	// near 100 mean restock_throughput < consumption_rate.
	LoadPct float64
}
