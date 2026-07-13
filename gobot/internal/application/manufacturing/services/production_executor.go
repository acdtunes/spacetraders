package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// productionDockConfirmAttempts bounds how many times NavigateAndDock will reload
// and re-issue a dock while waiting for the ship to reach a confirmed DOCKED
// state (arrival + persisted dock). Bounded so a wedged ship can never spin
// forever.
const productionDockConfirmAttempts = 10

// productionDockRetryLimit bounds how many times a cargo transaction that fails
// with a transient "must be docked" signal is re-docked and retried before the
// error is surfaced. Bounded so a genuinely undockable ship can never infinite
// loop (sp-n7yp feeder crash #3).
const productionDockRetryLimit = 3

// productionEmptyTrancheRetryLimit bounds how many times an input buy that comes
// back empty ("partial failure: ... 0 units processed" / API 400 — a market drained
// between the scout read and the buy) is retried before the tranche is skipped so the
// feeder run can continue. Bounded so a structurally-empty market can never
// infinite-loop (sp-q02m feeder crash #4).
const productionEmptyTrancheRetryLimit = 3

// productionEmptyTrancheRetryDelay is the backoff between empty-tranche retries,
// giving the market a chance to refill. It runs on the injected clock, so it is a
// no-op under the test clock.
const productionEmptyTrancheRetryDelay = 2 * time.Second

// productionDwellWarnThreshold bounds how long PollForProduction can wait on a
// fabrication without escalating its logging. Below this, the existing sparse
// "every 5th attempt" INFO cadence is enough; past it, a WARNING fires on
// EVERY attempt so a factory holding docked hull claims for tens of minutes
// (sp-npyr: SHIP_PARTS held TORWIND-3/6 for 40+ min with only sparse logging,
// reading as a silent stall from the outside) has its wait reason visible in
// the logs at the true claim-holding site.
const productionDwellWarnThreshold = 5 * time.Minute

// minOutputSellMarginFactor is the bid>=basis loss floor enforced on every
// fabricated-OUTPUT sale (sp-rqwm). The harvested product is sold at the resale
// sink the chain-margin guard priced only if that sink's live bid is at least the
// unit basis — the factory ask we paid to harvest — times this factor. 1.0 = strict
// breakeven: the output leg never realizes a loss. It is the last-line backstop to
// the sp-2dv4 chain-margin guard and the bp6f #3 crushed-sink harvest guard, checked
// at the actual point of the output sale where the sink bid may have decayed since
// production started (the −258k MEDICINE incident: guard cleared vs sink A1@5,248, the
// worker instead dumped the output at the factory's own ~1,560 bid). Below the floor
// the output is HELD (parked), never dumped. Tunable per ruling #5; kept at breakeven
// so a healthy sink is never over-restricted.
const minOutputSellMarginFactor = 1.0

// defaultWorkingCapitalReserve is the IMMUTABLE lower bound of the working-capital
// spend floor applied to factory INPUT purchases (sp-9aoc). It mirrors bp6f's
// trade-circuit floor (the identically-named const in run_trade_route_coordinator.go)
// and closes the same drain class one layer over — re-enabling 4 goods factories at
// ~848k drained the float to 23k in ~1min because bp6f guarded trade circuits but NOT
// factory input buys.
//
// sp-agzj unifies this with the fleet's per-run working-capital reserve (the
// working_capital_reserve launch-config key the tour/trade/arb coordinators already
// run): the EFFECTIVE floor is effectiveReserveFloor's max(50000, configured). A
// factory that ran this stale 50k while the fleet reserved 1M legally rode the balance
// to a ~617k trough (the sp-agzj incident's second half). Config-level unification is
// allowed; per-run WEAKENING below 50k is not (RULINGS #5) — a configured reserve under
// 50k is clamped UP, so this stays the non-tunable floor an over-eager re-enable can
// never zero out.
const defaultWorkingCapitalReserve = 50000

// reserveCtxKey carries the per-run configured working-capital reserve from the factory
// coordinator (RunFactoryCoordinatorCommand.WorkingCapitalReserve, resolved from the
// working_capital_reserve launch-config key) down to the point of spend. It rides on ctx
// because the ProductionExecutor is a SINGLETON shared across every concurrent factory
// container (main.go constructs it once) — a struct field would be a data race between
// sibling factories running different reserves; ctx is per-Handle and race-free.
type reserveCtxKey struct{}

// WithConfiguredReserve stamps the per-run working-capital reserve onto ctx (sp-agzj).
// The floor actually enforced is effectiveReserveFloor's max(defaultWorkingCapitalReserve,
// configured); a 0/absent value simply leaves the immutable 50k floor in force.
func WithConfiguredReserve(ctx context.Context, reserve int) context.Context {
	return context.WithValue(ctx, reserveCtxKey{}, reserve)
}

// effectiveReserveFloor resolves the working-capital floor to enforce at a factory input
// buy: the GREATER of the immutable defaultWorkingCapitalReserve (50k) and the per-run
// configured reserve carried on ctx (sp-agzj). A configured reserve BELOW 50k is clamped
// UP to 50k — the floor may be unified with the fleet's config or RAISED, never weakened
// below its non-tunable lower bound (RULINGS #5). A reserve ABOVE it (the fleet's 1M) is
// honored, so the factory input floor tracks the same reserve the rest of the fleet runs.
func effectiveReserveFloor(ctx context.Context) int {
	configured := 0
	if v, ok := ctx.Value(reserveCtxKey{}).(int); ok {
		configured = v
	}
	if configured > defaultWorkingCapitalReserve {
		return configured
	}
	return defaultWorkingCapitalReserve
}

// resolveInputBuyFloor applies the sp-yqx4 counter-cyclical resolution to the ABSOLUTE
// factory floor (effectiveReserveFloor's max(50k, configured)) once the live treasury is
// known. When a coordinator has stamped a treasury-percent on ctx, the enforced floor
// becomes max(50k, min(absolute, pct% × liveTreasury)) — so a factory input buy is not
// deadlocked by a reserve above the treasury, the same trough that idled the tour fleet.
// With no pct stamped (the sp-agzj/sp-kk61 default and every direct test) it returns the
// absolute floor UNCHANGED, keeping the guard exactly as before. It logs when the
// proportional floor binds below the absolute — the watch's counter-cyclical signal.
//
// liveTreasury MUST be a readable balance: both call sites fail CLOSED (park) on an
// unreadable read BEFORE reaching here, so the LOWERED proportional floor is never
// computed against a treasury the guard could not see (RULINGS #4).
func resolveInputBuyFloor(ctx context.Context, absolute, liveTreasury int) int {
	pct, ok := common.ReserveTreasuryPctFromContext(ctx)
	if !ok {
		return absolute
	}
	effective := int(common.EffectiveReserveFloor(int64(absolute), pct, int64(liveTreasury)))
	if effective < absolute {
		common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
			"Counter-cyclical factory working-capital floor engaged: proportional floor %d (%d%% of live treasury %d) below the configured %d reserve (sp-yqx4)",
			effective, pct, liveTreasury, absolute), map[string]interface{}{
			"effective_floor": effective, "configured_reserve": absolute, "treasury_pct": pct, "live_treasury": liveTreasury,
		})
	}
	return effective
}

