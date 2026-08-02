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
// fabricated-OUTPUT sale. The harvested product is sold at the resale
// sink the chain-margin guard priced only if that sink's live bid is at least the
// unit basis — the factory ask we paid to harvest — times this factor. 1.0 = strict
// breakeven: the output leg never realizes a loss. It is the last-line backstop to
// the chain-margin guard and the bp6f #3 crushed-sink harvest guard, checked
// at the actual point of the output sale where the sink bid may have decayed since
// production started (the −258k MEDICINE incident: guard cleared vs sink A1@5,248, the
// worker instead dumped the output at the factory's own ~1,560 bid). Below the floor
// the output is HELD (parked), never dumped. Tunable per ruling #5; kept at breakeven
// so a healthy sink is never over-restricted.
const minOutputSellMarginFactor = 1.0

// defaultWorkingCapitalReserve is the IMMUTABLE lower bound of the working-capital
// spend floor applied to factory INPUT purchases. It mirrors bp6f's
// trade-circuit floor (the identically-named const in run_trade_route_coordinator.go)
// and closes the same drain class one layer over — re-enabling 4 goods factories at
// ~848k drained the float to 23k in ~1min because bp6f guarded trade circuits but NOT
// factory input buys.
//
// sp-agzj unified this with the fleet's per-run working-capital reserve; sp-05glh then
// scrapped the proportional/per-run-override apparatus entirely (nothing ever stamped a
// non-default value in production) — the factory input floor is now simply this flat
// immutable constant, unconditionally (RULINGS #5). sp-q8bon raised it from the 50k base
// to the 150k non-contract floor: margin-blind gate-fill buys dragged the treasury
// 638k→142k and the contract engine — the sole earner — parked against ITS 50k floor, a
// full-economy deadlock; the 50k–150k band is now contract-exclusive.
const defaultWorkingCapitalReserve = common.NonContractWorkingCapitalFloor // sp-q8bon: the non-contract 150k floor (was the 50k base)

// effectiveReserveFloor resolves the working-capital floor to enforce at a factory input
// buy: the flat, immutable defaultWorkingCapitalReserve (the 150k non-contract floor), no
// proportional-of-treasury computation and no per-run override (sp-05glh scrapped both the
// sp-yqx4 counter-cyclical shrink and the dead sp-agzj ctx-override seam, which no
// production coordinator ever stamped — the factory's own working-capital config surface
// was retired with sp-hoj8u).
func effectiveReserveFloor(ctx context.Context) int {
	return defaultWorkingCapitalReserve
}

