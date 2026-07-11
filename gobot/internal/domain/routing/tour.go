package routing

import "time"

// Multi-hop trade-tour planning types (sp-1ek0 P1b). These are the Go-side
// mirror of the OptimizeTradeTour gRPC contract (pkg/proto/routing/routing.proto):
// the daemon assembles a market snapshot + waypoint coordinates, the stateless
// Python planner returns an ordered TourPlan, and the tour_run container executes
// it leg-by-leg with live price re-verification. The domain leg type is TourLeg;
// the proto leg is TradeTourLeg only because OptimizeFueledTour already owns the
// proto name TourLeg (there is no such clash in this package).

// TourGoodSnapshot is one (waypoint, good) row of the request-carried market
// snapshot. Ask is what the hull PAYS to buy (the market SELL column /
// SellPrice); Bid is what the hull RECEIVES selling to it (the market BUY column
// / PurchasePrice) — the same orientation GoodListing uses. ObservedAt stamps the
// freshness of the cached market row so the planner can exclude stale prices.
type TourGoodSnapshot struct {
	Waypoint    string
	System      string
	Good        string
	Supply      string
	Activity    string
	Ask         int
	Bid         int
	TradeVolume int
	ObservedAt  time.Time
}

// TourWaypoint carries coordinates for one distinct market waypoint so the
// planner prices travel time for real. Assembled Go-side from the waypoints
// table; an empty list makes the planner fall back to flat travel defaults with
// a logged warning (harbormaster amendment 2026-07-09).
type TourWaypoint struct {
	Symbol string
	System string
	X      int
	Y      int
}

// TourShipState is the touring hull's position, capacity and current cargo.
// Cargo maps good symbol to units currently aboard (flow-conservation input for
// the solver, and the cumulative-actuals basis a re-plan resumes from).
type TourShipState struct {
	ShipSymbol      string
	CurrentWaypoint string
	CurrentSystem   string
	HoldCapacity    int
	FuelCurrent     int
	FuelCapacity    int
	EngineSpeed     int
	Cargo           map[string]int
}

// TourConstraints binds the solver: hop budget, money guards, system scope, and
// the expected model version. ExpectedModelVersion MUST be set to "<fit_version>@<era>"
// (read from the checked-in artifact at launch) — the solver FAILS CLOSED when it
// is unset, and errors loudly on a mismatch rather than silently using a stale model.
type TourConstraints struct {
	MaxHops               int
	MinMarginPerUnit      int
	MaxSnapshotAgeMinutes int
	MaxSpend              int64
	WorkingCapitalReserve int64
	AllowedSystems        []string
	ExpectedModelVersion  string
}

// TourTrade is one buy or sell tranche at a leg. ExpectedUnitPrice is the
// curve-adjusted price the planner projected for this tranche; the executor
// re-verifies it live before trading.
//
// IsDeposit marks a SELL-side tranche that is a haul-to-storage DEPOSIT into the
// home warehouse (sp-dchv Lane C), not a market sale: ExpectedUnitPrice is the
// synthetic bid (= home_ask, the contract-savings value), the executor deposits
// via ReserveSpace/TransferCargo/ConfirmDeposit and books ZERO revenue, and it
// skips the live-price re-verify (no market bid exists to drift against). IsBuy
// is always false for a deposit.
type TourTrade struct {
	Good              string
	Units             int
	ExpectedUnitPrice int
	IsBuy             bool
	IsDeposit         bool
	// IsStock marks a BUY tranche that WITHDRAWS factory output from warehouse stock
	// at cost basis (C1, sp-64je) rather than buying at market. ExpectedUnitPrice is
	// the recorded basis; the executor withdraws instead of purchasing.
	IsStock bool
}

// TourDepositCandidate is one home-warehouse pre-positioning offer the daemon
// assembles for the planner (sp-dchv Lane C): a good the contract history
// recurrently needs, cheaper in a reachable foreign system, that may be
// DEPOSITED into the home warehouse instead of arb-sold. UnitsWanted is the
// Go-side cap (min of remaining contract demand, remaining warehouse space, and
// the pre-positioning capital ceiling); SyntheticBid is the flat sink price
// (= home_ask). The solver treats (StorageWaypoint, Good) as a no-decay,
// no-A-cap synthetic sink absorbing at most UnitsWanted units.
type TourDepositCandidate struct {
	Good            string
	UnitsWanted     int
	SyntheticBid    int
	StorageWaypoint string
	StorageSystem   string
}

