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