// budgetedReserveFloor is the floor a construction/factory buy is ACTUALLY guarded against:
// the flat effectiveReserveFloor RAISED by the share of deployable capital reserved
// for the trade side. Without that allocation the two non-contract engines draw on a shared pool
// with no split between them, and because construction carries no proportional cap while trade
// caps itself at 25% of treasury, construction consumes everything above its floor — including
// the whole band trade needs to function.
//
// It can only ever RAISE the floor (common.BudgetedSpendFloor is >= floor for every input), so
// this ADDS a constraint and weakens nothing (RULINGS #4), and it derives the deployable pool
// from THIS engine's own floor rather than a second constant (RULINGS #5).
//
// Three resolutions, deliberately different:
//
//   - No sensor wired -> the flat floor, unchanged. The optional-port contract for the package's
//     fixtures; the daemon always wires one.
//   - Sensor says trade is idle -> graceful degradation hands construction the WHOLE deployable
//     pool, and BudgetedSpendFloor returns exactly the flat floor. Capital never idles because
//     no trade hull is live.
//   - Sensor errors -> fail CONSERVATIVE, not open: assume trade IS working and take only the
//     proportional share. A blind read must never hand construction 100% of the treasury, which
//     is precisely the failure this floor exists to remove.
//
// Note that construction passes `true` for its OWN side unconditionally — an executor asking this
// question is an executor about to buy — so a sensor miss can never budget construction to zero
// and park the gate.
func (e *ProductionExecutor) budgetedReserveFloor(ctx context.Context, playerID, treasury int) int {
	floor := effectiveReserveFloor(ctx)
	if e.workSensor == nil {
		return floor
	}

	logger := common.LoggerFromContext(ctx)
	tradeHasWork := true
	if has, err := e.workSensor.TradeHasWork(ctx, playerID); err != nil {
		// Numbers/cause in the MESSAGE: the container log renderer drops the
		// metadata map, so a conservative resolution must name its cause in the text.
		logger.Log("WARNING", fmt.Sprintf("Could not sense whether trade is live for the capital budget — assuming it is and taking only construction's %d%% share (fail-conservative): %v", 100-common.TradeCapitalSharePct, err), map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		tradeHasWork = has
	}

	deployable := common.CapitalDeployable(int64(treasury), int64(floor))
	_, constructionBudget := common.CapitalSplit(common.TradeCapitalSharePct, deployable, tradeHasWork, true)
	return int(common.BudgetedSpendFloor(int64(floor), deployable, constructionBudget))
}

// defaultHullFillFraction is the fraction of a hauler's hold the construction-supply drain fills
// per trip when no fraction is configured. 1.0 = fill the whole hull — the fix's intent:
// a construction buy tops the hold up toward capacity instead of stopping at one trade-volume
// tranche (~1/4 hull), which quartered per-trip throughput. A 0/absent value resolves here at the
// point of use — a protective default that turns the FILL on, not money movement, so a default is
// correct (RULINGS #5); WithHullFillTarget's fraction parameter is the seam a per-run config sets
// to fill a smaller fraction.
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
// tranche. billRemaining caps the fill at the material's outstanding bill (never
// over-buying past demand); fraction (<=0 => full hull) parametrizes how much of the hold to fill
// (RULINGS #5). ONLY the construction drain stamps this; every other buyGood caller (goods-factory
// inputs) leaves it unset and keeps the single-tranche behavior exactly as before (RULINGS #2).
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

// liveAsk re-reads the current EXPORT ask (purchase_price — what WE PAY, sp-en5h7) of good at waypoint from the market DB — the
// per-iteration price the hull-fill loop re-checks its guards against, so a laddering market is not
// chased tranche-by-tranche. ok is false when the market/good is unreadable or the ask is
// non-positive; the loop treats that as fail-CLOSED (stop, deliver what is aboard), never buying at
// an unknown price (RULINGS #4).
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

// SpendReservationLedger is the cross-container concurrent factory-input spend cap
// (sp-w3he). The per-buy floor checks live treasury per container, but N factory
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

// TreasuryReader reports a player's credit balance for the factory money guards. The daemon
// satisfies it with the LEDGER-backed reader (sp-muq66), which serves the balance from the
// transaction ledger — no API call — and falls back to the coalesced live read only when the
// ledger is older than its freshness bound. `Get Agent` was 9-10% of the API ceiling and did
// not fall under request coalescing, because the reads are invalidation-driven rather than
// duplicated; the only way to remove them is to stop asking.
//
// An error means UNREADABLE, and both callers here PARK the input buy on it (RULINGS #4).
// The reader never converts a failed read into a stale or zero success.
type TreasuryReader interface {
	Credits(ctx context.Context, playerID int) (int64, error)
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
	// apiClient live-reads treasury for the working-capital spend floor.
	// nil disables the floor — the fail-OPEN contract for the package's test fixtures
	// that cannot supply a live client; the daemon always wires the real one (main.go).
	apiClient domainPorts.APIClient
	// spendLedger is the cross-container concurrent spend cap. nil disables it —
	// the same optional-port fail-OPEN contract as apiClient (tests pass nothing; the daemon
	// wires the real DB-backed ledger via SetSpendLedger). Injected by setter, not constructor,
	// so the package's existing test fixtures and the executor's many call sites stay untouched.
	spendLedger SpendReservationLedger
	// treasury is the LEDGER-backed treasury reader (sp-muq66) BOTH factory money guards —
	// the per-buy spend floor and the cross-container concurrent-spend cap — read through
	// instead of calling Get Agent before every input tranche. nil (every
	// existing fixture) leaves the direct apiClient read in place, byte-identical; the
	// daemon injects the shared reader via SetTreasuryReader at boot, with no config gate
	// between. Wired or not, an unreadable treasury still PARKS the buy (fail closed).
	treasury TreasuryReader
	// priceHistory backs the input price ceiling (sp-iv65): the trailing-median-ask source a
	// factory input buy is checked against before it dispatches. nil disables the ceiling — the
	// same optional-port fail-OPEN contract as apiClient/spendLedger (tests wire nothing; the
	// daemon wires the DB-backed price history repo via SetPriceHistoryReader).
	priceHistory InputPriceHistoryReader
	// constructionRepo backs the DeliverToConstructionSite terminal: the construction
	// supply API a sourced hauler delivers gate materials through. nil leaves the terminal
	// unavailable (returns an error if reached) — the optional-port contract; the daemon wires
	// the real API-backed repo via SetConstructionRepo, and only the construction-supply drain
	// ever calls the terminal, so every other caller is unaffected.
	constructionRepo manufacturing.ConstructionSiteRepository
	// workSensor backs the per-operation capital budget (sp-ftqgp): it answers whether the TRADE
	// side is live, which is what sizes construction's share of deployable capital. nil disables
	// the budget and leaves the flat reserve floor guarding alone — the SAME optional-port
	// fail-OPEN contract as apiClient/spendLedger/priceHistory (the package's fixtures wire
	// nothing; the daemon wires the container-backed sensor unconditionally via
	// SetCapitalWorkSensor, with no config gate between). A wired-but-erroring sensor does NOT
	// fail open: see budgetedReserveFloor.
	workSensor common.CapitalWorkSensor
}

// SetSpendLedger wires the cross-container concurrent spend cap. The daemon calls
// this after construction (main.go, via the coordinator handler); leaving it unset keeps the
// cap fail-open, which is exactly what every non-daemon caller wants.
func (e *ProductionExecutor) SetSpendLedger(ledger SpendReservationLedger) {
	e.spendLedger = ledger
}

// SetTreasuryReader wires the daemon's shared ledger-backed treasury reader into
// BOTH factory money guards. The daemon calls this UNCONDITIONALLY when it builds the
// construction executor — no config key, no default-off, no arming step. Leaving it unset is
// the test-fixture path only, and falls back to the direct live read.
func (e *ProductionExecutor) SetTreasuryReader(r TreasuryReader) {
	e.treasury = r
}

// treasuryCredits reads the player's balance for a factory money guard: through the
// ledger-backed reader when the daemon has wired one, otherwise the direct live call this
// guard has always made. The direct path is the optional-port contract the package's
// fixtures rely on, not an arming switch.
//
// Every failure is an ERROR — never a zero, never a retained value. Both callers read that
// as "PARK this input buy" (RULINGS #4).
func (e *ProductionExecutor) treasuryCredits(ctx context.Context, playerID int) (int64, error) {
	if e.treasury != nil {
		return e.treasury.Credits(ctx, playerID)
	}
	if e.apiClient == nil {
		return 0, fmt.Errorf("no treasury source wired")
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unavailable: %w", err)
	}
	agentData, err := e.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("live agent read failed: %w", err)
	}
	return int64(agentData.Credits), nil
}