// TourStockSource is one planner-visible-stock withdrawal offer (C1, sp-64je):
// factory output stocked in a warehouse at a recorded cost basis that the tour may
// WITHDRAW at basis instead of buying our own output at the laddered market ask.
// The buy-side mirror of TourDepositCandidate. UnitsAvailable is the reservable
// on-hand stock (net of outstanding cross-tour reservations); UnitAsk is the
// weighted-average cost basis (the flat source price).
type TourStockSource struct {
	Good            string
	UnitsAvailable  int
	UnitAsk         int
	StorageWaypoint string
	StorageSystem   string
}

// TourMarketAbsorption is one (waypoint, good, side) pool's outstanding
// cross-container depth the daemon assembles from the absorption ledger and hands the
// planner to NET out of available tranche depth (sp-78ai L3). The daemon decays the
// EXECUTED recovery residual on the fitted half-life curve BEFORE building this, so the
// solver stays clock-free. PlannedUnits (in-flight PLANNED from other containers)
// advances the price schedule AND consumes capacity; RecoveringUnits (the decayed
// EXECUTED residual) consumes capacity ONLY — the live quote already reflects the crush
// (the price-honesty split, design §2). An empty absorption list plans against full
// depth, byte-identical to pre-sp-78ai (additive-field contract).
type TourMarketAbsorption struct {
	Waypoint        string
	Good            string
	Side            string
	PlannedUnits    int
	RecoveringUnits float64
}

// TourLeg is one market stop of the planned tour. Trades are ordered for
// execution (the planner emits SELLS before BUYS within a leg); ProjectedLegProfit
// is the planner's projection and TravelSecondsFromPrev prices the hop into it.
type TourLeg struct {
	Waypoint              string
	System                string
	Trades                []TourTrade
	ProjectedLegProfit    int64
	TravelSecondsFromPrev int
}

// TourPlan is the planner's ordered answer. Feasible=false carries a structured
// InfeasibleReason (model_artifact_missing, model_version_mismatch,
// no_profitable_tour, ...) so the executor can fail open cleanly to single-lane
// trading. TopRejected mirrors the lane-selection observability: the top-3
// alternative tours the solver declined, each a "<summary> — <reason>" line.
type TourPlan struct {
	Feasible                bool
	InfeasibleReason        string
	Legs                    []TourLeg
	ProjectedProfit         int64
	ProjectedCreditsPerHour float64
	// HeldLiquidation is the slice of ProjectedProfit that is REVENUE from
	// selling cargo already aboard at plan time (launch-liquidation legs — sell
	// tranches with no paired buy). ProjectedProfit stays the TOTAL that ranks
	// tour selection so pure-liquidation tours remain plannable (Admiral ruling
	// C, sp-bc27); this field is reporting-only, letting the planned-manifest log
	// split fresh-trade profit (ProjectedProfit - HeldLiquidation) from
	// liquidation revenue.
	HeldLiquidation int64
	// DepositValue is the slice of ProjectedProfit that is SYNTHETIC savings value
	// from haul-to-storage DEPOSIT legs (sp-dchv Lane C): sum of units*SyntheticBid
	// over deposit tranches (SyntheticBid = home_ask). It is NOT cash — a deposit
	// books zero realized revenue; the value is realized later when a contract
	// sources the good from inventory. ProjectedProfit stays the TOTAL (fresh cash
	// arb + this synthetic value) so the solver ranks deposit legs against arb
	// sells; this field lets the planned-manifest log report fresh cash profit
	// (ProjectedProfit - HeldLiquidation - DepositValue) apart from pre-positioning
	// value (mirrors HeldLiquidation, sp-bc27).
	DepositValue int64
	ModelVersion string
	TopRejected  []string
}
