package commands

import (
	"context"
	"fmt"
	"math"
)

// The LIGHT (factory-worker) demand model (sp-1txd M3). Lights are the HAULER pool the factory
// chains rotate through; the autosizer sizes that pool to the number of factory chains the fleet
// could profitably run, inverting the vdld siting coordinator's C3 rotation math:
//
//	demand_workers = ceil(desired_chains × light_rotation_slots) + rebalancer_vacancies
//
// desired_chains is the profitable factory-chain candidate count (running standing chains plus any
// promising-but-unlaunched headroom — vdld sizes chains DOWN to workers, so when it is
// worker-limited there are more profitable chains than workers support, and that gap is the buy
// signal). rebalancer_vacancies is the worker-rebalancer's hub-vacancy count: a factory hub running
// with no in-system idle worker is a concrete, immediate worker shortfall regardless of the chain
// math. The realized-worker-$/hr gate (chain_pnl NetPerHour) rides along for the guard stack.

// LightDemandSources are the reads the light-demand model consumes. Concrete impls (M6) wrap the
// ship repo (HAULER count), the running-chains controller, the rebalancer vacancy query, and the
// chain-P&L reader; tests inject fakes.
type LightDemandSources interface {
	// WorkerCount is the current HAULER-role hull count (the worker pool).
	WorkerCount(ctx context.Context, playerID int) (int, error)
	// DesiredChains is the number of profitable factory-chain candidates the fleet could run —
	// running standing chains plus any promising-but-unlaunched headroom.
	DesiredChains(ctx context.Context, playerID int) (int, error)
	// Vacancies is the worker-rebalancer hub-vacancy count (factory hubs with no in-system idle
	// worker). Additive to the chain-derived base demand.
	Vacancies(ctx context.Context, playerID int) (int, error)
	// MarginalWorkerRate returns the realized worker $/hr the guard judges: the marginal chain's
	// realized rate (chain_pnl NetPerHour), the fleet-average, whether the trend is declining, and
	// whether any of it was readable (false on a pre-realization fleet with no P&L yet).
	MarginalWorkerRate(ctx context.Context, playerID int) (marginal, fleetAvg float64, declining, readable bool, err error)
}

// LightDemandProvider sizes the factory-worker (HAULER) pool to factory-chain demand.
type LightDemandProvider struct {
	sources LightDemandSources
}

// NewLightDemandProvider wires the light-demand provider over its read sources.
func NewLightDemandProvider(sources LightDemandSources) *LightDemandProvider {
	return &LightDemandProvider{sources: sources}
}

// Class identifies this provider as the light (worker) sizer.
func (p *LightDemandProvider) Class() HullClass { return HullClassLight }

// Demand reads the worker pool, the desired-chain count, and vacancies, and returns the sized
// light demand. A failed read of a CORE signal (workers or desired chains) fails CLOSED —
// Readable=false, no buy — rather than erroring the whole tick; an unreadable vacancy count is
// treated as zero (the base chain demand still sizes); an unreadable realized rate is surfaced as
// RateReadable=false so the guard stack fails the rate gate closed on its own.
func (p *LightDemandProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	workers, err := p.sources.WorkerCount(ctx, playerID)
	if err != nil {
		return unreadableLight(fmt.Sprintf("worker count unreadable: %v", err)), nil
	}
	chains, err := p.sources.DesiredChains(ctx, playerID)
	if err != nil {
		return unreadableLight(fmt.Sprintf("desired-chain count unreadable: %v", err)), nil
	}
	vacancies, verr := p.sources.Vacancies(ctx, playerID)
	if verr != nil {
		vacancies = 0 // additive signal; a partial read must not fabricate demand
	}
	marginal, fleetAvg, declining, rateReadable, rerr := p.sources.MarginalWorkerRate(ctx, playerID)
	if rerr != nil {
		rateReadable = false
	}
	return computeLightDemand(lightDemandInputs{
		CurrentWorkers: workers,
		DesiredChains:  chains,
		Vacancies:      vacancies,
		RotationSlots:  params.LightRotationSlots,
		MarginalRate:   marginal,
		FleetAvgRate:   fleetAvg,
		RateDeclining:  declining,
		RateReadable:   rateReadable,
	}), nil
}

// lightDemandInputs are the raw signals the pure light-demand math consumes.
type lightDemandInputs struct {
	CurrentWorkers int
	DesiredChains  int
	Vacancies      int
	RotationSlots  float64
	MarginalRate   float64
	FleetAvgRate   float64
	RateDeclining  bool
	RateReadable   bool
}

// computeLightDemand is the pure C3-inverted worker-sizing math (unit-tested from fixtures,
// independent of the read sources).
func computeLightDemand(in lightDemandInputs) ClassDemand {
	slots := in.RotationSlots
	if slots <= 0 {
		slots = defaultLightRotationSlots
	}
	base := int(math.Ceil(float64(in.DesiredChains) * slots))
	demand := base + in.Vacancies
	return ClassDemand{
		Class:         HullClassLight,
		Demand:        demand,
		Current:       in.CurrentWorkers,
		MarginalRate:  in.MarginalRate,
		FleetAvgRate:  in.FleetAvgRate,
		RateDeclining: in.RateDeclining,
		RateReadable:  in.RateReadable,
		Readable:      true,
		Reason:        fmt.Sprintf("%d chains × %.2f rot = %d + %d vacancies = %d workers", in.DesiredChains, slots, base, in.Vacancies, demand),
	}
}

// unreadableLight is the fail-closed light demand (a core signal could not be read).
func unreadableLight(reason string) ClassDemand {
	return ClassDemand{Class: HullClassLight, Readable: false, Reason: reason}
}