// SetCapitalWorkSensor wires the per-operation capital budget's hasWork sensor. The
// daemon calls this UNCONDITIONALLY when it builds the construction executor — there is no config
// key, no default-off and no arming step between the sensor and a live budget. Leaving it unset
// is the test-fixture path only, and keeps the flat reserve floor as the sole guard.
func (e *ProductionExecutor) SetCapitalWorkSensor(sensor common.CapitalWorkSensor) {
	e.workSensor = sensor
}

// SetConstructionRepo wires the construction supply API the DeliverToConstructionSite terminal
// delivers gate materials through. The daemon calls this when it builds the executor
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
	// Defense in depth (sp-vh1s): PollForProduction is clock-driven (e.clock.Now()). The construction
	// daemon builds this executor directly with a nil clock (main.go), unlike the factory path which
	// defaults nil→RealClock upstream in NewRunFactoryCoordinatorHandler. Default it here so NO
	// construction path can nil-panic on the first e.clock.Now(). The default applies ONLY when nil,
	// so an injected mock clock is always honored. Mirrors the same nil-guard precedent in
	// shared.NewLifecycleStateMachine and NewRunConstructionCoordinatorHandler.
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

	// Tag this input buy with the selector branch that chose the source: it rides ctx
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
		// We're already docked at this market, so try to sell whatever is onboard to
		// free space before giving up — a factory that didn't unload its last
		// output before buying the next input should recover, not die.
		//
		// No protectGood here. This is an INPUT market (where a feed is
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

	// A zero-volume market cannot be bought from (preserves the prior "trade volume is zero"
	// error for both the single-tranche and hull-fill paths).
	if marketResult.TradeVolume <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", node.Good)
	}

	// Per-trip fill target. A construction-supply buy tops the hold up TOWARD hull
	// capacity so a full round-trip carries ~a hull, not one ~trade-volume tranche (~1/4 hull,
	// which quadrupled the round-trips). A single SpaceTraders market buy is itself capped at
	// trade_volume, so filling the hold needs a LOOP of tranches. Every OTHER caller (goods-factory
	// inputs) stamps NO fill target and keeps the original single-tranche behavior — buy ONE input
	// so the hold has room for the factory's other inputs (RULINGS #2).
	capacity := updatedShip.Cargo().Capacity
	onboard := updatedShip.Cargo().Units
	billRemaining, fraction, fillMode := HullFillTargetFromContext(ctx)

	// tripTarget = the max units of node.Good to acquire THIS trip.
	tripTarget := marketResult.TradeVolume // single-tranche default (factory input path)
	loopFill := fillMode                   // buy multiple tranches up to tripTarget (vs exactly one)
	if fillMode {
		if fraction <= 0 {
			fraction = defaultHullFillFraction
		}
		tripTarget = int(float64(capacity) * fraction)
		if billRemaining > 0 && billRemaining < tripTarget {
			tripTarget = billRemaining // never over-buy past the outstanding bill
		}
	}

	// sp-to2v — balanced+saturation FEED CAP. When the fabrication-efficiency feeding planner has
	// sized this input's delivery to the limiting (scarcest) input's flow — capped at the saturation
	// window — it stamps that cap on ctx. It only ever LOWERS the target (an ample input is pulled
	// down toward the scarce one's flow, never past saturation), and it turns the multi-tranche loop
	// ON so a small market trade volume still accumulates up to the balanced tranche rather than
	// stopping at one ~tv slice. Every non-feeding caller (the OFF fleet, every pre-bead test) leaves
	// it unstamped → loopFill/tripTarget are byte-identical to today.
	if feedCap, capped := inputFeedCapFromContext(ctx); capped {
		if feedCap < tripTarget {
			tripTarget = feedCap
		}
		loopFill = true
	}

	acquired := 0
	totalCost := 0
	for {
		availableSpace := capacity - onboard
		if availableSpace <= 0 {
			break // STOP: hold full
		}
		want := tripTarget - acquired
		if want <= 0 {
			break // STOP: fill target / remaining bill met (single-tranche: one buy already done)
		}
		trancheQty := min(availableSpace, marketResult.TradeVolume, want)
		if trancheQty <= 0 {
			break
		}

		// Per-tranche price. The single-tranche path keeps the selection-time snapshot (behavior
		// preserved). The hull-fill loop RE-READS the live ask each iteration and re-checks the
		// cross-market price ceiling, so a market laddering under our own draw is not chased
		// tranche-by-tranche. An unreadable live ask fails CLOSED — stop the loop and
		// deliver what is aboard, never buy blind (RULINGS #4).
		ask := marketResult.Price
		if loopFill {
			liveAsk, ok := e.liveAsk(ctx, marketResult.WaypointSymbol, node.Good, playerID)
			if !ok {
				logger.Log("WARNING", fmt.Sprintf("Stopping hull-fill of %s at %s after %d units — could not re-read the live ask for the per-tranche price check (fail-closed); delivering what is aboard", node.Good, marketResult.WaypointSymbol, acquired), map[string]interface{}{
					"good": node.Good, "market": marketResult.WaypointSymbol, "acquired": acquired,
					"action": "hull_fill_stopped", "reason": "live_ask_unreadable",
				})
				break
			}
			ask = liveAsk
			if mode == sourceModeEligible && e.inputPriceCeilingParked(ctx, marketResult.WaypointSymbol, node.Good, systemSymbol, playerID, ask) {
				break // STOP: ask laddered above the ceiling — deliver what is aboard, park the rest
			}
		}

		// Working-capital spend floor — re-checked EACH iteration against live treasury,
		// fail-CLOSED under the loop (RULINGS #4): once the NEXT tranche would drop treasury below
		// the reserve the loop stops and delivers what is aboard, never forcing the buy. This is the
		// per-buy backstop to bp6f's circuit-level floor; chain_margin_guard sits UPSTREAM
		// at the coordinator. The park cause goes IN THE MESSAGE — the container log
		// renderer drops the metadata map.
		projectedCost := trancheQty * ask
		if breached, enforcedFloor := e.spendFloorBreached(ctx, playerID, projectedCost); breached {
			if acquired == 0 {
				logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — would breach working-capital reserve (projected cost %d, reserve %d)", node.Good, marketResult.WaypointSymbol, projectedCost, enforcedFloor), map[string]interface{}{
					"good": node.Good, "market": marketResult.WaypointSymbol, "projected_cost": projectedCost,
					"action": "factory_parked", "reason": "spend_floor",
				})
			} else {
				logger.Log("WARNING", fmt.Sprintf("Stopping purchase of %s at %s after %d units — next tranche would breach the working-capital reserve (projected cost %d, reserve %d)", node.Good, marketResult.WaypointSymbol, acquired, projectedCost, enforcedFloor), map[string]interface{}{
					"good": node.Good, "market": marketResult.WaypointSymbol, "projected_cost": projectedCost, "acquired": acquired,
					"action": "factory_parked", "reason": "spend_floor",
				})
			}
			break
		}

		// Cross-container concurrent spend cap: the floor above is a PER-CONTAINER live
		// check, so N containers can each clear it inside their own check->buy window and
		// collectively breach the reserve. This HARD cap serializes all containers' in-flight input
		// spend through a shared DB ledger and PARKS if the combined exposure would breach. Also
		// re-checked per iteration and fail-CLOSED. Reserved per tranche and released after it.
		reservationID, parked := e.reserveConcurrentSpendOrPark(ctx, playerID, projectedCost, marketResult.WaypointSymbol, node.Good)
		if parked {
			break // STOP: concurrent cap (reserveConcurrentSpendOrPark logged the cause)
		}

		logger.Log("INFO", fmt.Sprintf("Purchasing %d units of %s (cargo space: %d, trade_volume: %d, acquired so far: %d)", trancheQty, node.Good, availableSpace, marketResult.TradeVolume, acquired), nil)

		purchaseCmd := &shipCargo.PurchaseCargoCommand{
			ShipSymbol: updatedShip.ShipSymbol(),
			GoodSymbol: node.Good,
			Units:      trancheQty,
			PlayerID:   playerIDValue,
		}

		// Dispatch through the empty-tranche guard: a transient "must be docked" is still re-docked
		// and retried inside; an empty / zero-volume tranche ("partial failure: ... 0
		// units processed" / API 400) is bounded-retried then reported as a skip so the feeder
		// survives instead of crashing the container (sp-q02m crash #4).
		response, err := e.purchaseInputWithEmptyTrancheGuard(ctx, purchaseCmd)
		if err != nil {
			e.releaseSpendReservation(ctx, reservationID)
			return nil, fmt.Errorf("failed to purchase cargo: %w", err)
		}
		if response == nil {
			// Empty tranche persisted across the retry bound — the market is drained. STOP the fill
			// and deliver whatever is aboard (nothing yet if this was the first tranche).
			e.releaseSpendReservation(ctx, reservationID)
			if acquired == 0 {
				logger.Log("WARN", fmt.Sprintf("Skipped empty tranche for %s at %s — market sold 0 units; feeder continues", node.Good, marketResult.WaypointSymbol), map[string]interface{}{
					"good": node.Good, "market": marketResult.WaypointSymbol,
				})
			} else {
				logger.Log("INFO", fmt.Sprintf("Stopping hull-fill of %s at %s after %d units — market stock exhausted; delivering what is aboard", node.Good, marketResult.WaypointSymbol, acquired), map[string]interface{}{
					"good": node.Good, "market": marketResult.WaypointSymbol, "acquired": acquired,
				})
			}
			break
		}
		e.releaseSpendReservation(ctx, reservationID)

		acquired += response.UnitsAdded
		onboard += response.UnitsAdded
		totalCost += response.TotalCost
		logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, node.Good, response.TotalCost), map[string]interface{}{
			"good":       node.Good,
			"quantity":   response.UnitsAdded,
			"total_cost": response.TotalCost,
			"market":     marketResult.WaypointSymbol,
		})

		if !loopFill {
			break // single-tranche (goods-factory input) path: exactly one buy, unchanged
		}
		if response.UnitsAdded <= 0 {
			break // safety: no forward progress (never spin)
		}
	}

	return &ProductionResult{
		QuantityAcquired: acquired,
		TotalCost:        totalCost,
		WaypointSymbol:   marketResult.WaypointSymbol,
	}, nil
}

