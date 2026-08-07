package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultHullFillFraction is the fraction of a hauler's hold the construction-supply drain fills
// per trip when no fraction is configured: the whole hull, so a construction buy tops the hold up
// toward capacity instead of stopping at one trade-volume tranche and paying a full round trip for
// a fraction of a load. A 0/absent value resolves here at the point of use — a protective default
// that turns the FILL on, not money movement (RULINGS #5); WithHullFillTarget's fraction parameter
// is the seam a per-run config sets to fill less.
const defaultHullFillFraction = 1.0

// hullFillCtxKey carries the per-trip hull-fill target for a construction-supply input buy. It
// rides on ctx (not a struct field) for the SAME singleton-executor race reason as the reserve /
// price-ceiling / sourcing configs: the ProductionExecutor is shared across every concurrent
// container, so a struct field would race between sibling drains; ctx is per-Handle and race-free.
type hullFillCtxKey struct{}

type hullFillTarget struct {
	billRemaining int     // units of this material the site still needs; <=0 => no bill cap (fill to fraction × hull)
	fraction      float64 // fraction of hull capacity to fill this trip; <=0 => defaultHullFillFraction
}

// WithHullFillTarget stamps the per-trip hull-fill target onto ctx so buyGood tops the hold up
// toward hull capacity for a construction-supply buy instead of carrying a single trade-volume
// tranche. billRemaining caps the fill at the material's outstanding bill so the drain never
// over-buys past demand; fraction (<=0 => full hull) parametrizes how much of the hold to fill
// (RULINGS #5). ONLY the construction drain stamps this; every other buyGood caller (goods-factory
// inputs) leaves it unset and keeps single-tranche behaviour.
func WithHullFillTarget(ctx context.Context, billRemaining int, fraction float64) context.Context {
	return context.WithValue(ctx, hullFillCtxKey{}, hullFillTarget{billRemaining: billRemaining, fraction: fraction})
}

// HullFillTargetFromContext reports the per-trip hull-fill target stamped by WithHullFillTarget.
// ok is false when no target was stamped — buyGood then keeps its single-tranche behavior.
func HullFillTargetFromContext(ctx context.Context) (billRemaining int, fraction float64, ok bool) {
	if v, vok := ctx.Value(hullFillCtxKey{}).(hullFillTarget); vok {
		return v.billRemaining, v.fraction, true
	}
	return 0, 0, false
}

// liveAsk re-reads the current EXPORT ask (purchase_price — what WE PAY) of good at waypoint from
// the market DB: the per-iteration price the hull-fill loop re-checks its guards against, so a
// laddering market is not chased tranche-by-tranche. ok is false when the market/good is unreadable
// or the ask is non-positive; the loop treats that as fail-CLOSED (stop, deliver what is aboard),
// never buying at an unknown price (RULINGS #4).
func (e *ProductionExecutor) liveAsk(ctx context.Context, waypoint, good string, playerID int) (int, bool) {
	marketData, err := e.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil || marketData == nil {
		return 0, false
	}
	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return 0, false
	}
	ask := tradeGood.PurchasePrice()
	if ask <= 0 {
		return 0, false
	}
	return ask, true
}

