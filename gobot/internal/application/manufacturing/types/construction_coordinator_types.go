package types

// RunConstructionCoordinatorCommand drives the thin construction-supply drain: a
// standing per-player coordinator that each tick sources and delivers a construction
// pipeline's READY DELIVER_TO_CONSTRUCTION tasks to their jump-gate site, on the shared
// ProductionExecutor engine.
type RunConstructionCoordinatorCommand struct {
	// PlayerID scopes the pipelines, tasks, and haulers the drain serves.
	PlayerID int
	// SystemSymbol is the system the drain operates in; idle-hauler discovery is
	// restricted to it (construction legs are in-system, never a jump).
	SystemSymbol string
	// ContainerID tags the atomic ship claims (shared "manufacturing" fleet identity)
	// and links the operation context for transaction attribution.
	ContainerID string
	// MaxIterations bounds a run: >0 runs that many drain ticks then returns; <=0 loops
	// forever (the standing default the daemon launches with, iterations=-1).
	MaxIterations int
	// TickSeconds is the delay between drain ticks; <=0 uses the coordinator's default.
	TickSeconds int
	// ProductionStrategy is the SupplyChainResolver acquisition strategy for a FABRICATE
	// material's dependency tree: "smart" (fabricate a SCARCE intermediate that has a
	// factory, buy an abundant one — the fleet-wide default) unsticks a scarce gate
	// material by producing it locally instead of buying it scarce; "prefer-buy" is the
	// flat one-level sourcing. Empty defaults to "smart". Only consulted on the FABRICATE
	// branch — a buy-final material (no factory) never touches the resolver. Fed from
	// production_strategy.
	ProductionStrategy string
	// UnifiedGateFill switches the drain from the planner's frozen buy-vs-fabricate
	// decision (OFF, default: byte-identical) to the resolver's full scarcity-gated tree
	// for every gate material, marking the run a gate node so the output-buy is
	// throughput-paced and lane B's per-node gates go margin-blind (ON). Fed from
	// ManufacturingConfig.UnifiedGateFill.
	UnifiedGateFill bool
	// FabricationEfficiency enables balanced-to-limiting input feeding with
	// saturation-capped tranches, taproot-first ordering, and buy-or-skip for
	// non-responsive goods. OFF (default) keeps greedy byte-identical feeding. Fed from
	// fabrication_efficiency.
	FabricationEfficiency bool
	// FeedSaturationMaxUnits / FeedSaturationMinUnits / FeedNonResponsiveGoods are the
	// feeding knobs: saturation-window bounds (0/absent -> 200/25) and the non-responsive
	// OUTPUT-good set that is BUY-OR-SKIPed (nil/empty -> {EQUIPMENT,LAB_INSTRUMENTS,
	// FOOD,MEDICINE}). Fed from feed_saturation_max_units / feed_saturation_min_units /
	// feed_non_responsive_goods.
	FeedSaturationMaxUnits int
	FeedSaturationMinUnits int
	FeedNonResponsiveGoods []string

	// SupplyTaskTimeoutSeconds bounds a single supplyTask (claim->source->route-with-refuel
	// ->supply->record) before the drain abandons it and retries next tick. 0/absent uses
	// the coordinator's 30m default — must cover a legit multi-hop light-hauler round trip,
	// or a healthy long haul gets abandoned at the finish line and re-bought on a fresh
	// empty hull while the laden one strands. Fed from
	// [manufacturing].construction_supply_task_timeout_seconds so a captain retunes it live.
	SupplyTaskTimeoutSeconds int

	// DedicatedFleet is the Ship.DedicatedFleet() tag this drain prefers: each tick it
	// discovers idle hulls carrying this tag via FindIdleShipsByFleet first, then
	// supplements with opportunistic non-dedicated idle hulls. Empty defaults (in-handler)
	// to the shared "manufacturing" identity, which is ALSO the ClaimShip operation string:
	// the two MUST match, or ClaimShip's atomic no-poach guard rejects the drain claiming
	// its own dedicated hull. Preference derives from the live persisted tag read every
	// tick, so a `fleet assign` (or a restart) re-derives it with no carried state. Fed
	// from dedicated_fleet.
	DedicatedFleet string
	// ExclusiveDedicatedFleet seals the drain to its dedicated fleet: when true and ANY
	// hull carries the DedicatedFleet tag, the drain draws ONLY from idle dedicated members
	// and never supplements from the opportunistic pool — even while a dedicated hull is
	// busy or out-of-system. Default false prefers dedicated first, then falls back to
	// opportunistic idle hulls when dedicated capacity is insufficient. A hull pinned to
	// ANOTHER fleet is never claimed either way — ClaimShip enforces that atomically. Fed
	// from exclusive_dedicated_fleet.
	ExclusiveDedicatedFleet bool
}

// RunConstructionCoordinatorResponse reports the outcome of the last drain tick.
type RunConstructionCoordinatorResponse struct {
	// TasksDrained is how many DELIVER_TO_CONSTRUCTION tasks were sourced and supplied.
	TasksDrained int
	// NoWorkReason explains a tick that supplied nothing (no ready task, no idle hauler),
	// so a long-parked drain still proves it is alive and why.
	NoWorkReason string
}