// SpendReservationLedger is the cross-container concurrent factory-input spend cap
// (sp-w3he). The per-buy floor (sp-9aoc) checks live treasury per container, but N factory
// containers can each pass that independent check inside the check->buy window and
// collectively dip below the reserve. This ledger closes that race using shared DB state:
// a factory records its spend intent and, in one serialized atomic step, verifies live
// treasury minus the SUM of all active in-flight reservations still clears the reserve.
//
// Reserve reports ok==false when the combined spend would breach (caller PARKS) and rolls
// the reservation back. On ok==true the caller Releases the returned id after the buy.
// ExpireStale reclaims reservations a dead container never released.
type SpendReservationLedger interface {
	Reserve(ctx context.Context, playerID int, containerID string, projectedCost, liveCredits, reserveFloor int) (reservationID string, ok bool, err error)
	Release(ctx context.Context, reservationID string) error
	ExpireStale(ctx context.Context, maxAge time.Duration) (int, error)
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
	// apiClient live-reads treasury for the working-capital spend floor (sp-9aoc).
	// nil disables the floor — the fail-OPEN contract for the package's test fixtures
	// that cannot supply a live client; the daemon always wires the real one (main.go).
	apiClient domainPorts.APIClient
	// spendLedger is the cross-container concurrent spend cap (sp-w3he). nil disables it —
	// the same optional-port fail-OPEN contract as apiClient (tests pass nothing; the daemon
	// wires the real DB-backed ledger via SetSpendLedger). Injected by setter, not constructor,
	// so the package's existing test fixtures and the executor's many call sites stay untouched.
	spendLedger SpendReservationLedger
	// priceHistory backs the input price ceiling (sp-iv65): the trailing-median-ask source a
	// factory input buy is checked against before it dispatches. nil disables the ceiling — the
	// same optional-port fail-OPEN contract as apiClient/spendLedger (tests wire nothing; the
	// daemon wires the DB-backed price history repo via SetPriceHistoryReader).
	priceHistory InputPriceHistoryReader
	// constructionRepo backs the DeliverToConstructionSite terminal (sp-382j): the construction
	// supply API a sourced hauler delivers gate materials through. nil leaves the terminal
	// unavailable (returns an error if reached) — the optional-port contract; the daemon wires
	// the real API-backed repo via SetConstructionRepo, and only the construction-supply drain
	// ever calls the terminal, so every other caller is unaffected.
	constructionRepo manufacturing.ConstructionSiteRepository
}

// SetSpendLedger wires the cross-container concurrent spend cap (sp-w3he). The daemon calls
// this after construction (main.go, via the coordinator handler); leaving it unset keeps the
// cap fail-open, which is exactly what every non-daemon caller wants.
func (e *ProductionExecutor) SetSpendLedger(ledger SpendReservationLedger) {
	e.spendLedger = ledger
}

// SetConstructionRepo wires the construction supply API the DeliverToConstructionSite terminal
// delivers gate materials through (sp-382j). The daemon calls this when it builds the executor
// for the construction-supply drain; leaving it unset keeps the terminal unavailable, which is
// exactly what every non-construction caller (goods factory, tour, arb) wants.
func (e *ProductionExecutor) SetConstructionRepo(repo manufacturing.ConstructionSiteRepository) {
	e.constructionRepo = repo
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
		[]time.Duration{30 * time.Second, 60 * time.Second}, // Default intervals
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
// harvested (sp-q02m). It never suppresses an input buy — the raw materials still
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
	// Add operation context to Go context for transaction tagging
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	switch node.AcquisitionMethod {
	case goods.AcquisitionBuy:
		return e.buyGood(ctx, ship, node, systemSymbol, playerID, opContext)
	case goods.AcquisitionFabricate:
		return e.fabricateGood(ctx, ship, node, systemSymbol, playerID, opContext, inputsOnly)
	default:
		return nil, fmt.Errorf("unknown acquisition method: %s", node.AcquisitionMethod)
	}
}

