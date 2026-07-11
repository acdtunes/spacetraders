package types

import "github.com/andrescamacho/spacetraders-go/internal/domain/goods"

// RunFactoryWorkerCommand initiates a factory worker to produce a good
type RunFactoryWorkerCommand struct {
	PlayerID       int
	ShipSymbol     string
	ProductionNode *goods.SupplyChainNode
	FactoryID      string
	SystemSymbol   string
	CoordinatorID  string              // Optional: for signaling completion back to coordinator
	CompletionChan chan<- WorkerResult // Optional: channel for async completion signaling
}

// RunFactoryWorkerResponse contains the result of the worker operation
type RunFactoryWorkerResponse struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Completed        bool
	Error            string
}

// WorkerResult is sent via channel when worker completes
type WorkerResult struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Error            error
}

// RunFactoryCoordinatorCommand initiates a factory coordinator for fleet-based production
type RunFactoryCoordinatorCommand struct {
	PlayerID      int
	TargetGood    string
	SystemSymbol  string // Where to produce (defaults to current system)
	ContainerID   string // Container ID for ship assignment tracking
	MaxIterations int    // Maximum iterations to run (-1 for infinite, 0 for single run, >0 for specific count)
	// InputsOnly, when true, feeds the dependency tree but does NOT harvest the
	// fabricated output: the factory produces the target good and leaves it in its
	// export stock for a construction pipeline to source. This is the era-2 gate-fill
	// fix — a harvesting factory bought back its own 149 FAB_MATS and froze the fill
	// (sp-q02m). Default (false) preserves the original harvest-the-output behavior.
	InputsOnly bool
	// WorkingCapitalReserve unifies the factory input-buy spend floor with the fleet's
	// per-run reserve (sp-agzj): the effective floor enforced at each input buy is
	// max(50000, WorkingCapitalReserve), the 50k an immutable lower bound (RULINGS #5).
	// 0/absent leaves the standing 50k floor. Fed from the goods_factory launch config's
	// working_capital_reserve key, the same knob the tour/trade/arb coordinators run — so
	// a fleet reserving 1M no longer leaves its factories draining to 50k.
	WorkingCapitalReserve int
	// WorkingCapitalReserveTreasuryPct engages the sp-yqx4 counter-cyclical floor at each
	// input buy: the enforced floor becomes max(50k, min(WorkingCapitalReserve, pct% × live
	// treasury)) so a reserve above the treasury can no longer park every factory buy (the
	// deadlock that idled the tour fleet applies identically to factories). 0 leaves the
	// absolute floor in force; the goods_factory launch build resolves 0/absent to
	// common.DefaultReserveTreasuryPct (40) so production runs the proportional floor while a
	// command built directly (tests) keeps the absolute behavior.
	WorkingCapitalReserveTreasuryPct int
	// InputPriceCeilingMultiplier is the ladder-chase ceiling on factory INPUT buys (sp-iv65):
	// an input aborts when its live ask exceeds this multiple of the good's trailing-median ask.
	// 0/absent resolves to defaultInputPriceCeilingMultiplier (1.5) at the point of use — a
	// protective default that turns the GUARD on, not money movement (RULINGS #5). Fed from the
	// goods_factory launch config's input_price_ceiling_multiplier key.
	InputPriceCeilingMultiplier float64
	// InputPriceCeilingDisabled is the emergency off-switch for the ladder-chase ceiling
	// (RULINGS #5): true skips the guard entirely. Fed from input_price_ceiling_disabled.
	InputPriceCeilingDisabled bool
	// InputRescueMultiplier caps the supply-first sourcing rescue clause (sp-a5j7 Phase 2): a
	// SCARCE/LIMITED source is bought only when no eligible source exists AND its ask is within
	// this multiple of the trailing median. 0/absent → the 1.2 default. Fed from
	// input_rescue_multiplier.
	InputRescueMultiplier float64
	// InputEraEndPriceFirst flips sourcing to price-first for the era-end window (< T-6h), the
	// wedx exception. Fed from input_era_end_price_first.
	InputEraEndPriceFirst bool
	// InputSourcingDisabled is the RULINGS #5 escape hatch reverting sourcing to pure price-first.
	// Fed from input_sourcing_disabled.
	InputSourcingDisabled bool
}

// RunFactoryCoordinatorResponse contains the result of the coordinator operation
type RunFactoryCoordinatorResponse struct {
	FactoryID        string
	TargetGood       string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	ShipsUsed        int
	Completed        bool
	Error            string
	// NoWorkReason is set when the iteration completed cleanly (Error == "")
	// but performed no work at all — pre-spend guard park, or every claimable
	// node parked for lack of a claimable hull (sp-2q2o). A -1 (infinite)
	// caller uses this to back off before the next iteration instead of
	// spinning; it stays empty on any iteration that produced something.
	NoWorkReason string
}
