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
}

// RunConstructionCoordinatorResponse reports the outcome of the last drain tick.
type RunConstructionCoordinatorResponse struct {
	// TasksDrained is how many DELIVER_TO_CONSTRUCTION tasks were sourced and supplied.
	TasksDrained int
	// NoWorkReason explains a tick that supplied nothing (no ready task, no idle hauler),
	// so a long-parked drain still proves it is alive and why.
	NoWorkReason string
}
