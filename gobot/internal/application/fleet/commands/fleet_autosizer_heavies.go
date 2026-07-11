package commands

import (
	"context"
	"fmt"
)

// The HEAVY (trade) demand model (sp-1txd M4). Heavies are the trade-tour pool (DedicatedFleet
// "trade"); the autosizer sizes it to UNSERVED trade demand — the count of profitable, feasible
// solver lanes beyond the current heavy count. The trade solver already ranks more feasible plans
// than there are hulls to fly them; that surplus IS the capacity-short signal (the 0gsw refute
// logic mechanized):
//
//	demand_heavies = current_heavies + unserved_profitable_lanes
//
// The ECONOMIC gate is applied by the guard stack, not here: the provider reports the fleet-average
// realized tour $/hr and the MARGINAL heavy's realized rate, plus whether the fleet-average trend
// is declining (absorption saturating). The realized-rate guard buys only while the marginal rate
// holds near the fleet average, and stops on decay; the coordinator additionally requires the
// unserved-lane shortfall to persist heavy_unserved_lanes_min consecutive ticks before buying
// (that anti-thrash streak lives in the coordinator's ACT step, M5, where the tick state is).
//
// SEAM (banked, sp-1txd plan): the unserved-lane count and the realized tour-rate read paths are
// the heavy-demand data risk. This provider is fail-CLOSED on an unreadable lane count — no lane
// signal, no buy — so a concrete source that cannot yet surface the count leaves heavies un-bought
// (never wrongly bought) until the seam is wired (the vdld TourAlignmentProvider precedent).

// HeavyDemandSources are the reads the heavy-demand model consumes. Concrete impls (M6) wrap the
// ship repo (DedicatedFleet=="trade" count), the trade solver's profitable-lane surface, and the
// tour telemetry realized-rate reader; tests inject fakes.
type HeavyDemandSources interface {
	// HeavyCount is the current trade-dedicated hull count.
	HeavyCount(ctx context.Context, playerID int) (int, error)
	// UnservedLaneCount is the number of profitable, feasible trade lanes the solver ranks BEYOND
	// the current heavy count — the capacity-short signal. readable=false when the solver surface
	// has no read path yet (the banked seam): the provider then fails closed (no buy).
	UnservedLaneCount(ctx context.Context, playerID int) (count int, readable bool, err error)
	// FleetTourRate returns the fleet-average realized tour $/hr, the MARGINAL (lowest-earning)
	// heavy's realized rate, whether the fleet-average trend is declining (absorption saturating),
	// and whether any of it was readable.
	FleetTourRate(ctx context.Context, playerID int) (fleetAvg, marginal float64, declining, readable bool, err error)
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
// stack exists to prevent. An unreadable realized rate is surfaced as RateReadable=false so the
// guard fails the rate gate closed on its own, without blocking demand sizing.
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
	fleetAvg, marginal, declining, rateReadable, rerr := p.sources.FleetTourRate(ctx, playerID)
	if rerr != nil {
		rateReadable = false
	}
	return computeHeavyDemand(heavyDemandInputs{
		CurrentHeavies: heavies,
		UnservedLanes:  lanes,
		MarginalRate:   marginal,
		FleetAvgRate:   fleetAvg,
		RateDeclining:  declining,
		RateReadable:   rateReadable,
	}), nil
}

// heavyDemandInputs are the raw signals the pure heavy-demand math consumes.
type heavyDemandInputs struct {
	CurrentHeavies int
	UnservedLanes  int
	MarginalRate   float64
	FleetAvgRate   float64
	RateDeclining  bool
	RateReadable   bool
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
		Class:         HullClassHeavy,
		Demand:        in.CurrentHeavies + unserved,
		Current:       in.CurrentHeavies,
		MarginalRate:  in.MarginalRate,
		FleetAvgRate:  in.FleetAvgRate,
		RateDeclining: in.RateDeclining,
		RateReadable:  in.RateReadable,
		Readable:      true,
		Reason:        fmt.Sprintf("%d heavies + %d unserved profitable lanes = %d (marginal %.0f vs fleet-avg %.0f, declining=%v)", in.CurrentHeavies, unserved, in.CurrentHeavies+unserved, in.MarginalRate, in.FleetAvgRate, in.RateDeclining),
	}
}

// unreadableHeavy is the fail-closed heavy demand (a core signal could not be read).
func unreadableHeavy(reason string) ClassDemand {
	return ClassDemand{Class: HullClassHeavy, Readable: false, Reason: reason}
}