// spendFloorBreached reports whether buying an input tranche costing projectedCost would drop
// treasury below the floor this buy is guarded against, and returns that floor so the caller
// can name the ACTUAL enforced number in its park log rather than re-deriving a base that the
// capital budget may have raised. It mirrors bp6f's trade floor (spendFloorBreached in
// run_trade_route_coordinator.go): a pre-commit treasury read checked right before the buy
// commits, so the caller can PARK instead of spending.
//
// That mirror is why the read is now the ledger-backed one: bp6f's floor was routed
// through the shared reader in sp-muq66, and this guard exists to enforce the SAME line the
// same way. It stays a genuine pre-commit read — the freshness bound is what makes the ledger
// answer admissible, and past it the coalesced live call still answers.
//
// The floor is budgetedReserveFloor (sp-ftqgp): the flat non-contract reserve, raised by the share
// of deployable capital reserved for the trade side. Layering the budget HERE covers both spend
// paths at once — raw input buys (buyGood) and fabricated-output harvests both funnel through
// this one primitive — with no second guard to drift.
//
// Fails OPEN when NO treasury source is wired at all (neither the ledger-backed reader nor
// an apiClient): the guard is simply unavailable — the optional-port contract the package's
// test fixtures rely on (they pass nil), never the daemon, which always wires both.
//
// Fails CLOSED on every treasury-read failure (an unresolvable player token, GetAgent itself
// erroring, or a ledger too stale to trust with the live fallback also down): a guard whose
// whole job is keeping treasury above the reserve must never let a buy through just because
// it went blind. A hiccup here parks the input rather than spending unseen — the factory-side
// analogue of bp6f's fail-closed.
func (e *ProductionExecutor) spendFloorBreached(ctx context.Context, playerID, projectedCost int) (bool, int) {
	logger := common.LoggerFromContext(ctx)
	if e.apiClient == nil && e.treasury == nil {
		return false, effectiveReserveFloor(ctx)
	}

	credits, err := e.treasuryCredits(ctx, playerID)
	if err != nil {
		// Numbers/cause in the MESSAGE: the container log renderer drops the
		// metadata map, so a blind fail-closed park must name its cause in the text.
		logger.Log("WARNING", fmt.Sprintf("Could not read treasury for factory spend-floor check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return true, effectiveReserveFloor(ctx)
	}
	treasury := int(credits)

	// The flat non-contract floor (sp-05glh/sp-q8bon), RAISED by trade's reserved share of
	// deployable capital (sp-ftqgp). Resolved against the SAME treasury figure the breach test
	// uses, so the budget and the check can never disagree about the balance they are sizing.
	reserve := e.budgetedReserveFloor(ctx, playerID, treasury)

	if treasury-projectedCost < reserve {
		logger.Log("WARNING", fmt.Sprintf("Factory input buy would breach the working-capital reserve — treasury %d, projected cost %d, reserve %d", treasury, projectedCost, reserve), map[string]interface{}{
			"treasury": treasury, "projected_cost": projectedCost, "reserve": reserve,
		})
		return true, reserve
	}

	return false, reserve
}

// reserveConcurrentSpendOrPark records this input buy's spend intent in the shared ledger
// and reports whether it must PARK because the COMBINED in-flight factory spend would
// breach the reserve. On the proceed path it returns the reservation id the
// caller must Release after the buy.
//
// Fails OPEN when the cap is unavailable (no ledger wired, or no treasury source at all to
// read the balance from) — the same optional-port contract as the per-buy floor, so every
// non-daemon caller is unaffected. Fails CLOSED (parks) on any treasury-read or ledger error:
// a cap whose job is protecting the reserve must never let a buy through blind.
//
// The treasury read here is deliberately independent of spendFloorBreached (sp-9aoc, left
// unchanged): factory input buys are low-frequency — one per market visit, after a
// multi-second navigate+dock — so the second read is negligible next to keeping the two
// guards decoupled, each with its own legible park reason. It is now the ledger-backed read
// (sp-45s6f), which makes "negligible" literal on the common path: no API call at all.
//
// WHY A LEDGER BALANCE IS SOUND FOR THE CONCURRENT CAP. The cap subtracts in-flight
// RESERVATIONS from a base balance, precisely because a balance read cannot see spend that
// has not committed yet. Committed spend is in the ledger (every transaction records its
// post-transaction balance); uncommitted spend is in the reservation ledger. Nothing falls
// between the two: the cargo transaction handler records the transaction SYNCHRONOUSLY, in
// the same call, before the buy returns — and the reservation is only released after that
// call returns — so there is no window in which a spend is absent from both.
//
// The read stays OUTSIDE the ledger transaction (passed in as a value) so the DB is never
// held open across it.
func (e *ProductionExecutor) reserveConcurrentSpendOrPark(ctx context.Context, playerID, projectedCost int, market, good string) (reservationID string, parked bool) {
	logger := common.LoggerFromContext(ctx)
	if e.spendLedger == nil || (e.apiClient == nil && e.treasury == nil) {
		return "", false
	}

	credits, err := e.treasuryCredits(ctx, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Could not read treasury for factory concurrent-spend-cap check — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}
	treasury := int(credits)

	// Same floor as the per-buy check: the concurrent cap must serialize against the SAME
	// reserve the per-container floor enforces, or the two guards would disagree on where the
	// line is (sp-agzj/sp-05glh). That reserve is now the BUDGETED floor (sp-ftqgp) — the flat
	// non-contract base raised by trade's reserved share — resolved against this call's own
	// treasury read, so a construction buy cannot slip past the budget by taking the ledger path.
	reserve := e.budgetedReserveFloor(ctx, playerID, treasury)

	// Container id attributes the reservation to the owning factory (already threaded into
	// ctx by the coordinator, sp-9aoc's operation context). Best-effort: the staleness sweep
	// is time-based, so a missing id never affects correctness, only log/debug attribution.
	containerID := "factory-unknown"
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.ContainerID != "" {
		containerID = opCtx.ContainerID
	}

	resID, ok, err := e.spendLedger.Reserve(ctx, playerID, containerID, projectedCost, treasury, reserve)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Factory concurrent-spend-cap ledger error — parking input buy (fail-closed): %v", err), map[string]interface{}{
			"error": err.Error(),
		})
		return "", true
	}
	if !ok {
		// Numbers in the MESSAGE: the container log renderer drops the metadata map,
		// so the cause — combined in-flight factory spend breaching the reserve — must be legible
		// in the text or an operator never sees why this factory parked.
		logger.Log("WARNING", fmt.Sprintf("Parked input purchase of %s at %s — cross-container concurrent spend cap: treasury %d minus in-flight factory reservations would breach the working-capital reserve %d (this buy %d)", good, market, treasury, reserve, projectedCost), map[string]interface{}{
			"good":           good,
			"market":         market,
			"projected_cost": projectedCost,
			"treasury":       treasury,
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

	// sp-to2v — FABRICATION EFFICIENCY feeding policy. Two decisions layer in here when the policy is
	// engaged; unstamped/OFF the greedy byte-identical loop below runs unchanged.
	//   #4b FEED-RESPONSIVE-ONLY: if this node's output good does not respond to feeding
	//       (EQUIPMENT/LAB_INSTRUMENTS/FOOD/MEDICINE — verified), hauling inputs to feed it is wasted
	//       hull-hours, so BUY-OR-SKIP. Step 0 already bought it if the factory was abundantly stocked;
	//       reaching here means not-stocked, so skip (zero-spend) rather than feed a non-responder.
	//   #2/#3/#4a BALANCED-TO-LIMITING, SATURATION-CAPPED, TAPROOT-FIRST: order the inputs scarcest
	//       (taproot) first and cap each delivery at the balanced tranche sized to the limiting input,
	//       so an ample input is not greedily piled on while the scarce one starves (the ~4x lever).
	feedCfg := defaultFeedingPolicy()
	if len(node.Children) > 0 && !feedCfg.isFeedResponsive(node.Good) {
		logger.Log("INFO", fmt.Sprintf("Feed-responsive gate: %s does not respond to feeding — buy-or-skip, not hauling inputs (sp-to2v)", node.Good), map[string]interface{}{
			"good": node.Good, "factory": factoryMarket.WaypointSymbol,
			"action": "feed_skipped", "reason": "non_responsive_good",
		})
		return &ProductionResult{QuantityAcquired: 0, TotalCost: 0, WaypointSymbol: factoryMarket.WaypointSymbol}, nil
	}

	// Step 1: Recursively produce all required inputs
	logger.Log("INFO", fmt.Sprintf("Starting fabrication of %s (requires %d inputs)", node.Good, len(node.Children)), map[string]interface{}{
		"good":        node.Good,
		"input_count": len(node.Children),
	})

	// Taproot-first order + the balanced+saturation per-input feed cap (sp-sxyx6: unconditional — the
	// balanced feeding policy is the executor's sole feeding path). A childless leaf keeps the declared
	// order with no cap (childCtx == ctx).
	childOrder := node.Children
	feedCap := 0
	if len(node.Children) > 0 {
		childOrder, feedCap = e.planBalancedFeed(ctx, node, systemSymbol, playerID, feedCfg)
	}

	for _, child := range childOrder {
		// Children are inputs that must be harvested and delivered to THIS factory —
		// inputs-only never suppresses their acquisition, so force it off here.
		childCtx := ctx
		if feedCap > 0 {
			// A FRESH per-child stamp: the cap applies to THIS child's sourcing/harvest only; the
			// child's own feeding planner re-stamps a fresh cap for its subtree, so a parent's cap
			// never leaks onto a grandchild (sp-to2v).
			childCtx = WithInputFeedCap(ctx, feedCap)
		}
		result, err := e.ProduceGood(childCtx, ship, child, systemSymbol, playerID, opContext, false)
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
	// The factory EXPORTS the finished good (a low ASK) and IMPORTS the inputs.

	logger.Log("INFO", fmt.Sprintf("Found factory (export market) for %s at %s", node.Good, factoryMarket.WaypointSymbol), map[string]interface{}{
		"good":     node.Good,
		"waypoint": factoryMarket.WaypointSymbol,
		"ask":      factoryMarket.Price, // the factory's ASK — what we pay to harvest (sp-en5h7)
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
	// harvest is skipped so the good is left in factory stock.
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
				"ask":           tradeGood.PurchasePrice(),
			})

			// Construction-support (inputs-only) mode: production is confirmed and the
			// output now sits in the factory's export stock. Do NOT harvest it — leave
			// it for the construction pipeline to be the sole buyer. Harvesting here is
			// exactly what starved the era-2 gate fill: the factory bought back its own
			// 149 FAB_MATS and froze the fill at 898/1600 for ~6h.
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
			// against what we're about to pay to harvest (tradeGood.PurchasePrice, the
			// factory's own ask). Fail OPEN (harvest anyway) if no sink can be found at
			// all - that's normal for goods with no direct resale market, not a signal
			// to stop production.
			//
			// sp-qmp8 / sp-vh1s: a construction-supply OR unified gate-fill harvest delivers its
			// output to the jump-gate site, NOT to a resale sink, so this resale-margin guard does
			// not apply — skip straight to the harvest. The construction pipeline's own economics
			// govern the gate fill (the Admiral's primary objective — gate nodes are MARGIN-BLIND),
			// and the INPUT buys already passed the full money-guard stack (RULINGS #4). Without this
			// a market that happens to import the gate material at a crushed bid would wrongly park
			// the gate fill.
			if !shared.ConstructionSupplyFromContext(ctx) && !IsUnifiedGateNode(ctx) {
				if sink, sinkErr := e.marketLocator.FindImportMarket(ctx, good, systemSymbol, playerID.Value()); sinkErr == nil && sink != nil {
					harvestCost := tradeGood.PurchasePrice()
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

			return e.purchaseFabricatedOutput(ctx, good, waypointSymbol, shipSymbol, playerID, tradeGood.TradeVolume(), tradeGood.PurchasePrice())
		}

		// Log polling attempt. Past productionDwellWarnThreshold, escalate to a
		// WARNING on EVERY attempt with the elapsed dwell stated in the message
		// text itself (the container-log renderer prints only level+message and
		// drops metadata, sp-iqyq) so a long fabrication wait is observable
		// rather than reading as a silent stall.
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
	unitPrice int, // per-unit harvest cost (the factory's ask = tradeGood.PurchasePrice()) — the working-capital-floor basis
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to reload ship: %w", err)
	}

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	if availableSpace <= 0 {
		// We're already docked at this market to harvest, so try to sell
		// whatever is onboard to free space first. Unlike a skipped INPUT
		// purchase, a skipped output harvest loses nothing: the fabricated good
		// stays in the factory's export stock and is picked up on a later pass,
		// so skip gracefully rather than die.
		//
		// Protect `good` — the fabricated output. We are docked at the
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

	// The output-buy drains to what the market has to sell: min(cargo space, trade volume). A gate
	// fill and a profit-factory harvest share this cap — the natural "buy only what's exported" limit
	// paces the drain; the solvency floor is the money bound.
	perTrancheCap := tradeVolume

	// sp-to2v: a fabricated INPUT harvested to FEED a parent factory is capped to the balanced tranche
	// the parent's feeding planner sized — the same limiting-input balance a raw input buy honors, so
	// an ample intermediate is not over-harvested past the scarce sibling's flow. The ROOT output (no
	// parent stamps a feed cap) is never feed-capped. Additive with the per-tranche cap above:
	// both are upper bounds, the lesser binds.
	if feedCap, capped := inputFeedCapFromContext(ctx); capped && feedCap < perTrancheCap {
		perTrancheCap = feedCap
	}

	purchaseQty := min(availableSpace, perTrancheCap)
	if purchaseQty <= 0 {
		return 0, 0, fmt.Errorf("trade volume is zero for %s", good)
	}

	// Working-capital spend floor (floor-everywhere) — the fabricated-OUTPUT buy is floored
	// IDENTICALLY to the raw-input buyGood path, reusing the SAME primitive: no new floor,
	// no new knob (RULINGS #4/#5). Closes the gap where the two LARGEST gate purchases (FAB_MATS,
	// ADVANCED_CIRCUITRY) executed bounded only by a units cap. Fail-CLOSED: a breach — or a blind
	// live-treasury read inside spendFloorBreached — PARKS the harvest rather than spending below the
	// reserve. Parking loses nothing: the fabricated good stays in the factory's export stock and is
	// picked up on a later pass (same as the full-hold skip above). The park cause goes IN THE MESSAGE
	// (sp-iqyq) — the container-log renderer drops the metadata map.
	projectedCost := purchaseQty * unitPrice
	if breached, enforcedFloor := e.spendFloorBreached(ctx, playerID.Value(), projectedCost); breached {
		logger.Log("WARNING", fmt.Sprintf("Parked fabricated-output harvest of %s at %s — would breach the working-capital reserve (projected cost %d, reserve %d)", good, waypointSymbol, projectedCost, enforcedFloor), map[string]interface{}{
			"good": good, "market": waypointSymbol, "projected_cost": projectedCost,
			"action": "factory_parked", "reason": "spend_floor",
		})
		return 0, 0, nil
	}

	// Cross-container concurrent spend cap: the floor above is a PER-CONTAINER live check, so
	// N factories can each clear it inside their own check->buy window and collectively breach. This
	// HARD cap serializes all in-flight factory spend — input AND output — through the shared ledger and
	// PARKS if the combined exposure would breach. Reserved before the buy, released after (fail-CLOSED,
	// same as buyGood). No-op when no ledger/apiClient is wired (the optional-port fail-open contract).
	reservationID, parked := e.reserveConcurrentSpendOrPark(ctx, playerID.Value(), projectedCost, waypointSymbol, good)
	if parked {
		return 0, 0, nil // concurrent cap (reserveConcurrentSpendOrPark logged the cause)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of fabricated %s (cargo: %d, trade_volume: %d)", purchaseQty, good, availableSpace, tradeVolume), nil)

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      purchaseQty,
		PlayerID:   playerID,
	}

	// Same dock-retry guard as the raw-buy path: a transient "must be docked"
	// re-docks and retries rather than crashing the container.
	response, err := e.purchaseWithDockRetry(ctx, purchaseCmd)
	// Release the concurrent-spend reservation on BOTH paths (success and error), mirroring buyGood
	// (a no-op when no reservation was taken — the fail-open contract).
	e.releaseSpendReservation(ctx, reservationID)
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
// chain-margin guard priced — NEVER the factory/buy market — closing the
// guard-vs-execution divergence that bled −258k on MEDICINE: the guard
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
// site accepted. It is the delivery TERMINAL of the construction-supply drain: the drain
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

	// sp-v5d1/sp-j09q — post-supply cargo WRITE-BACK (the root fix). The server just removed
	// result.UnitsDelivered of good from this hull, but the daemon's cached ship row still holds the
	// pre-delivery cargo. Write the decrement back through the single-writer CAS path (RULINGS #3 — the
	// same SaveWithRetry seam sp-wa7c routes ship writes through) so the NEXT drain tick reads the REAL
	// emptied hold, not PHANTOM cargo it would re-route to re-deliver (→ API 4219 → the sp-6zkg drain
	// hang). Mirrors transfer_cargo.go's post-transfer RemoveCargo write-back.
	//
	// Idempotent under CAS re-apply: the mutation removes only what the FRESH row still holds — a
	// concurrent writer or a `ship refresh` that already reconciled the hull leaves nothing to remove
	// (changed=false, no spurious version bump), so it can never error on an already-reconciled row.
	// Best-effort: the supply already committed server-side, so a write-back failure is logged, never
	// fatal — the cache reconciles on the next sync.
	if delivered := result.UnitsDelivered; delivered > 0 {
		if _, _, wbErr := e.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				cargo := sh.Cargo()
				if cargo == nil {
					return false, nil
				}
				have := cargo.GetItemUnits(good)
				if have <= 0 {
					return false, nil // already reconciled — no phantom to clear
				}
				remove := delivered
				if remove > have {
					remove = have // fresh row holds fewer than we delivered; strip only what is actually there
				}
				if err := sh.RemoveCargo(good, remove); err != nil {
					return false, err
				}
				return true, nil
			}); wbErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Post-supply cargo write-back failed for %s (%d %s) — cache may briefly show phantom until next sync: %v", shipSymbol, delivered, good, wbErr), map[string]interface{}{
				"ship": shipSymbol, "good": good, "units_delivered": delivered,
			})
		}
	}
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
// dock.
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