// ProductionExecutor orchestrates the production of goods by coordinating ship operations.
// It handles both purchasing goods from markets (BUY) and manufacturing them (FABRICATE).
type ProductionExecutor struct {
	mediator         common.Mediator
	shipRepo         navigation.ShipRepository
	marketRepo       market.MarketRepository
	marketLocator    *MarketLocator
	clock            shared.Clock
	pollingIntervals []time.Duration // Configurable polling intervals
	// apiClient live-reads treasury for the working-capital spend floor. nil disables the floor —
	// the fail-OPEN optional-port contract for fixtures that cannot supply a live client; the
	// daemon always wires the real one.
	apiClient domainPorts.APIClient
	// spendLedger is the cross-container concurrent spend cap. nil disables it, the same
	// fail-OPEN contract as apiClient. Injected by setter, not constructor, so the package's
	// fixtures and the executor's call sites stay untouched.
	spendLedger SpendReservationLedger
	// treasury is the LEDGER-backed reader BOTH factory money guards — the per-buy spend floor and
	// the concurrent-spend cap — read through instead of calling Get Agent before every input
	// tranche. nil leaves the direct apiClient read in place. Wired or not, an unreadable treasury
	// still PARKS the buy (fail closed).
	treasury TreasuryReader
	// priceHistory backs the RESCUE-BUY validator: the trailing-median-ask source a rescue
	// source-buy is capped against before it dispatches. nil does NOT disable the check and is NOT
	// the fail-OPEN contract apiClient/spendLedger have — a nil reader parks EVERY rescue buy
	// (trailingMedianAsk returns ok=false and rescueSource refuses on false), and the park logs "no
	// trailing median", indistinguishable from a market with no history. Unwired is silent.
	priceHistory InputPriceHistoryReader
	// constructionRepo backs the DeliverToConstructionSite terminal. nil leaves the terminal
	// unavailable (it errors if reached) — the optional-port contract; only the construction-supply
	// drain calls the terminal, so every other caller is unaffected.
	constructionRepo manufacturing.ConstructionSiteRepository
	// workSensor backs the per-operation capital budget: it answers whether the TRADE side is live,
	// which is what sizes construction's share of deployable capital. nil disables the budget and
	// leaves the flat reserve floor guarding alone — the same fail-OPEN contract as
	// apiClient/spendLedger, NOT priceHistory's. A wired-but-erroring sensor does NOT fail open:
	// see budgetedReserveFloor.
	workSensor common.CapitalWorkSensor
}

// NewProductionExecutor creates a new production executor with default polling intervals
func NewProductionExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *ProductionExecutor {
	return NewProductionExecutorWithConfig(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
		defaultProductionPollIntervals,
		apiClient,
	)
}

// NewProductionExecutorWithConfig creates a new production executor with custom polling intervals
func NewProductionExecutorWithConfig(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
	pollingIntervals []time.Duration,
	apiClient domainPorts.APIClient,
) *ProductionExecutor {
	// PollForProduction is clock-driven, and the construction daemon builds this executor directly
	// with a nil clock (unlike the factory path, which defaults nil->RealClock upstream in
	// NewRunFactoryCoordinatorHandler), so default it here or a construction path nil-panics on the
	// first e.clock.Now(). Applies ONLY when nil, so an injected mock clock is always honoured.
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ProductionExecutor{
		mediator:         mediator,
		shipRepo:         shipRepo,
		marketRepo:       marketRepo,
		marketLocator:    marketLocator,
		clock:            clock,
		pollingIntervals: pollingIntervals,
		apiClient:        apiClient,
	}
}

// ProductionResult contains the outcome of a production operation
type ProductionResult struct {
	QuantityAcquired int
	TotalCost        int
	WaypointSymbol   string // Where the good was acquired
}

// ProduceGood orchestrates the production of a good using the given ship.
// For BUY nodes: finds market, navigates, purchases whatever is available.
// For FABRICATE nodes: recursively produces inputs, delivers them, polls for output, purchases output.
// Returns the quantity acquired and total cost.
//
// inputsOnly applies to the OUTPUT of this node only: when true and this node is
// fabricated, its finished output is left in factory stock instead of being
// harvested. It never suppresses an input buy — the raw materials still
// have to be acquired and delivered — so buyGood ignores it, and fabricateGood
// forces it off when recursing into children (an intermediate fabricated input must
// be harvested so it can be delivered to the parent factory).
func (e *ProductionExecutor) ProduceGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool,
) (*ProductionResult, error) {
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	switch node.AcquisitionMethod {
	case goods.AcquisitionBuy:
		return e.buyGood(ctx, ship, node, systemSymbol, playerID)
	case goods.AcquisitionFabricate:
		return e.fabricateGood(ctx, fabricationRun{
			ship:         ship,
			node:         node,
			systemSymbol: systemSymbol,
			playerID:     playerID,
			opContext:    opContext,
			inputsOnly:   inputsOnly,
		})
	default:
		return nil, fmt.Errorf("unknown acquisition method: %s", node.AcquisitionMethod)
	}
}

// buyGood purchases a good from a market. The operation context rides on ctx (ProduceGood
// stamps it via shared.WithOperationContext), so it is not passed separately.
func (e *ProductionExecutor) buyGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
) (*ProductionResult, error) {
	ctx, marketResult, mode, parked, err := e.resolveInputSource(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}
	if parked != nil {
		return parked, nil
	}

	// The SELECTED-source path feeds a factory: its buys are sold into an import listing, so the
	// min-effective-delivery floor is the right one and is unchanged (sp-lpy9i).
	return e.fillFromSource(ctx, ship, node.Good, marketResult, systemSymbol, playerID, mode, SinkFactoryFeed)
}

