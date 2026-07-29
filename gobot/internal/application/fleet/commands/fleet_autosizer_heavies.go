package commands

import (
	"context"
	"fmt"
)

// The HEAVY (trade) demand model. Heavies are the trade-tour pool (DedicatedFleet
// "trade"); the autosizer sizes it to UNSERVED trade demand — the count of profitable, feasible
// solver lanes beyond the current heavy count. The trade solver already ranks more feasible plans
// than there are hulls to fly them; that surplus IS the capacity-short signal:
//
//	demand_heavies = current_heavies + unserved_profitable_lanes
//
// The SPEND gate is applied by the guard stack, not here (price ceilings, class ceiling, heavy cap,
// affordability); the coordinator additionally requires the unserved-lane shortfall to persist
// heavy_unserved_lanes_min consecutive ticks before buying (that anti-thrash streak lives in the
// coordinator's ACT step, where the tick state is).
//
// SEAM (banked): the unserved-lane count read path is the heavy-demand data risk. This provider is fail-CLOSED on an unreadable lane count — no lane
// signal, no buy — so a concrete source that cannot yet surface the count leaves heavies un-bought
// (never wrongly bought) until the seam is wired (the vdld TourAlignmentProvider precedent).

// HeavyDemandSources are the reads the heavy-demand model consumes. Concrete impls wrap the
// ship repo (DedicatedFleet=="trade" count) and the trade solver's profitable-lane surface;
// tests inject fakes.
type HeavyDemandSources interface {
	// HeavyCount is the current trade-dedicated hull count.
	HeavyCount(ctx context.Context, playerID int) (int, error)
	// UnservedLaneCount is the number of profitable, feasible trade lanes the solver ranks BEYOND
	// the current heavy count — the capacity-short signal. readable=false when the solver surface
	// has no read path yet (the banked seam): the provider then fails closed (no buy).
	UnservedLaneCount(ctx context.Context, playerID int) (count int, readable bool, err error)
}

// HeavyDemandProvider sizes the trade-tour pool to unserved trade demand.
type HeavyDemandProvider struct {
	sources HeavyDemandSources
}

// NewHeavyDemandProvider wires the heavy-demand provider over its read sources.
func NewHeavyDemandProvider(sources HeavyDemandSources) *HeavyDemandProvider {
	return &HeavyDemandProvider{sources: sources}
}

// Class identifies this provider as the heavy (trade) sizer.
func (p *HeavyDemandProvider) Class() HullClass { return HullClassHeavy }

// Demand reads the heavy count and the unserved-lane count and returns the sized heavy demand. It
// fails CLOSED (Readable=false, no buy) when the heavy count or the unserved-lane signal cannot be
// read — buying trade hulls against a demand signal we cannot see is exactly the runaway the guard
// stack exists to prevent.
func (p *HeavyDemandProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	heavies, err := p.sources.HeavyCount(ctx, playerID)
	if err != nil {
		return unreadableHeavy(fmt.Sprintf("heavy count unreadable: %v", err)), nil
	}
	lanes, laneReadable, lerr := p.sources.UnservedLaneCount(ctx, playerID)
	if lerr != nil || !laneReadable {
		reason := "unserved-lane signal unreadable (banked seam) — heavies fail closed"
		if lerr != nil {
			reason = fmt.Sprintf("unserved-lane count unreadable: %v", lerr)
		}
		return unreadableHeavy(reason), nil
	}
	return computeHeavyDemand(heavyDemandInputs{
		CurrentHeavies: heavies,
		UnservedLanes:  lanes,
	}), nil
}

// heavyDemandInputs are the raw signals the pure heavy-demand math consumes.
type heavyDemandInputs struct {
	CurrentHeavies int
	UnservedLanes  int
}

// computeHeavyDemand is the pure heavy-sizing math: one wanted hull per unserved profitable lane
// beyond the current pool. A negative unserved count is clamped to zero (never shrinks the pool —
// the autosizer only grows; retirement is not its job).
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