// deliverInputs sells the hauled inputs to the factory the ship is docked at.
//
// Only goods this market actually takes are offered (marketBuys), and every good is
// independent: a refused sell is held aboard and the rest of the hold still delivers,
// and the revenue of whatever did sell is returned in full. Aborting the whole
// sourcing step on the first rejection poisons the hold — nothing reconciles a hold
// between lots, so the hull is disabled permanently.
//
// Nothing sellable is a no-op, not an error: it is indistinguishable from docking with
// an empty hold, which this step has always accepted, and the production poll
// downstream carries its own bounds.
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0
	deliveredGoods := 0

	// The market the sell will actually transact against is the one the hull is docked
	// at, so the eligibility read is anchored to the ship's own location — the same
	// waypoint the cargo handler resolves its trade volume from.
	waypointSymbol := ship.CurrentLocation().Symbol
	var listings *market.Market
	if data, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID.Value()); err == nil {
		listings = data
	}

	for _, item := range ship.Cargo().Inventory {
		if !marketBuys(listings, item.Symbol) {
			logger.Log("INFO", fmt.Sprintf("Holding %d units of %s aboard at %s — this market does not buy it", item.Units, item.Symbol, waypointSymbol), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol,
			})
			continue
		}

		sellCmd := &shipCargo.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}

		sellResp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			// Cause in the MESSAGE: the container-log renderer drops metadata.
			logger.Log("WARNING", fmt.Sprintf("Could not deliver %d units of %s at %s — held aboard, delivering the rest of the hold: %v", item.Units, item.Symbol, waypointSymbol, err), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol, "error": err.Error(),
			})
			continue
		}

		response, ok := sellResp.(*shipCargo.SellCargoResponse)
		if !ok {
			logger.Log("WARNING", fmt.Sprintf("Unexpected response type delivering %s at %s — held aboard", item.Symbol, waypointSymbol), map[string]interface{}{
				"good": item.Symbol, "ship": ship.ShipSymbol(), "waypoint": waypointSymbol,
			})
			continue
		}

		totalRevenue += response.TotalRevenue
		deliveredGoods++

		logger.Log("INFO", fmt.Sprintf("Delivered input: %d units of %s (revenue: %d credits)", response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"input_good": item.Symbol,
			"units":      response.UnitsSold,
			"revenue":    response.TotalRevenue,
		})
	}

	if deliveredGoods == 0 && !ship.Cargo().IsEmpty() {
		logger.Log("WARNING", fmt.Sprintf("Delivered nothing at %s: none of the %d onboard good(s) are bought here — the hold rides on and production runs on factory stock", waypointSymbol, len(ship.Cargo().Inventory)), map[string]interface{}{
			"ship": ship.ShipSymbol(), "waypoint": waypointSymbol, "onboard_goods": len(ship.Cargo().Inventory),
		})
	}

	return totalRevenue, nil
}

