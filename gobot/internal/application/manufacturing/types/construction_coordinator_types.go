package types

// RunConstructionCoordinatorCommand drives the thin construction-supply drain (sp-382j):
// a standing per-player coordinator that each tick sources and delivers a construction
// pipeline's READY DELIVER_TO_CONSTRUCTION tasks to their jump-gate site, on the shared
// ProductionExecutor engine. It rebuilds the gate-construction EXECUTION path deleted by
// sp-jav2 without resurrecting the parallel task coordinator.
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
	// ProductionStrategy is the SupplyChainResolver acquisition strategy the drain resolves a
	// FABRICATE material's dependency tree on (sp-yfzi): "smart" (fabricate a SCARCE intermediate
	// that has a factory, buy an abundant one — the fleet-wide production default) unsticks a scarce
	// gate material by producing it locally instead of buying it scarce; "prefer-buy" dials back to
	// the flat one-level sourcing. Stamped onto the tree-build ctx (WithProductionStrategy). Empty is
	// a no-op (resolver keeps prefer-buy); the launch build defaults it to "smart"
	// (resolveProductionStrategy). Fed from production_strategy. Only consulted on the FABRICATE
	// branch — a buy-final material (no factory) never touches the resolver.
	ProductionStrategy string
	// UnifiedGateFill turns the drain into a UNIFIED GATE-FILL wrapper (sp-vh1s, Admiral sign-off
	// 2026-07-14). OFF (default): the drain honors the planner's frozen buy-vs-fabricate decision per
	// material — byte-identical to today. ON: it IGNORES that frozen decision and drives the resolver's
	// FULL scarcity-gated tree for every gate material (feeding inherent — the fix for the zero-feeding
	// pure-BUY bug), and marks the run a gate node (WithUnifiedGateFill + a construction-site
	// DeliveryTarget) so the output-buy is throughput-paced and lane B's per-node gates go margin-blind.
	// Fed from ManufacturingConfig.UnifiedGateFill.
	UnifiedGateFill bool
	// FabricationEfficiency turns on the sp-to2v feeding-efficiency policy for the drain's per-material
	// production (balanced-to-limiting input feeding, saturation-capped tranches, taproot-first, and
	// buy-or-skip for non-responsive goods), stamped on the produce ctx (WithFeedingPolicy). OFF (the
	// default) leaves the greedy byte-identical feeding. Fed from fabrication_efficiency.
	FabricationEfficiency bool
	// FeedSaturationMaxUnits / FeedSaturationMinUnits / FeedNonResponsiveGoods are the sp-to2v feeding
	// knobs: the saturation-window bounds (0/absent → 200/25) and the non-responsive OUTPUT-good set
	// that is BUY-OR-SKIPed (nil/empty → the verified {EQUIPMENT,LAB_INSTRUMENTS,FOOD,MEDICINE}). Fed
	// from feed_saturation_max_units / feed_saturation_min_units / feed_non_responsive_goods.
	FeedSaturationMaxUnits int
	FeedSaturationMinUnits int
	FeedNonResponsiveGoods []string

	// SupplyTaskTimeoutSeconds bounds a SINGLE supplyTask (claim→source→route-with-refuel→supply→record)
	// before the drain abandons it and retries next tick (sp-ubwi). 0/absent → the coordinator's raised
	// 30m default; a legit multi-hop light-hauler round trip exceeds the old hardcoded 10m, which
	// abandoned healthy long hauls at the finish line (and the retry re-bought on a fresh empty hull,
	// stranding the laden one). Fed from [manufacturing].construction_supply_task_timeout_seconds so a
	// captain retunes it live (RULINGS #5); a per-launch config value overrides the handler default.
	SupplyTaskTimeoutSeconds int
}

// RunConstructionCoordinatorResponse reports the outcome of the last drain tick.
type RunConstructionCoordinatorResponse struct {
	// TasksDrained is how many DELIVER_TO_CONSTRUCTION tasks were sourced and supplied.
	TasksDrained int
	// NoWorkReason explains a tick that supplied nothing (no ready task, no idle hauler),
	// so a long-parked drain still proves it is alive and why.
	NoWorkReason string
}