// fillFromSource navigates to source, makes room, and runs the tranche loop until the hold,
// the trip target or a guard stops it.
//
// It is shared by the SELECTED-source path (buyGood, which resolves the market first) and the
// PINNED-source path (BuyAtTerminalFactory, whose market phase 1's topology already resolved).
// Extracted rather than duplicated on purpose: every money guard in this loop is re-checked per
// iteration and fails closed, and a second copy would be free to drift from this one silently —
// which is how a guard stops guarding without any test noticing.
//
// sink names what the goods are FOR, and is the ONLY thing that differs between the two callers'
// physics. It selects the shrink floor and nothing else — every money guard below runs identically
// for both sinks, in the same order, and still fails closed.
func (e *ProductionExecutor) fillFromSource(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	source *MarketLocatorResult,
	systemSymbol string,
	playerID int,
	mode inputSourceMode,
	sink TrancheSink,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	playerIDValue := shared.MustNewPlayerID(playerID)

	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), source.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	updatedShip, roomFreed := e.makeRoomForInputBuy(ctx, updatedShip, good, playerIDValue)
	if !roomFreed {
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   source.WaypointSymbol,
		}, nil
	}

	// A zero-volume market cannot be bought from (preserves the prior "trade volume is zero"
	// error for both the single-tranche and hull-fill paths).
	if source.TradeVolume <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", good)
	}

	fill := newHullFill(ctx, updatedShip, source, good)
	for {
		trancheQty := fill.nextTranche()
		if trancheQty <= 0 {
			break // STOP: hold full, or fill target / remaining bill met
		}

		ask := source.Price
		if fill.loopFill {
			liveAsk, ok := e.trancheAsk(ctx, fill, systemSymbol, playerID, mode)
			if !ok {
				break // STOP: unreadable ask or ceiling breach — deliver what is aboard, park the rest
			}
			ask = liveAsk
		}

		// Both money guards are re-checked EVERY iteration against live treasury and fail CLOSED
		// (RULINGS #4): once the NEXT tranche would breach, the loop SHRINKS it to what the reserve
		// can absorb, and stops only when even the minimum viable tranche will not fit.
		//
		// SHRINK, NOT BYPASS. Ending the fill outright on a breach kills a whole step whose ask has
		// risen, even when a smaller tranche would have cleared the reserve comfortably — and if
		// treasury is falling there is no self-recovery path, because waiting requires reserve plus
		// a FULL tranche to become affordable, which moves further away rather than closer.
		//
		// The guard below still runs on the SHRUNKEN size and still decides. Nothing here spends
		// against an unvalidated number: affordableTrancheUnits only PROPOSES, and a proposal the
		// guard then refuses stops the fill.
		projectedCost := trancheQty * ask
		if breached, enforcedFloor := e.spendFloorBreached(ctx, playerID, projectedCost); breached {
			// THE RESIZE RESCUES AN EMPTY LEG, AND ONLY AN EMPTY LEG.
			//
			// With units already aboard, stopping is right: the hull is not going home empty, the
			// factory gets fed, and grinding the treasury toward the floor in ever-smaller tranches
			// would buy little while spending the fleet's whole cushion.
			//
			// acquired == 0 is the case worth rescuing — one oversized first tranche otherwise kills
			// the whole leg, which returns with nothing and scores the step a failure.
			if fill.acquired > 0 {
				logSpendFloorStop(ctx, good, source.WaypointSymbol, fill.acquired, projectedCost, enforcedFloor)
				break
			}
			affordable := e.affordableTrancheUnits(ctx, playerID, ask, trancheQty)
			// THE FLOOR IS THE SINK'S, NOT THE LOOP'S. Feeding a factory it is the min-effective
			// delivery, below which a leg buys a dribble that moves the factory's activity nothing.
			// Delivering to a construction site there is no such threshold — the site consumes
			// against a bill — so the floor is a trip-economics bound instead, and a nearly-finished
			// material is never held hostage to a floor larger than what is left of it.
			minUnits := minTrancheUnitsFor(sink, fill.capacity)
			if affordable < minUnits {
				// Logged DISTINCTLY from the ordinary stop so "the reserve is genuinely too tight
				// for a useful buy" and "the full tranche happened not to fit" stay separable.
				logSpendFloorTooTightToShrink(ctx, good, source.WaypointSymbol, fill.acquired, trancheQty, affordable, minUnits, projectedCost, enforcedFloor)
				break
			}
			logSpendFloorShrink(ctx, good, source.WaypointSymbol, trancheQty, affordable, projectedCost, enforcedFloor)
			trancheQty = affordable
			projectedCost = trancheQty * ask
			// THE GUARD DECIDES, EVEN ON THE SIZE WE JUST PROPOSED. Re-running it is what keeps this
			// a resize rather than a relaxation: the shrunken buy has to pass the same commit-time
			// check the full one failed, on a fresh treasury read.
			if reBreached, reFloor := e.spendFloorBreached(ctx, playerID, projectedCost); reBreached {
				logSpendFloorStop(ctx, good, source.WaypointSymbol, fill.acquired, projectedCost, reFloor)
				break
			}
		}

		purchaseCmd := &shipCargo.PurchaseCargoCommand{
			ShipSymbol: updatedShip.ShipSymbol(),
			GoodSymbol: good,
			Units:      trancheQty,
			PlayerID:   playerIDValue,
		}

		response, parked, err := e.buyInputTranche(ctx, purchaseCmd, fill, playerID, projectedCost)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase cargo: %w", err)
		}
		if parked {
			break // STOP: concurrent cap (reserveConcurrentSpendOrPark logged the cause)
		}
		if response == nil {
			// Empty tranche persisted across the retry bound — the market is drained. STOP the fill
			// and deliver whatever is aboard (nothing yet if this was the first tranche).
			logEmptyTrancheStop(ctx, good, source.WaypointSymbol, fill.acquired)
			break
		}

		fill.record(response.UnitsAdded, response.TotalCost)
		logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, good, response.TotalCost), map[string]interface{}{
			"good":       good,
			"quantity":   response.UnitsAdded,
			"total_cost": response.TotalCost,
			"market":     source.WaypointSymbol,
		})

		if !fill.loopFill {
			break // single-tranche (goods-factory input) path: exactly one buy, unchanged
		}
		if response.UnitsAdded <= 0 {
			break // safety: no forward progress (never spin)
		}
	}

	return fill.result(), nil
}