// marketBuys reports whether the market described by listings will take good off a
// hull: it must be listed there, as an IMPORT (a consumer) or an EXCHANGE (a trader) —
// the same sell-destination eligibility SellMarketDistributor applies. An EXPORT
// listing is the market's own product, and selling into it ladders its own bid down;
// that is the resale-sink divergence SellFabricatedOutputAtSink exists to prevent, so
// a factory's own output is never dumped back at the factory.
//
// Unreadable listings answer true: with nothing to read there is no basis to withhold
// a delivery, a sell spends nothing, and the caller tolerates the refusal if the market
// does reject it. Withholding on a stale row would stall a fabrication over a data gap.
func marketBuys(listings *market.Market, good string) bool {
	if listings == nil {
		return true
	}
	tradeGood := listings.FindGood(good)
	if tradeGood == nil {
		return false
	}
	return tradeGood.TradeType() != market.TradeTypeExport
}

// freeCargoSpace sells whatever is currently in the ship's hold at its current
// docked market so a full hold does not block an input purchase.
// Best-effort like deliverInputs: an item this market doesn't import is skipped
// rather than aborting the whole attempt, since the goal here is only to make
// room, not to guarantee every item sells. It offers the hold unfiltered — a
// rejection costs one call and the goal is space at any price — where
// deliverInputs pre-filters on the listing to avoid dumping into an export bid.
// Returns the reloaded ship reflecting whatever did sell.
//
// protectGood is a good this make-room path must NEVER sell here — the
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
		// Never dump the fabricated output at the current/buy market to make
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