// buyGood purchases a good from a market
func (e *ProductionExecutor) buyGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Supply-first source selection (sp-a5j7 Phase 2 — wedx restoration): choose the buy source
	// by SUPPLY eligibility+ranking (MODERATE+, supply>activity>price), restoring the original
	// SupplyChainResolver design the runtime input path bypassed for price-first. The selector
	// RE-SOURCES to a healthy market instead of riding a depleting one down — the leading-
	// indicator fix for every input blowup this era (parts -220k, the micro chase, electronics
	// -891k, the -6.6M furnace, all begun at a SCARCE/LIMITED source). It returns the mode so
	// buyGood applies the right downstream guards (the eligible path faces the cross-market
	// ceiling; rescue/era-end were already price-validated inside the selector).
	marketResult, mode, err := e.selectInputSource(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market selling %s: %w", node.Good, err)
	}
	if marketResult == nil || mode == sourceModeNone {
		// No eligible source and no valid rescue: PARK (the selector logged the cause). A
		// blocked chain waits for supply to regenerate rather than laddering a depleted market.
		return &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: ""}, nil
	}

	// Tag this input buy with the selector branch that chose the source (sp-br0m): it rides ctx
	// down to the PURCHASE_CARGO ledger recorder (cargo_transaction.recordCargoTransaction),
	// which stamps it into the transaction metadata beside good_symbol. That makes A1
	// (supply-first compliance) gradable straight from the ledger — an ELIGIBLE buy is a healthy
	// supply-first pick, a RESCUE buy the legal single-source-degraded exception — and arms the
	// rescue-rate mis-siting tripwire, which cannot be reconstructed from the row otherwise. Only
	// the input-buy path stamps it; the fabricated-output harvest and every non-factory cargo
	// caller leave it unset, so their rows are unchanged.
	ctx = shared.WithSelectorBranch(ctx, mode.String())

	logger.Log("INFO", fmt.Sprintf("Selected supply-first source for %s purchase (%s)", node.Good, mode), map[string]interface{}{
		"good":            node.Good,
		"market":          marketResult.WaypointSymbol,
		"price":           marketResult.Price,
		"activity":        marketResult.Activity,
		"supply":          marketResult.Supply,
		"trade_volume":    marketResult.TradeVolume,
		"selector_branch": mode.String(),
	})

	// The cross-market price ceiling applies on the ELIGIBLE path only — the BACKSTOP to the
	// supply-first selector, catching a chosen eligible source priced anomalously above its
	// healthy peers (the poison-proof cross-market baseline, sp-a5j7 Phase 2 / hzz5 X4). The
	// rescue path was already validated by the 1.2x rescue cap and the era-end/disabled paths
	// are intentional price-first, so re-gating them would veto a deliberate decision. The
	// selector's MODERATE+ eligibility SUBSUMES the interim per-buy supply gate (sp-a5j7 Phase 1):
	// a SCARCE/LIMITED source is never SELECTED here, so a buy-time supply park is redundant —
	// the leading-indicator defense is now selection, not a post-selection veto. Ordered
	// selector → ceiling → capital-floor (below).
	if mode == sourceModeEligible {
		if e.inputPriceCeilingParked(ctx, marketResult.WaypointSymbol, node.Good, systemSymbol, playerID, marketResult.Price) {
			return &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: marketResult.WaypointSymbol}, nil
		}
	}

	// Navigate to market and dock
	playerIDValue := shared.MustNewPlayerID(playerID)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), marketResult.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	// Calculate purchase quantity (capped by cargo space and trade volume)
	availableSpace := updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
	if availableSpace <= 0 {
		// sp-mu6u: a full hold used to crash the feeder outright here. We're
		// already docked at this market, so try to sell whatever is onboard to
		// free space before giving up — a factory that didn't unload its last
		// output before buying the next input should recover, not die.
		//
		// sp-rqwm: no protectGood here. This is an INPUT market (where a feed is
		// bought), never the terminal product's factory/buy market; the output is
		// drained at its resale sink before the next input run, so it is not carried
		// here, and the acceptance ("zero sells at the lift's buy market") concerns
		// the factory, not input markets.
		freedShip, sellErr := e.freeCargoSpace(ctx, updatedShip, playerIDValue, "")
		if sellErr != nil {
			logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this input purchase of %s", node.Good), map[string]interface{}{
				"good":  node.Good,
				"ship":  updatedShip.ShipSymbol(),
				"error": sellErr.Error(),
			})
			return &ProductionResult{
				QuantityAcquired: 0,
				TotalCost:        0,
				WaypointSymbol:   marketResult.WaypointSymbol,
			}, nil
		}
		updatedShip = freedShip
		availableSpace = updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
		if availableSpace <= 0 {
			logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this input purchase of %s", node.Good), map[string]interface{}{
				"good": node.Good,
				"ship": updatedShip.ShipSymbol(),
			})
			return &ProductionResult{
				QuantityAcquired: 0,
				TotalCost:        0,
				WaypointSymbol:   marketResult.WaypointSymbol,
			}, nil
		}
	}

	// Cap at trade volume to leave room for other inputs
	purchaseQty := min(availableSpace, marketResult.TradeVolume)
	if purchaseQty <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", node.Good)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of %s (cargo: %d, trade_volume: %d)", purchaseQty, node.Good, availableSpace, marketResult.TradeVolume), nil)

	// Working-capital spend floor (sp-9aoc): refuse this input buy BEFORE it dispatches
	// if paying for it would drop live treasury below the reserve. This is the per-buy
	// backstop to bp6f's circuit-level floor — 4 goods factories buying inputs
	// concurrently with no floor drained 848k→23k in ~1min, the same drain class the
	// trade incident had. chain_margin_guard (sp-2dv4) sits UPSTREAM at the coordinator
	// on projected chain margin; this is the last-line check at the actual point of spend.
	projectedCost := purchaseQty * marketResult.Price
	if e.spendFloorBreached(ctx, projectedCost) {
		// PARK: zero-spend result, mirroring the recoverable-condition returns above
		// (full-hold skip, empty-tranche skip). The numbers go IN THE MESSAGE too
		// (sp-iqyq) — the container log renderer drops the metadata map, so a park
		// hidden only in {"projected_cost": ...} never reaches an operator.
		logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — would breach working-capital reserve (projected cost %d, reserve %d)", node.Good, marketResult.WaypointSymbol, projectedCost, effectiveReserveFloor(ctx)), map[string]interface{}{
			"good":           node.Good,
			"market":         marketResult.WaypointSymbol,
			"projected_cost": projectedCost,
			"action":         "factory_parked",
			"reason":         "spend_floor",
		})
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   marketResult.WaypointSymbol,
		}, nil
	}

	// Cross-container concurrent spend cap (sp-w3he): the floor above is a PER-CONTAINER
	// live check, so N factory containers can each clear it inside their own check->buy
	// window and collectively breach the reserve. This HARD cap serializes all factories'
	// in-flight input spend through a shared DB ledger — it records this buy's intent and
	// PARKS if the combined exposure would breach. Kept as a SECOND gate (not folded into
	// the floor) so each guard owns a distinct, legible park reason and sp-9aoc stays intact.
	reservationID, parked := e.reserveConcurrentSpendOrPark(ctx, playerID, projectedCost, marketResult.WaypointSymbol, node.Good)
	if parked {
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   marketResult.WaypointSymbol,
		}, nil
	}
	if reservationID != "" {
		// Release on EVERY exit below (success, empty-tranche skip, or error) — defer covers
		// them all. A failed release only leaks until the staleness sweep reclaims it.
		defer e.releaseSpendReservation(ctx, reservationID)
	}

	// Purchase cargo (capped by trade volume)
	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: updatedShip.ShipSymbol(),
		GoodSymbol: node.Good,
		Units:      purchaseQty,
		PlayerID:   playerIDValue,
	}

	// Dispatch through the empty-tranche guard: a transient "must be docked" is still
	// re-docked and retried inside (sp-n7yp); an empty / zero-volume tranche
	// ("partial failure: ... 0 units processed" / API 400) is bounded-retried then
	// skipped so the feeder survives instead of crashing the container (sp-q02m crash #4).
	response, err := e.purchaseInputWithEmptyTrancheGuard(ctx, purchaseCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase cargo: %w", err)
	}
	if response == nil {
		// Empty tranche persisted across the retry bound: skip this input with a
		// zero-unit result and let the run continue rather than crashing the container.
		logger.Log("WARN", fmt.Sprintf("Skipped empty tranche for %s at %s — market sold 0 units; feeder continues", node.Good, marketResult.WaypointSymbol), map[string]interface{}{
			"good":   node.Good,
			"market": marketResult.WaypointSymbol,
		})
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   marketResult.WaypointSymbol,
		}, nil
	}

	logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, node.Good, response.TotalCost), map[string]interface{}{
		"good":       node.Good,
		"quantity":   response.UnitsAdded,
		"total_cost": response.TotalCost,
		"market":     marketResult.WaypointSymbol,
	})

	return &ProductionResult{
		QuantityAcquired: response.UnitsAdded,
		TotalCost:        response.TotalCost,
		WaypointSymbol:   marketResult.WaypointSymbol,
	}, nil
}

// spendFloorBreached reports whether buying an input tranche costing projectedCost
// would drop live treasury below defaultWorkingCapitalReserve. It mirrors bp6f's trade
// floor (spendFloorBreached in run_trade_route_coordinator.go): a live GetAgent read
// checked right before the buy commits, so the caller can PARK instead of spending.
//
// Fails OPEN when no apiClient is wired (e.apiClient == nil): the guard is simply
// unavailable — the optional-port contract the package's test fixtures rely on (they
// pass nil), never the daemon, which always wires the real client.
//
// Fails CLOSED on every live-read failure (an unresolvable player token, or GetAgent
// itself erroring): a guard whose whole job is keeping treasury above the reserve must
// never let a buy through just because it went blind. An API hiccup here parks the
// input rather than spending unseen — the factory-side analogue of bp6f's fail-closed.
func (e *ProductionExecutor) spendFloorBreached(ctx context.Context, projectedCost int) bool {
	logger := common.LoggerFromContext(ctx)
	if e.apiClient == nil {
		return false
	}

	// Effective floor = max(50k, per-run configured reserve) (sp-agzj): unified with
	// the fleet's working_capital_reserve, 50k an immutable lower bound (RULINGS #5).
	reserve := effectiveReserveFloor(ctx)

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		// Numbers/cause in the MESSAGE (sp-iqyq): the container log renderer drops the
		// metadata map, so a blind fail-closed park must name its cause in the text.
		logger.Log("WARNING", fmt.Sprintf("Could not resolve player token for factory spend-floor check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return true
	}

	agentData, err := e.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not read live treasury for factory spend-floor check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return true
	}

	// sp-yqx4: below ~2.5M treasury the proportional floor binds so a factory input buy can
	// still proceed (a floor above the treasury would deadlock the factory as it did the tour
	// fleet). No-op when no pct is stamped — the absolute floor above stands unchanged.
	reserve = resolveInputBuyFloor(ctx, reserve, agentData.Credits)

	if agentData.Credits-projectedCost < reserve {
		logger.Log("WARNING", fmt.Sprintf("Factory input buy would breach the working-capital reserve — treasury %d, projected cost %d, reserve %d", agentData.Credits, projectedCost, reserve), map[string]interface{}{
			"treasury": agentData.Credits, "projected_cost": projectedCost, "reserve": reserve,
		})
		return true
	}

	return false
}