// hullFill is one input buy's tranche-loop state: the source it draws from and how full the hold
// is. loopFill distinguishes the hull-filling construction path from the single-tranche
// goods-factory path, which buys exactly once.
type hullFill struct {
	source     *MarketLocatorResult
	good       string
	capacity   int
	tripTarget int
	loopFill   bool
	onboard    int
	acquired   int
	totalCost  int
}

func newHullFill(ctx context.Context, ship *navigation.Ship, source *MarketLocatorResult, good string) *hullFill {
	capacity := ship.Cargo().Capacity
	tripTarget, loopFill := inputTripTarget(ctx, capacity, source.TradeVolume)
	return &hullFill{
		source:     source,
		good:       good,
		capacity:   capacity,
		tripTarget: tripTarget,
		loopFill:   loopFill,
		onboard:    ship.Cargo().Units,
	}
}

func (f *hullFill) availableSpace() int { return f.capacity - f.onboard }

// nextTranche sizes the next buy against the free hold, the market's per-transaction volume and
// the remaining fill target. <=0 ends the fill.
func (f *hullFill) nextTranche() int {
	if f.availableSpace() <= 0 {
		return 0
	}
	want := f.tripTarget - f.acquired
	if want <= 0 {
		return 0
	}
	return min(f.availableSpace(), f.source.TradeVolume, want)
}

func (f *hullFill) record(units, cost int) {
	f.acquired += units
	f.onboard += units
	f.totalCost += cost
}

func (f *hullFill) result() *ProductionResult {
	return &ProductionResult{
		QuantityAcquired: f.acquired,
		TotalCost:        f.totalCost,
		WaypointSymbol:   f.source.WaypointSymbol,
	}
}

