package commands

import "fmt"

// The HEAVY (trade) demand model: demand_heavies = current_heavies + unserved_profitable_lanes.
// The solver already ranks more feasible plans than there are hulls to fly them, and that surplus
// IS the capacity-short signal.
//
// Nothing here gates SPEND. The guard stack does (price, cap, affordability), and the WAVE gates it
// one level above: growth only reaches a heavy buy on a HEAVY wave, so a surface the regime has
// declared saturated raises no demand this side of the predicate.

// heavyDemandInputs are the raw signals the pure heavy-demand math consumes.
type heavyDemandInputs struct {
	CurrentHeavies int
	UnservedLanes  int
}

// computeHeavyDemand is the pure heavy-sizing math: one wanted hull per unserved profitable lane
// beyond the current pool. A negative unserved count is clamped to zero (never shrinks the pool —
// growth only grows; retirement is not its job).
func computeHeavyDemand(in heavyDemandInputs) ClassDemand {
	unserved := in.UnservedLanes
	if unserved < 0 {
		unserved = 0
	}
	return ClassDemand{
		Class:    HullClassHeavy,
		Demand:   in.CurrentHeavies + unserved,
		Current:  in.CurrentHeavies,
		Readable: true,
		Reason:   fmt.Sprintf("%d heavies + %d unserved profitable lanes = %d", in.CurrentHeavies, unserved, in.CurrentHeavies+unserved),
	}
}

// unreadableHeavy is the fail-closed heavy demand (a core signal could not be read).
func unreadableHeavy(reason string) ClassDemand {
	return ClassDemand{Class: HullClassHeavy, Readable: false, Reason: reason}
}

// HeavyDemandFor exposes the sizing math to the adapter-level census tests. It runs the SAME pure
// function the coordinator does, so the arithmetic cannot change under them.
func HeavyDemandFor(currentHeavies, unservedLanes int) int {
	return computeHeavyDemand(heavyDemandInputs{CurrentHeavies: currentHeavies, UnservedLanes: unservedLanes}).Demand
}