// reserveConcurrentSpendOrPark records this input buy's spend intent in the shared ledger
// and reports whether it must PARK because the COMBINED in-flight factory spend would
// breach the reserve (sp-w3he). On the proceed path it returns the reservation id the
// caller must Release after the buy.
//
// Fails OPEN when the cap is unavailable (no ledger wired, or no apiClient to read live
// treasury) — the same optional-port contract as the per-buy floor, so every non-daemon
// caller is unaffected. Fails CLOSED (parks) on any live-read or ledger error: a cap whose
// job is protecting the reserve must never let a buy through blind.
//
// The live treasury read here is deliberately independent of spendFloorBreached (sp-9aoc,
// left unchanged): factory input buys are low-frequency — one per market visit, after a
// multi-second navigate+dock — so the second read is negligible next to keeping the two
// guards decoupled, each with its own legible park reason. The read stays OUTSIDE the
// ledger transaction (passed in as a value) so the DB is never held open across the API call.
func (e *ProductionExecutor) reserveConcurrentSpendOrPark(ctx context.Context, playerID, projectedCost int, market, good string) (reservationID string, parked bool) {
	logger := common.LoggerFromContext(ctx)
	if e.spendLedger == nil || e.apiClient == nil {
		return "", false
	}

	// Same unified floor as the per-buy check (sp-agzj): the concurrent cap must serialize
	// against the SAME reserve the per-container floor enforces, or the two guards would
	// disagree on where the line is. max(50k, configured), 50k immutable (RULINGS #5).
	reserve := effectiveReserveFloor(ctx)

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not resolve player token for factory concurrent-spend-cap check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}

	agentData, err := e.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not read live treasury for factory concurrent-spend-cap check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}

	// sp-yqx4: serialize the concurrent cap against the SAME counter-cyclical floor the
	// per-buy check enforces (they must not disagree on where the line is). No-op with no pct
	// stamped — the absolute floor stands, unchanged from sp-w3he.
	reserve = resolveInputBuyFloor(ctx, reserve, agentData.Credits)

	// Container id attributes the reservation to the owning factory (already threaded into
	// ctx by the coordinator, sp-9aoc's operation context). Best-effort: the staleness sweep
	// is time-based, so a missing id never affects correctness, only log/debug attribution.
	containerID := "factory-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	resID, ok, err := e.spendLedger.Reserve(ctx, playerID, containerID, projectedCost, agentData.Credits, reserve)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Factory concurrent-spend-cap ledger error — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}
	if !ok {
		// Numbers in the MESSAGE (sp-iqyq): the container log renderer drops the metadata map,
		// so the cause — combined in-flight factory spend breaching the reserve — must be legible
		// in the text or an operator never sees why this factory parked.
		logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — cross-container concurrent spend cap: live treasury %d minus in-flight factory reservations would breach the working-capital reserve %d (this buy %d)", good, market, agentData.Credits, reserve, projectedCost), map[string]interface{}{
			"good":           good,
			"market":         market,
			"projected_cost": projectedCost,
			"treasury":       agentData.Credits,
			"reserve":        reserve,
			"action":         "factory_parked",
			"reason":         "concurrent_spend_cap",
		})
		return "", true
	}

	return resID, false
}

// releaseSpendReservation consumes a spend reservation after its buy completes (success or
// failure). A failed release is logged, never surfaced: the reservation simply leaks until
// the staleness sweep reclaims it, so cleanup can never fail an otherwise-successful buy.
func (e *ProductionExecutor) releaseSpendReservation(ctx context.Context, reservationID string) {
	if e.spendLedger == nil || reservationID == "" {
		return
	}
	if err := e.spendLedger.Release(ctx, reservationID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to release factory spend reservation %s (staleness sweep will reclaim it): %v", reservationID, err), map[string]interface{}{
			"reservation_id": reservationID,
			"error":          err.Error(),
		})
	}
}

// fabricateGood manufactures a good by producing inputs and delivering them to a manufacturing waypoint
func (e *ProductionExecutor) fabricateGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	totalCost := 0

	// Step 0: Check if factory already has ABUNDANT supply - skip input production if so
	// This allows opportunistic collection when factory already has goods ready
	factoryMarket, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find factory (export market) for %s: %w", node.Good, err)
	}

	// Check current supply at factory
	playerIDValue := shared.MustNewPlayerID(playerID)
	stockedResult, err := e.collectExistingFactorySupply(ctx, ship, node, factoryMarket, playerIDValue, opContext, inputsOnly, systemSymbol)
	if err != nil {
		return nil, err
	}
	if stockedResult != nil {
		return stockedResult, nil
	}

	// Chain-level negative-margin gate (sp-iv65): before buying ANY input, refuse a
	// fabrication that is structurally underwater — summed input ask already above the
	// output's resale bid — even when no single input trips the per-buy ceiling. Scoped to
	// resale runs (inputs-only construction supply has no resale sink and is governed by the
	// construction pipeline's own economics + the bp6f #3 harvest guard), mirroring the
	// coordinator ChainMarginGuard's scope. sp-qmp8 extends that scoping to the new
	// harvest-into-hauler construction model: a construction-supply run delivers its output to
	// the gate, never resells it, so a resale-margin veto would wrongly park the gate fill. The
	// INPUT buys below still pass the full money-guard stack (RULINGS #4). Parks with a
	// zero-spend result like the other recoverable-condition returns in this file.
	if !inputsOnly && !shared.ConstructionSupplyFromContext(ctx) && len(node.Children) > 0 && e.inputRoundMarginParked(ctx, node, systemSymbol, playerID) {
		return &ProductionResult{
			QuantityAcquired: 0,
			TotalCost:        0,
			WaypointSymbol:   factoryMarket.WaypointSymbol,
		}, nil
	}

	// Step 1: Recursively produce all required inputs
	logger.Log("INFO", fmt.Sprintf("Starting fabrication of %s (requires %d inputs)", node.Good, len(node.Children)), map[string]interface{}{
		"good":        node.Good,
		"input_count": len(node.Children),
	})

	for _, child := range node.Children {
		// Children are inputs that must be harvested and delivered to THIS factory —
		// inputs-only never suppresses their acquisition, so force it off here.
		result, err := e.ProduceGood(ctx, ship, child, systemSymbol, playerID, opContext, false)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
		logger.Log("INFO", fmt.Sprintf("Produced input: %d units of %s (cost: %d credits)", result.QuantityAcquired, child.Good, result.TotalCost), map[string]interface{}{
			"input_good": child.Good,
			"quantity":   result.QuantityAcquired,
			"cost":       result.TotalCost,
		})
	}

	// Step 2: Navigate to factory (already found above in Step 0)
	// CRITICAL: We need an EXPORT market (factory that produces and sells cheap),
	// NOT an import market (consumer that buys at high price).
	// The factory EXPORTS the finished good (low sell price) and IMPORTS the inputs.

	logger.Log("INFO", fmt.Sprintf("Found factory (export market) for %s at %s", node.Good, factoryMarket.WaypointSymbol), map[string]interface{}{
		"good":       node.Good,
		"waypoint":   factoryMarket.WaypointSymbol,
		"sell_price": factoryMarket.Price, // Factory's sell price (what we pay to buy)
	})

	// Step 3: Navigate to factory and dock (playerIDValue already created in Step 0)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Step 4: Deliver all inputs by selling cargo to the factory
	// The factory IMPORTS the inputs (we sell to them)
	deliveryRevenue, err := e.deliverInputs(ctx, updatedShip, playerIDValue, opContext)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver inputs: %w", err)
	}
	totalCost -= deliveryRevenue

	logger.Log("INFO", "Delivered inputs to factory", map[string]interface{}{
		"good":             node.Good,
		"waypoint":         factoryMarket.WaypointSymbol,
		"delivery_revenue": deliveryRevenue,
	})

	// Step 5: Poll for production until output good supply increases, then purchase
	// The factory EXPORTS the finished good (we buy from them at their sell price).
	// In inputs-only mode the poll still confirms the output was produced, but the
	// harvest is skipped so the good is left in factory stock (sp-q02m).
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext, inputsOnly, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed during production polling: %w", err)
	}

	totalCost += cost

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   factoryMarket.WaypointSymbol,
	}, nil
}