// buyInputTranche holds a cross-container spend reservation across exactly one market buy. The
// per-buy floor is a PER-CONTAINER live check, so N containers can each clear it inside their own
// check->buy window and collectively breach the reserve; this HARD cap serializes their in-flight
// spend through a shared ledger and PARKS if the combined exposure would breach.
//
// A nil response with parked false is an empty tranche the guard already bounded-retried: the
// market drained between the scout read and the buy, which is a skip, not a crash.
func (e *ProductionExecutor) buyInputTranche(
	ctx context.Context,
	purchaseCmd *shipCargo.PurchaseCargoCommand,
	fill *hullFill,
	playerID, projectedCost int,
) (*shipCargo.PurchaseCargoResponse, bool, error) {
	reservationID, parked := e.reserveConcurrentSpendOrPark(ctx, playerID, projectedCost, fill.source.WaypointSymbol, purchaseCmd.GoodSymbol)
	if parked {
		return nil, true, nil
	}
	defer e.releaseSpendReservation(ctx, playerID, reservationID)

	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Purchasing %d units of %s (cargo space: %d, trade_volume: %d, acquired so far: %d)", purchaseCmd.Units, fill.good, fill.availableSpace(), fill.source.TradeVolume, fill.acquired), nil)

	response, err := e.purchaseInputWithEmptyTrancheGuard(ctx, purchaseCmd)
	return response, false, err
}

// resolveInputSource picks the buy source by SUPPLY eligibility+ranking (MODERATE+,
// supply>activity>price) rather than price, so a depleting market is left rather than ridden
// down. A non-nil *ProductionResult is a zero-spend PARK the caller must return as-is.
//
// The returned ctx carries the selector branch down to the PURCHASE_CARGO ledger recorder, which
// stamps it into the transaction metadata — the only place supply-first compliance is gradable
// from. Only the input-buy path stamps it; every other cargo caller leaves it unset.
func (e *ProductionExecutor) resolveInputSource(
	ctx context.Context,
	good, systemSymbol string,
	playerID int,
) (context.Context, *MarketLocatorResult, inputSourceMode, *ProductionResult, error) {
	marketResult, mode, err := e.selectInputSource(ctx, good, systemSymbol, playerID)
	if err != nil {
		return ctx, nil, mode, nil, fmt.Errorf("failed to find market selling %s: %w", good, err)
	}
	if marketResult == nil || mode == sourceModeNone {
		// No eligible source and no valid rescue: PARK (the selector logged the cause). A
		// blocked chain waits for supply to regenerate rather than laddering a depleted market.
		return ctx, nil, mode, &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: ""}, nil
	}

	ctx = shared.WithSelectorBranch(ctx, mode.String())

	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Selected supply-first source for %s purchase (%s)", good, mode), map[string]interface{}{
		"good":            good,
		"market":          marketResult.WaypointSymbol,
		"price":           marketResult.Price,
		"activity":        marketResult.Activity,
		"supply":          marketResult.Supply,
		"trade_volume":    marketResult.TradeVolume,
		"selector_branch": mode.String(),
	})

	// The cross-market ceiling backstops the selector on the ELIGIBLE path only: the rescue path was
	// already validated by the rescue cap, and era-end/disabled are deliberately price-first, so
	// re-gating them would veto a decision already made. Ordered selector -> ceiling -> capital floor.
	if mode == sourceModeEligible && e.inputPriceCeilingParked(ctx, marketResult.WaypointSymbol, good, systemSymbol, playerID, marketResult.Price) {
		return ctx, marketResult, mode, &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: marketResult.WaypointSymbol}, nil
	}

	return ctx, marketResult, mode, nil, nil
}

// makeRoomForInputBuy unloads whatever is aboard when the hold is full, so a factory that did not
// drain its last output before buying the next input recovers instead of dying. Reports false when
// no space could be freed.
//
// No protectGood: this is an INPUT market, never the terminal product's factory/buy market, so the
// output is not carried here.
func (e *ProductionExecutor) makeRoomForInputBuy(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	playerID shared.PlayerID,
) (*navigation.Ship, bool) {
	if ship.Cargo().Capacity-ship.Cargo().Units > 0 {
		return ship, true
	}
	logger := common.LoggerFromContext(ctx)

	freedShip, sellErr := e.freeCargoSpace(ctx, ship, playerID, "")
	if sellErr != nil {
		logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this input purchase of %s", good), map[string]interface{}{
			"good":  good,
			"ship":  ship.ShipSymbol(),
			"error": sellErr.Error(),
		})
		return ship, false
	}
	if freedShip.Cargo().Capacity-freedShip.Cargo().Units <= 0 {
		logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this input purchase of %s", good), map[string]interface{}{
			"good": good,
			"ship": freedShip.ShipSymbol(),
		})
		return freedShip, false
	}
	return freedShip, true
}

// makeRoomForOutputHarvest unloads whatever is aboard when the hold is full at the factory.
// Reports false when no space could be freed — which loses nothing: the fabricated good stays in
// the factory's export stock for a later pass.
//
// It protects `good`: we are docked at the factory (the BUY market) to harvest, so dumping
// already-held output here to make room would ladder the factory's own bid down under us.
func (e *ProductionExecutor) makeRoomForOutputHarvest(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	playerID shared.PlayerID,
) (*navigation.Ship, bool) {
	if ship.Cargo().Capacity-ship.Cargo().Units > 0 {
		return ship, true
	}
	logger := common.LoggerFromContext(ctx)

	freedShip, sellErr := e.freeCargoSpace(ctx, ship, playerID, good)
	if sellErr != nil {
		logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
			"good":  good,
			"ship":  ship.ShipSymbol(),
			"error": sellErr.Error(),
		})
		return ship, false
	}
	if freedShip.Cargo().Capacity-freedShip.Cargo().Units <= 0 {
		logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
			"good": good,
			"ship": freedShip.ShipSymbol(),
		})
		return freedShip, false
	}
	return freedShip, true
}

// inputTripTarget sizes the units of one good to acquire this trip, and whether to loop tranches
// to reach it. A single SpaceTraders buy is capped at trade_volume, so filling a hold needs a LOOP.
//
// Unstamped (every goods-factory input) the target is ONE trade-volume tranche, leaving hold room
// for the factory's other inputs (RULINGS #2). A construction-supply buy stamps a hull-fill target
// so a round-trip carries ~a hull. A balanced-feed cap only ever LOWERS the target — an ample input
// is pulled down toward the scarce sibling's flow — and turns the loop on so a small trade volume
// still accumulates to the balanced tranche.
func inputTripTarget(ctx context.Context, capacity, tradeVolume int) (tripTarget int, loopFill bool) {
	billRemaining, fraction, fillMode := HullFillTargetFromContext(ctx)

	tripTarget = tradeVolume
	loopFill = fillMode
	if fillMode {
		if fraction <= 0 {
			fraction = defaultHullFillFraction
		}
		tripTarget = int(float64(capacity) * fraction)
		if billRemaining > 0 && billRemaining < tripTarget {
			tripTarget = billRemaining // never over-buy past the outstanding bill
		}
	}

	if feedCap, capped := inputFeedCapFromContext(ctx); capped {
		if feedCap < tripTarget {
			tripTarget = feedCap
		}
		loopFill = true
	}
	return tripTarget, loopFill
}

// trancheAsk re-reads the live ask before each hull-fill tranche and re-checks the cross-market
// ceiling, so a market laddering under our own draw is not chased tranche-by-tranche. Fails CLOSED
// (RULINGS #4): an unreadable ask reports false rather than buying blind.
func (e *ProductionExecutor) trancheAsk(
	ctx context.Context,
	fill *hullFill,
	systemSymbol string,
	playerID int,
	mode inputSourceMode,
) (int, bool) {
	good, waypoint, acquired := fill.good, fill.source.WaypointSymbol, fill.acquired
	liveAsk, ok := e.liveAsk(ctx, waypoint, good, playerID)
	if !ok {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Stopping hull-fill of %s at %s after %d units — could not re-read the live ask for the per-tranche price check (fail-closed); delivering what is aboard", good, waypoint, acquired), map[string]interface{}{
			"good": good, "market": waypoint, "acquired": acquired,
			"action": "hull_fill_stopped", "reason": "live_ask_unreadable",
		})
		return 0, false
	}
	if mode == sourceModeEligible && e.inputPriceCeilingParked(ctx, waypoint, good, systemSymbol, playerID, liveAsk) {
		return 0, false
	}
	return liveAsk, true
}