func (e *ProductionExecutor) collectExistingFactorySupply(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	factoryMarket *MarketLocatorResult,
	playerIDValue shared.PlayerID,
	opContext *shared.OperationContext,
	inputsOnly bool,
	systemSymbol string,
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	marketData, err := e.marketRepo.GetMarketData(ctx, factoryMarket.WaypointSymbol, playerIDValue.Value())
	if err != nil || marketData == nil {
		return nil, nil
	}
	tradeGood := marketData.FindGood(node.Good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return nil, nil
	}
	supply := *tradeGood.Supply()
	if !isHighOrAbundant(supply) {
		return nil, nil
	}

	logger.Log("INFO", fmt.Sprintf("Factory already has %s supply of %s - skipping input production", supply, node.Good), map[string]interface{}{
		"good":    node.Good,
		"factory": factoryMarket.WaypointSymbol,
		"supply":  supply,
	})

	// Navigate directly to factory and purchase
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Purchase the goods directly (PollForProduction will find them immediately since supply is HIGH/ABUNDANT).
	// In inputs-only mode the harvest is skipped, so the already-abundant stock is left for construction to source.
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext, inputsOnly, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase from factory: %w", err)
	}

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        cost,
		WaypointSymbol:   factoryMarket.WaypointSymbol,
	}, nil
}

// PollForProduction polls the market database until the output good appears in exports.
// Uses exponential backoff with NO timeout - polls indefinitely until good appears or context cancelled.
// Returns quantity purchased and cost.
func (e *ProductionExecutor) PollForProduction(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
	inputsOnly bool, // when true, confirm production then LEAVE the output in factory stock (skip the harvest)
	systemSymbol string, // system to search for a resale sink when checking the crushed-sink guard (bp6f #3)
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	// Use configured polling intervals (or defaults if not set)
	intervals := e.pollingIntervals
	if len(intervals) == 0 {
		intervals = []time.Duration{
			30 * time.Second, // Initial poll - catch fast production
			60 * time.Second, // Settled interval
		}
	}

	attempt := 0
	pollStart := e.clock.Now()
	for {
		// Check for context cancellation (daemon stop, user command, etc.)
		select {
		case <-ctx.Done():
			return 0, 0, fmt.Errorf("production polling cancelled: %w", ctx.Err())
		default:
			// Continue polling
		}

		// Query market data from database (kept fresh by scout tours)
		marketData, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID.Value())
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get market data during polling: %w", err)
		}

		// Check if good appears in exports
		tradeGood := marketData.FindGood(good)
		if tradeGood != nil {
			logger.Log("INFO", fmt.Sprintf("Production complete: %s now available at %s (polled %d times)", good, waypointSymbol, attempt+1), map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"poll_attempts": attempt + 1,
				"sell_price":    tradeGood.SellPrice(),
			})

			// Construction-support (inputs-only) mode: production is confirmed and the
			// output now sits in the factory's export stock. Do NOT harvest it — leave
			// it for the construction pipeline to be the sole buyer. Harvesting here is
			// exactly what starved the era-2 gate fill: the factory bought back its own
			// 149 FAB_MATS and froze the fill at 898/1600 for ~6h (sp-q02m).
			if inputsOnly {
				logger.Log("INFO", fmt.Sprintf("inputs-only: %s produced and left in factory stock at %s — harvest skipped", good, waypointSymbol), map[string]interface{}{
					"good":     good,
					"waypoint": waypointSymbol,
				})
				return 0, 0, nil
			}

			// bp6f #3: the trade crisis (over-buying + emergency liquidation) crushed
			// home sinks - e.g. D40 ADV_CIRC's bid fell 7000->2191 - so factories kept
			// harvesting output and reselling it below their own harvest cost:
			// loss-making production on every cycle. Compare the downstream resale bid
			// against what we're about to pay to harvest (tradeGood.SellPrice, the
			// factory's own ask). Fail OPEN (harvest anyway) if no sink can be found at
			// all - that's normal for goods with no direct resale market, not a signal
			// to stop production.
			//
			// sp-qmp8: a construction-supply harvest delivers its output to the jump-gate
			// site, NOT to a resale sink, so this resale-margin guard does not apply — skip
			// straight to the harvest. The construction pipeline's own economics govern the
			// gate fill (the Admiral's primary objective), and the INPUT buys already passed
			// the full money-guard stack (RULINGS #4). Without this a market that happens to
			// import the gate material at a crushed bid would wrongly park the gate fill.
			if !shared.ConstructionSupplyFromContext(ctx) {
				if sink, sinkErr := e.marketLocator.FindImportMarket(ctx, good, systemSymbol, playerID.Value()); sinkErr == nil && sink != nil {
					harvestCost := tradeGood.SellPrice()
					if sink.Price < harvestCost {
						logger.Log("WARNING", fmt.Sprintf(
							"Parking %s at %s: crushed sink - resale bid %d at %s is below harvest cost %d, producing would lose money",
							good, waypointSymbol, sink.Price, sink.WaypointSymbol, harvestCost,
						), map[string]interface{}{
							"action":       "factory_parked",
							"reason":       "crushed_sink",
							"good":         good,
							"waypoint":     waypointSymbol,
							"sink":         sink.WaypointSymbol,
							"sink_bid":     sink.Price,
							"harvest_cost": harvestCost,
						})
						return 0, 0, nil
					}
				}
			}

			return e.purchaseFabricatedOutput(ctx, good, waypointSymbol, shipSymbol, playerID, tradeGood.TradeVolume())
		}

		// Log polling attempt. Past productionDwellWarnThreshold, escalate to a
		// WARNING on EVERY attempt with the elapsed dwell stated in the message
		// text itself (the container-log renderer prints only level+message and
		// drops metadata, sp-iqyq) so a long fabrication wait is observable
		// rather than reading as a silent stall (sp-npyr).
		elapsed := e.clock.Now().Sub(pollStart)
		if elapsed >= productionDwellWarnThreshold {
			logger.Log("WARNING", fmt.Sprintf(
				"Still waiting on %s at %s after %s (ship %s, attempt %d) — fabrication in progress, not stalled",
				good, waypointSymbol, elapsed.Round(time.Second), shipSymbol, attempt+1,
			), map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"ship":          shipSymbol,
				"attempt":       attempt + 1,
				"elapsed_sec":   elapsed.Seconds(),
				"next_wait_sec": intervals[min(attempt, len(intervals)-1)].Seconds(),
			})
		} else if attempt == 0 || attempt%5 == 0 { // Log every 5th attempt to reduce noise
			logger.Log("INFO", "Polling for production completion", map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"attempt":       attempt + 1,
				"next_wait_sec": intervals[min(attempt, len(intervals)-1)].Seconds(),
			})
		}

		// Calculate wait interval
		intervalIndex := attempt
		if intervalIndex >= len(intervals) {
			intervalIndex = len(intervals) - 1 // Use last interval for all subsequent attempts
		}
		waitDuration := intervals[intervalIndex]

		// Wait before next poll
		// Create a timer for the wait duration
		timer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, 0, fmt.Errorf("production polling cancelled during wait: %w", ctx.Err())
		case <-timer.C:
			// Continue to next poll attempt
		}

		attempt++
	}
}

func (e *ProductionExecutor) purchaseFabricatedOutput(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
	tradeVolume int,
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to reload ship: %w", err)
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		// sp-wwhu: sibling of sp-mu6u's crash, on the harvest side — a full hold
		// used to crash the container outright here. We're already docked at this
		// market to harvest, so try to sell whatever is onboard to free space
		// first. Unlike a skipped INPUT purchase, a skipped output harvest loses
		// nothing: the fabricated good stays in the factory's export stock and is
		// picked up on a later pass, so skip gracefully rather than die.
		//
		// sp-rqwm: protect `good` — the fabricated output. We are docked at the
		// factory (the buy market) to harvest; dumping already-held output here to
		// make room is the −258k incident. Skipping it means a parked resale sink
		// holds the output onboard rather than deferring into a make-room dump.
		freedShip, sellErr := e.freeCargoSpace(ctx, ship, playerID, good)
		if sellErr != nil {
			logger.Log("WARN", fmt.Sprintf("Hold full and could not unload existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
				"good":  good,
				"ship":  ship.ShipSymbol(),
				"error": sellErr.Error(),
			})
			return 0, 0, nil
		}
		ship = freedShip
		availableSpace = ship.Cargo().Capacity - ship.Cargo().Units
		if availableSpace <= 0 {
			logger.Log("WARN", fmt.Sprintf("Hold still full after unloading existing cargo — skipping this output harvest of %s", good), map[string]interface{}{
				"good": good,
				"ship": ship.ShipSymbol(),
			})
			return 0, 0, nil
		}
	}

	purchaseQty := min(availableSpace, tradeVolume)
	if purchaseQty <= 0 {
		return 0, 0, fmt.Errorf("trade volume is zero for %s", good)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of fabricated %s (cargo: %d, trade_volume: %d)", purchaseQty, good, availableSpace, tradeVolume), nil)

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      purchaseQty,
		PlayerID:   playerID,
	}

	// Same dock-retry guard as the raw-buy path: a transient "must be docked"
	// re-docks and retries rather than crashing the container (sp-n7yp).
	response, err := e.purchaseWithDockRetry(ctx, purchaseCmd)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to purchase fabricated output: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Purchased fabricated output: %d units of %s for %d credits", response.UnitsAdded, good, response.TotalCost), map[string]interface{}{
		"good":       good,
		"quantity":   response.UnitsAdded,
		"total_cost": response.TotalCost,
		"waypoint":   waypointSymbol,
	})

	return response.UnitsAdded, response.TotalCost, nil
}

// SellFabricatedOutputAtSink binds the fabricated OUTPUT sale to the resale sink the
// chain-margin guard (sp-2dv4) priced — NEVER the factory/buy market — closing the
// guard-vs-execution divergence that bled −258k on MEDICINE (sp-rqwm): the guard
// cleared a chain against sink A1@5,248 while execution accumulated the output at the
// factory D39 and dumped it THERE via the make-room path, laddering D39's own bid down
// to ~1,560 (far below the ~3,100 harvest cost) and re-buying.
//
// The sink is re-derived LIVE at sell time via the IDENTICAL MarketLocator.FindImportMarket
// call the guard and the bp6f #3 harvest guard use, so execution sells where the guard
// planned rather than at the ship's current market. It enforces the bid>=basis loss floor
// (minOutputSellMarginFactor): if no sink can be priced, or the sink's live bid is below
// the unit basis (the factory ask we paid to harvest) times the floor, the output is HELD
// onboard (parked, zero sold) with the numbers in the message text — the fabricated good
// is retried on a later pass, never dumped at a loss. It never falls back to the current market.
//
// Only the FINAL product is sold this way (the coordinator calls it for the root
// fabrication node on a resale run); an intermediate feed is delivered to its parent fab
// and inputs-only leaves the output in factory stock, so both skip this leg. It also
// drains any output a prior parked sell left onboard, so a recovered sink clears the
// backlog. Returns realized sell revenue (0 when parked).
func (e *ProductionExecutor) SellFabricatedOutputAtSink(
	ctx context.Context,
	shipSymbol string,
	good string,
	unitBasis int, // credits paid per unit to harvest (the factory ask) — the loss-floor basis
	systemSymbol string,
	playerID shared.PlayerID,
	opContext *shared.OperationContext,
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	// Units of the output actually onboard right now — this cycle's fresh harvest plus
	// any the ship still carries from a prior parked sell.
	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to reload ship before output sale: %w", err)
	}
	units := onboardUnits(ship, good)
	if units <= 0 {
		return 0, nil // nothing harvested to sell (e.g. inputs-only, or a skipped harvest)
	}

	// Re-derive the resale sink LIVE — the same call the guard priced against. A
	// vanished/unpriceable sink PARKS the output (held, not dumped): we NEVER fall back
	// to selling at the current (factory/buy) market.
	sink, err := e.marketLocator.FindImportMarket(ctx, good, systemSymbol, playerID.Value())
	if err != nil || sink == nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Holding %d %s onboard: no priceable resale sink (basis %d/u) — NOT selling at the factory/buy market, will retry next pass: %v",
			units, good, unitBasis, err,
		), map[string]interface{}{
			"action": "output_sell_parked", "reason": "no_sink", "good": good, "units": units, "basis": unitBasis,
		})
		return 0, nil
	}

	// Bid>=basis loss floor (sp-rqwm fix b) — the guard on the output SELL dispatch.
	floor := int(float64(unitBasis) * minOutputSellMarginFactor)
	if sink.Price < floor {
		logger.Log("WARNING", fmt.Sprintf(
			"Holding %d %s onboard: resale sink %s bid %d below loss floor %d (basis %d/u × %.2f) — parking, NOT dumping at the factory. stages[hold %s bid%d<floor%d]",
			units, good, sink.WaypointSymbol, sink.Price, floor, unitBasis, minOutputSellMarginFactor, good, sink.Price, floor,
		), map[string]interface{}{
			"action": "output_sell_parked", "reason": "bid_below_basis",
			"good": good, "units": units, "sink": sink.WaypointSymbol, "sink_bid": sink.Price, "basis": unitBasis, "floor": floor,
		})
		return 0, nil
	}

	// Fly the sell leg to the sink and sell THERE. Factory legs are in-system by design,
	// so this is a NavigateAndDock (never a jump).
	docked, err := e.NavigateAndDock(ctx, shipSymbol, sink.WaypointSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to navigate to resale sink %s for %s: %w", sink.WaypointSymbol, good, err)
	}

	sellUnits := onboardUnits(docked, good)
	if sellUnits <= 0 {
		return 0, nil
	}

	sellCmd := &shipCargo.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      sellUnits,
		PlayerID:   playerID,
	}
	resp, err := e.mediator.Send(ctx, sellCmd)
	if err != nil {
		return 0, fmt.Errorf("failed to sell %s at resale sink %s: %w", good, sink.WaypointSymbol, err)
	}
	sellResp, ok := resp.(*shipCargo.SellCargoResponse)
	if !ok {
		return 0, fmt.Errorf("unexpected response type selling %s at resale sink %s", good, sink.WaypointSymbol)
	}

	logger.Log("INFO", fmt.Sprintf(
		"Sold %d %s at resale sink %s for %d credits (bid %d/u >= basis %d/u) — bound to the guard's sink, not the factory market",
		sellResp.UnitsSold, good, sink.WaypointSymbol, sellResp.TotalRevenue, sink.Price, unitBasis,
	), map[string]interface{}{
		"good": good, "units": sellResp.UnitsSold, "revenue": sellResp.TotalRevenue,
		"sink": sink.WaypointSymbol, "sink_bid": sink.Price, "basis": unitBasis,
	})
	return sellResp.TotalRevenue, nil
}