// logSpendFloorStop names the park cause IN THE MESSAGE — the container log renderer drops the
// metadata map — and distinguishes a trip that bought nothing from one stopping part-filled.
func logSpendFloorStop(ctx context.Context, good, waypoint string, acquired, projectedCost, enforcedFloor int) {
	logger := common.LoggerFromContext(ctx)
	if acquired == 0 {
		logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — would breach working-capital reserve (projected cost %d, reserve %d)", good, waypoint, projectedCost, enforcedFloor), map[string]interface{}{
			"good": good, "market": waypoint, "projected_cost": projectedCost,
			"action": "factory_parked", "reason": "spend_floor",
		})
		return
	}
	logger.Log("WARNING", fmt.Sprintf("Stopping purchase of %s at %s after %d units — next tranche would breach the working-capital reserve (projected cost %d, reserve %d)", good, waypoint, acquired, projectedCost, enforcedFloor), map[string]interface{}{
		"good": good, "market": waypoint, "projected_cost": projectedCost, "acquired": acquired,
		"action": "factory_parked", "reason": "spend_floor",
	})
}

// logEmptyTrancheStop distinguishes a drained market that sold nothing at all from one that
// exhausted part-way through a hull fill.
func logEmptyTrancheStop(ctx context.Context, good, waypoint string, acquired int) {
	logger := common.LoggerFromContext(ctx)
	if acquired == 0 {
		logger.Log("WARN", fmt.Sprintf("Skipped empty tranche for %s at %s — market sold 0 units; feeder continues", good, waypoint), map[string]interface{}{
			"good": good, "market": waypoint,
		})
		return
	}
	logger.Log("INFO", fmt.Sprintf("Stopping hull-fill of %s at %s after %d units — market stock exhausted; delivering what is aboard", good, waypoint, acquired), map[string]interface{}{
		"good": good, "market": waypoint, "acquired": acquired,
	})
}

// logSpendFloorShrink records a tranche RESIZED to fit the reserve. INFO, not a warning: the buy
// proceeds and the factory gets fed. Distinct from the stop lines below so "progress, just smaller"
// is never read as a park.
func logSpendFloorShrink(ctx context.Context, good, waypoint string, wanted, shrunk, wantedCost, enforcedFloor int) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Resized the %s tranche at %s from %d to %d units — the full %d would have cost %d against a reserve of %d, so the fill proceeds smaller rather than not at all",
		good, waypoint, wanted, shrunk, wanted, wantedCost, enforcedFloor), map[string]interface{}{
		"good": good, "market": waypoint, "wanted_units": wanted, "units": shrunk,
		"wanted_cost": wantedCost, "reserve": enforcedFloor,
		"action": "tranche_resized", "reason": "spend_floor_shrink",
	})
}

// logSpendFloorTooTightToShrink records the one case that still declines: the reserve cannot absorb
// even the min-effective tranche.
//
// IT IS A SEPARATE LINE FROM logSpendFloorStop ON PURPOSE. The ordinary stop means "the next
// tranche did not fit"; this means "no USEFUL tranche fits at all", a statement about the treasury
// rather than about this buy, and conflating the two makes a hard stall look like routine tranche
// accounting.
//
// THE BINDING CONSTRAINT LEADS THE MESSAGE, and that ordering is not cosmetic: the two numbers that
// decide the outcome are what the reserve affords and the minimum it must clear. Opening instead
// with the ATTEMPTED tranche invites a truncated read in which the attempted size looks like the
// minimum — i.e. that the floor tracks the market's trade_volume, a mechanism that does not exist.
func logSpendFloorTooTightToShrink(ctx context.Context, good, waypoint string, acquired, wanted, affordable, minUnits, wantedCost, enforcedFloor int) {
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Parked input purchase of %s at %s — reserve affords only %d units, below the %d-unit minimum for this sink: the %d-unit tranche would have cost %d against a reserve of %d. %d already aboard",
		good, waypoint, affordable, minUnits, wanted, wantedCost, enforcedFloor, acquired), map[string]interface{}{
		"good": good, "market": waypoint, "wanted_units": wanted, "affordable_units": affordable,
		"min_units": minUnits, "projected_cost": wantedCost, "reserve": enforcedFloor,
		"acquired": acquired,
		"action":   "factory_parked", "reason": "spend_floor_below_min_tranche",
	})
}