// DeliverToConstructionSite flies an ALREADY-SOURCED hauler to a jump-gate construction site and
// supplies whatever it carries of good via the construction supply API, returning the units the
// site accepted (sp-382j). It is the delivery TERMINAL of the construction-supply drain: the drain
// sources the material into the hull with ProduceGood (the shared engine — no duplicate sourcing
// logic), then hands off here to deliver. Modeled structurally on SellFabricatedOutputAtSink — the
// sale terminal's twin — reusing NavigateAndDock so a laden hull reaches a CONFIRMED-DOCKED state at
// the site before the supply fires, rather than resurrecting the deleted parallel coordinator's own
// navigation. Recovered from the acquire->navigate->supply->record leg of the sp-jav2-deleted
// DeliverToConstructionExecutor (ef2281b8), minus the acquire (now ProduceGood's job) and the
// task/pipeline bookkeeping (now the drain's job).
//
// A hull carrying nothing of good is a clean no-op (0 delivered, no flight) — the drain only reaches
// here after a non-zero ProduceGood, but the guard keeps a stale/empty hull from flying uselessly.
func (e *ProductionExecutor) DeliverToConstructionSite(
	ctx context.Context,
	shipSymbol string,
	good string,
	site string,
	playerID shared.PlayerID,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	if e.constructionRepo == nil {
		return 0, fmt.Errorf("construction repository not wired: cannot supply %s to %s", good, site)
	}

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to reload ship before construction delivery: %w", err)
	}
	if onboardUnits(ship, good) <= 0 {
		return 0, nil // nothing of this material onboard — nothing to deliver
	}

	// Fly the delivery leg to the site and dock. Construction legs are in-system by design, so
	// this is a NavigateAndDock (never a jump), returning only once CONFIRMED docked at the site.
	docked, err := e.NavigateAndDock(ctx, shipSymbol, site, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to navigate to construction site %s for %s: %w", site, good, err)
	}

	units := onboardUnits(docked, good)
	if units <= 0 {
		return 0, nil // arrived empty (e.g. cargo shed en route) — nothing to supply
	}

	result, err := e.constructionRepo.SupplyMaterial(ctx, shipSymbol, site, good, units, playerID.Value())
	if err != nil {
		// Surface the underlying supply error VERBATIM in the message so it reaches the container
		// log stream (structured map fields are dropped by the renderer). Recovered from the
		// deleted executor's supply-error handling.
		logger.Log("ERROR", fmt.Sprintf("Construction supply failed for %s at %s: %v", good, site, err), map[string]interface{}{
			"ship": shipSymbol, "construction_site": site, "good": good, "units": units,
		})
		return 0, fmt.Errorf("failed to supply construction site %s with %s: %w", site, good, err)
	}

	logger.Log("INFO", fmt.Sprintf("Supplied %d %s to construction site %s", result.UnitsDelivered, good, site), map[string]interface{}{
		"ship": shipSymbol, "construction_site": site, "good": good, "units_delivered": result.UnitsDelivered,
	})
	return result.UnitsDelivered, nil
}

// onboardUnits sums how many units of good the ship currently holds.
func onboardUnits(ship *navigation.Ship, good string) int {
	units := 0
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == good {
			units += item.Units
		}
	}
	return units
}

// NavigateAndDock navigates to a waypoint and returns the ship only once it is
// CONFIRMED docked — the dock is actually persisted via the API, not merely
// flipped to DOCKED in memory.
//
// The previous implementation pre-mutated the reloaded ship with EnsureDocked in
// its arrival poll, then handed that already-DOCKED ship to DockShipCommand. That
// made the dock a no-op: runStateTransition sees EnsureDocked report "no change"
// and short-circuits before calling the API, so the ship stayed IN_ORBIT in the
// DB while the code believed it was docked. The very next PurchaseCargoCommand
// reloaded IN_ORBIT and crashed the container with "ship must be docked"
// (sp-n7yp feeder crash #3). We now detect arrival WITHOUT mutating the ship and
// dock via a symbol-only command so the handler reloads the real IN_ORBIT state
// and the API dock actually fires, then re-read and assert DOCKED before
// returning.
func (e *ProductionExecutor) NavigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}
	if _, err := e.mediator.Send(ctx, navigateCmd); err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", destination, err)
	}

	return e.dockAndConfirm(ctx, shipSymbol, destination, playerID)
}

// dockAndConfirm waits for the ship to arrive, issues a real (API-backed) dock,
// and returns only after re-reading a persisted DOCKED state. Bounded by
// productionDockConfirmAttempts so a wedged ship can never spin forever.
//
// Critically, it never acts on a ship it mutated in memory: each attempt reloads
// a fresh ship, and the dock is issued via a symbol-only DockShipCommand so the
// handler loads the true (IN_ORBIT) state and EnsureDocked reports a real change
// — otherwise the dock short-circuits to a no-op and the buy races an unpersisted
// dock (sp-n7yp).
func (e *ProductionExecutor) dockAndConfirm(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	var ship *navigation.Ship
	for attempt := 0; attempt < productionDockConfirmAttempts; attempt++ {
		reloaded, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
		ship = reloaded

		if ship.IsDocked() {
			return ship, nil // confirmed: persisted DOCKED
		}

		if ship.IsInTransit() {
			// Still travelling — wait for arrival, then re-read.
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("dock wait cancelled: %w", ctx.Err())
			default:
				e.clock.Sleep(1 * time.Second)
			}
			continue
		}

		// Arrived and in orbit: issue a real dock. Pass ShipSymbol (nil Ship) so
		// DockShipHandler loads the true IN_ORBIT state, EnsureDocked reports a
		// change, and the API dock actually fires + persists.
		if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
			ShipSymbol: shipSymbol,
			PlayerID:   playerID,
		}); err != nil {
			return nil, fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
		}
		// Honor cancellation between issuing the dock and re-reading; loop back
		// immediately to confirm the persisted state (no mandatory sleep on the
		// happy path).
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("dock wait cancelled: %w", err)
		}
	}

	if ship != nil && ship.IsInTransit() {
		return nil, fmt.Errorf("ship %s still in transit after %d attempts", shipSymbol, productionDockConfirmAttempts)
	}
	return nil, fmt.Errorf("ship %s did not reach a confirmed DOCKED state at %s after %d attempts", shipSymbol, destination, productionDockConfirmAttempts)
}

// isTransientDockStateError reports whether err is the recoverable "ship must be
// docked" signal — the local precondition error (cargo_transaction.go) or the
// API's 4214/4244 codes — rather than a genuine failure (insufficient funds, no
// cargo space, ...). Only these are safe to retry after re-docking.
func isTransientDockStateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "must be docked") ||
		strings.Contains(msg, "4214") ||
		strings.Contains(msg, "4244")
}

// isEmptyTrancheError reports whether err is the "bought nothing" signal from an
// input buy — the cargo handler's "partial failure: ... 0 units processed" wrapper
// (cargo_transaction.go), raised when the first tranche's API call fails because the
// market's supply was drained between the scout read and the buy (an empty /
// zero-volume tranche, surfaced by the API as a 400).
//
// A genuine funds shortfall also processes zero units, so it too carries that phrase
// — but it is NOT an empty tranche and must surface as a real failure (mirroring how
// this file treats insufficient funds elsewhere). We therefore explicitly exclude it,
// so only a truly empty/zero-volume tranche is eligible for retry-then-skip
// (sp-q02m feeder crash #4).
func isEmptyTrancheError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "0 units processed") {
		return false
	}
	if strings.Contains(msg, "insufficient") {
		return false // genuine funds failure — must surface, never be silently skipped
	}
	return true
}

// purchaseWithDockRetry dispatches a PurchaseCargoCommand and, if it fails with a
// transient dock-state signal, reconciles the ship from the API (clearing any
// stale DOCKED cache entry that would make a re-dock a no-op — the subtlety
// NegotiateContractHandler documents), re-docks, and retries. Bounded by
// productionDockRetryLimit. A transient dock state must never crash the container
// (sp-n7yp feeder crash #3); genuine failures surface immediately, unretried.
func (e *ProductionExecutor) purchaseWithDockRetry(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionDockRetryLimit; attempt++ {
		resp, err := e.mediator.Send(ctx, cmd)
		if err == nil {
			response, ok := resp.(*shipCargo.PurchaseCargoResponse)
			if !ok {
				return nil, fmt.Errorf("unexpected response type from purchase command")
			}
			return response, nil
		}

		if !isTransientDockStateError(err) {
			return nil, err // genuine failure — surface immediately
		}

		lastErr = err
		if attempt == productionDockRetryLimit {
			break
		}

		logger.Log("WARN", "Purchase hit a transient dock-state error; re-docking and retrying", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		if rerr := e.redockFromAPI(ctx, cmd.ShipSymbol, cmd.PlayerID); rerr != nil {
			return nil, fmt.Errorf("failed to re-dock after transient dock error: %w", rerr)
		}
	}

	return nil, fmt.Errorf("purchase still failing after %d dock retries: %w", productionDockRetryLimit, lastErr)
}

// purchaseInputWithEmptyTrancheGuard dispatches an input buy and survives an empty /
// zero-volume tranche instead of crashing the container (sp-q02m feeder crash #4).
//
// Dock-state transients are still absorbed by the inner purchaseWithDockRetry. If the
// buy comes back empty ("partial failure: ... 0 units processed" / API 400 — the
// market drained between the scout read and the buy), we bounded-retry in case the
// supply refills, then report a SKIP so the caller can continue with a zero-unit
// result rather than dying unrecoverably. Genuine failures (insufficient funds,
// no cargo space, exhausted dock retries) surface immediately.
//
// Returns:
//   - (resp, nil): a successful buy
//   - (nil,  nil): the tranche stayed empty across the retry bound — SKIP and continue
//   - (nil,  err): a genuine failure
func (e *ProductionExecutor) purchaseInputWithEmptyTrancheGuard(
	ctx context.Context,
	cmd *shipCargo.PurchaseCargoCommand,
) (*shipCargo.PurchaseCargoResponse, error) {
	logger := common.LoggerFromContext(ctx)
	var lastErr error
	for attempt := 0; attempt <= productionEmptyTrancheRetryLimit; attempt++ {
		// Honour container shutdown between attempts.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := e.purchaseWithDockRetry(ctx, cmd)
		if err == nil {
			return resp, nil
		}
		if !isEmptyTrancheError(err) {
			return nil, err // genuine failure — surface immediately, unretried
		}

		lastErr = err
		if attempt == productionEmptyTrancheRetryLimit {
			break
		}

		logger.Log("WARN", "Input buy hit an empty/zero-volume tranche; retrying in case supply refills", map[string]interface{}{
			"ship":    cmd.ShipSymbol,
			"good":    cmd.GoodSymbol,
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		e.clock.Sleep(productionEmptyTrancheRetryDelay)
	}

	// The tranche stayed empty across the bound: report a skip so the feeder survives
	// (a permanently-empty market must not crash the container or infinite-loop).
	logger.Log("WARN", "Input tranche still empty after bounded retries — skipping to keep the feeder alive", map[string]interface{}{
		"ship":    cmd.ShipSymbol,
		"good":    cmd.GoodSymbol,
		"retries": productionEmptyTrancheRetryLimit,
		"error":   lastErr.Error(),
	})
	return nil, nil
}

// redockFromAPI reconciles the ship against the server (SyncShipFromAPI) so a
// stale DOCKED cache entry cannot make EnsureDocked a no-op, then issues a real
// dock via a symbol-only command. Mirrors the reactive re-dock in
// NegotiateContractHandler.
func (e *ProductionExecutor) redockFromAPI(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) error {
	if _, err := e.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		return fmt.Errorf("failed to refresh ship %s from API: %w", shipSymbol, err)
	}
	if _, err := e.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}); err != nil {
		return fmt.Errorf("failed to dock ship %s: %w", shipSymbol, err)
	}
	return nil
}

// deliverInputs sells all cargo (inputs) at the current location
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0

	// Sell each cargo item
	for _, item := range ship.Cargo().Inventory {
		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}

		sellResp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			return 0, fmt.Errorf("failed to sell %s: %w", item.Symbol, err)
		}

		response, ok := sellResp.(*shipCargo.SellCargoResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from sell command")
		}

		totalRevenue += response.TotalRevenue

		logger.Log("INFO", fmt.Sprintf("Delivered input: %d units of %s (revenue: %d credits)", response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"input_good": item.Symbol,
			"units":      response.UnitsSold,
			"revenue":    response.TotalRevenue,
		})
	}

	return totalRevenue, nil
}

// freeCargoSpace sells whatever is currently in the ship's hold at its current
// docked market so a full hold does not block an input purchase (sp-mu6u).
// Unlike deliverInputs (which hard-fails on the first item this market won't
// buy), this is best-effort: an item this market doesn't import is skipped
// rather than aborting the whole attempt, since the goal here is only to make
// room, not to guarantee every item sells. Returns the reloaded ship
// reflecting whatever did sell.
//
// protectGood (sp-rqwm) is a good this make-room path must NEVER sell here — the
// fabricated OUTPUT. The output is sold ONLY at the guard's resale sink
// (SellFabricatedOutputAtSink); dumping it at the current (factory/buy) market to
// make room is exactly the −258k MEDICINE incident, so the harvest path passes the
// output good and it is skipped. A parked sink therefore holds the output onboard
// instead of the next cycle's make-room silently dumping it. Empty string protects
// nothing (the input-buy path, which never carries the terminal product here).
func (e *ProductionExecutor) freeCargoSpace(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	protectGood string,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	if ship.Cargo().IsEmpty() {
		return nil, fmt.Errorf("hold reports full but carries no inventory (capacity %d) — nothing to unload", ship.Cargo().Capacity)
	}

	sold := 0
	for _, item := range ship.Cargo().Inventory {
		// sp-rqwm: never dump the fabricated output at the current/buy market to make
		// room — it is sold only at the guard's resale sink. Skip it here.
		if protectGood != "" && item.Symbol == protectGood {
			logger.Log("INFO", fmt.Sprintf("Not unloading %d units of %s here to free space — the fabricated output is sold only at its resale sink, never dumped at the factory/buy market", item.Units, item.Symbol), map[string]interface{}{
				"good": item.Symbol,
				"ship": ship.ShipSymbol(),
			})
			continue
		}
		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}
		resp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Could not unload %s to free cargo space — market may not import it", item.Symbol), map[string]interface{}{
				"good":  item.Symbol,
				"ship":  ship.ShipSymbol(),
				"error": err.Error(),
			})
			continue
		}
		response, ok := resp.(*shipCargo.SellCargoResponse)
		if !ok {
			continue
		}
		sold += response.UnitsSold
		logger.Log("INFO", fmt.Sprintf("Unloaded %d units of %s to free cargo space", response.UnitsSold, item.Symbol), map[string]interface{}{
			"good":     item.Symbol,
			"quantity": response.UnitsSold,
			"revenue":  response.TotalRevenue,
		})
	}

	if sold == 0 {
		return nil, fmt.Errorf("market would not buy any of the %d onboard item(s)", len(ship.Cargo().Inventory))
	}

	reloaded, err := e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after unloading cargo: %w", err)
	}
	return reloaded, nil
}
