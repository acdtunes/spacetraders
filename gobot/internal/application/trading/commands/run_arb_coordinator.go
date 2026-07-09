package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunArbCoordinatorCommand is a ONE-SHOT, captain-directed, guarded arbitrage run
// (sp-p4ua): fly THIS hull on THIS lane, ONCE, safely. It is the deliberate middle
// between hand-flying an arb leg and the autonomous trade-route circuit — the captain
// names the good, the source, and the destination, caps the exposure, sets a per-unit
// margin floor, and the coordinator buys once at the source, routes (cross-gate if
// needed) to the destination, sells, and stops. There is NO loop and NO lane
// auto-selection: unlike RunTradeRouteCoordinatorCommand it never ranks or re-visits.
//
// The guards are the whole point — every one fails CLOSED (refuses the buy) rather
// than risk the 11M→43k drain the unguarded path caused:
//   - location: the hull must actually be at BuyAt before anything is bought;
//   - min-margin: the live source ask vs the destination bid must clear MinMargin;
//   - caps: the tranche is bounded by MaxUnits, hold space, and MaxSpend;
//   - spend-floor: the buy must not drop live treasury below WorkingCapitalReserve.
type RunArbCoordinatorCommand struct {
	ShipSymbol  string
	Good        string
	BuyAt       string // source waypoint (the hull must already be here)
	SellAt      string // destination waypoint (may be in another system → gate+jump+refuel)
	MaxUnits    int    // hold-cap on the tranche; 0 → the hull's full available cargo space
	MaxSpend    int    // working-capital cap on the buy; 0 → no explicit spend cap (floor still applies)
	MinMargin   int    // per-unit floor: abort the buy if (destBid − sourceAsk) < this; 0 → only reject a non-positive margin
	PlayerID    int
	ContainerID string
	// WorkingCapitalReserve is the hard spend floor (mirrors sp-bp6f): the buy must
	// not drop live treasury below this line. 0 → defaultWorkingCapitalReserve.
	WorkingCapitalReserve int
}

// RunArbCoordinatorResponse reports the realised one-shot economics and, when the run
// bought nothing, exactly which guard refused it — so a no-trade result is never a
// silent zero. It distinguishes three terminal shapes: a completed trade
// (Completed, UnitsTraded>0), a guarded refusal (Aborted with the matching *Abort
// flag + AbortReason, nil error), and an operational failure mid-run (a non-nil
// error from Handle with AbortReason naming the failed leg).
type RunArbCoordinatorResponse struct {
	ShipSymbol     string
	Good           string
	SourceWaypoint string
	DestWaypoint   string
	UnitsTraded    int
	TotalCost      int
	TotalRevenue   int
	NetProfit      int
	Completed      bool
	Error          string

	// Aborted is set for any guarded refusal (location/min-margin/caps/spend-floor):
	// the run reached a clean, defined "did not trade" conclusion, distinct from an
	// operational failure (which surfaces as a non-nil error from Handle).
	Aborted     bool
	AbortReason string

	// Margin gate (evaluated before the buy). SourceAsk is the live price the hull
	// pays at BuyAt; DestBid is the price it would receive at SellAt; MarginPerUnit
	// is DestBid−SourceAsk; MinMarginFloor echoes the requested floor.
	SourceAsk      int
	DestBid        int
	MarginPerUnit  int
	MinMarginFloor int
	MarginAbort    bool

	// Spend-floor guard (mirrors sp-bp6f). TreasuryAtAbort is the live figure that
	// revealed the breach (0 on a blind fail-closed abort where the live read itself
	// failed); ReserveFloor is the floor in effect.
	SpendFloorAbort bool
	TreasuryAtAbort int
	ReserveFloor    int

	// Location guard: the hull must be at BuyAt before the buy. ExpectedLocation and
	// ActualLocation are populated whenever the check runs.
	LocationAbort    bool
	ExpectedLocation string
	ActualLocation   string
}

// RunArbCoordinatorHandler runs the one-shot guarded arb. It composes the proven
// RunTradeRouteCoordinatorHandler primitives rather than re-implementing them: hull
// movement (travel — cross-gate jump+cooldown, then the in-system hop), the
// nav-cache-race-safe dock, and the purchase/sell mediator dispatches are all its
// methods, so this coordinator inherits every fix those legs carry (sp-ynuf dock
// resync, sp-wlev cross-system travel, and — once sp-vzxu lands — the gate→waypoint
// final hop after a jump). It adds only the one-shot sequencing and the pre-buy
// guards on top.
type RunArbCoordinatorHandler struct {
	// legs delegates the movement/trade primitives (travel, dock, purchase, sell,
	// loadShip, observeGood) to the battle-tested circuit handler. Constructed with
	// the same ports so the underlying command handlers are identical to the daemon's.
	legs *RunTradeRouteCoordinatorHandler
	// apiClient live-reads treasury for the spend-floor guard; nil disables it
	// (fail-open on the missing port, matching the sibling guards' optional-port
	// contract). marketRefresher live-refreshes the source market before the margin
	// gate; nil skips the refresh and gates on the cached basis.
	apiClient       domainPorts.APIClient
	marketRefresher MarketRefresher
}

// NewRunArbCoordinatorHandler wires the one-shot arb coordinator with the same driven
// ports as the trade-route circuit, so buys/sells/navigation resolve to the exact
// command handlers the daemon uses. A nil marketRefresher skips the pre-gate live
// source refresh (the gate still runs on the cached basis); a nil apiClient disables
// the working-capital spend-floor guard (fail-open on the missing port). A nil clock
// defaults to RealClock inside the delegated handler.
func NewRunArbCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketRefresher MarketRefresher,
	clock shared.Clock,
	apiClient domainPorts.APIClient,
) *RunArbCoordinatorHandler {
	return &RunArbCoordinatorHandler{
		legs:            NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, marketRefresher, clock, apiClient),
		apiClient:       apiClient,
		marketRefresher: marketRefresher,
	}
}

// Handle executes the one-shot arb. A guarded refusal returns a nil error with the
// matching *Abort flag set (a defined "did not trade" outcome); an operational
// failure mid-run returns the underlying error with AbortReason naming the failed leg.
func (h *RunArbCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunArbCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	response := &RunArbCoordinatorResponse{ShipSymbol: cmd.ShipSymbol}
	if err := h.execute(ctx, cmd, response); err != nil {
		response.Error = err.Error()
		return response, err
	}
	if !response.Aborted {
		response.Completed = true
	}
	return response, nil
}

func (h *RunArbCoordinatorHandler) execute(
	ctx context.Context,
	cmd *RunArbCoordinatorCommand,
	response *RunArbCoordinatorResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	response.Good = cmd.Good
	response.SourceWaypoint = cmd.BuyAt
	response.DestWaypoint = cmd.SellAt

	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = defaultWorkingCapitalReserve
	}

	if cmd.BuyAt == "" || cmd.SellAt == "" || cmd.Good == "" {
		return fmt.Errorf("arb-run requires good, buy-at and sell-at")
	}
	if cmd.BuyAt == cmd.SellAt {
		return fmt.Errorf("buy-at and sell-at must differ (both %s)", cmd.BuyAt)
	}

	// Load the hull the daemon container runner claimed for this run (loadShip does
	// not claim/release — the runner owns that lifecycle).
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}

	// sp-5nqx retry safety — resume from the failed step, NEVER re-buy. The daemon
	// container runner retries a failed run by RE-RUNNING THE WHOLE iteration
	// (buy→travel→sell), so before this guard a post-buy failure (the missing
	// departure hop failing the jump) sent every retry back through the buy and blew
	// past --max-spend: the live incident bought 3× (−39,468/−39,624/−39,780 =
	// 118,872) against a 40k cap. The tranche a prior attempt already bought is
	// physically in the hull's hold, so if the hull is already holding this good we
	// treat the buy as DONE and resume at travel→sell — never re-buying, and never
	// re-running the pre-buy guards (re-gating could abort on a since-collapsed margin
	// and STRAND the very cargo we must deliver). For a one-shot run this IS the
	// cumulative-actuals cap: once any tranche is aboard, additional spend and units
	// are capped at zero. A fresh run (holding none of the good) runs the four guards
	// and buys exactly as before.
	tranche := unitsOfGoodAboard(ship, cmd.Good)
	if tranche > 0 {
		response.UnitsTraded = tranche
		logger.Log("INFO", fmt.Sprintf(
			"Resuming arb: %d units of %s already aboard from a prior attempt — skipping the buy, delivering to %s (retry-safe, no re-buy)",
			tranche, cmd.Good, cmd.SellAt,
		), map[string]interface{}{
			"action": "arb_resume_no_rebuy", "ship_symbol": cmd.ShipSymbol,
			"good": cmd.Good, "held": tranche, "dest": cmd.SellAt,
		})
	} else {
		buyUnits, berr := h.guardAndBuy(ctx, cmd, response, reserve, ship)
		if berr != nil {
			return berr
		}
		if response.Aborted {
			return nil
		}
		tranche = buyUnits
	}

	// --- past the buy (fresh) or resuming a prior one: reload → travel → dock → sell ---

	// Reload the hull so travel routes from the freshly-persisted post-dock/buy state,
	// then travel to the destination (cross-gate: source waypoint→gate hop, jump +
	// cooldown, then the gate→waypoint hop). travel returns the reloaded hull after a jump.
	ship, err = h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		response.AbortReason = fmt.Sprintf("could not reload ship %s before travel: %v", cmd.ShipSymbol, err)
		return err
	}
	ship, err = h.legs.travel(ctx, ship, cmd.SellAt, cmd.PlayerID)
	if err != nil {
		response.AbortReason = fmt.Sprintf("travel of %s to %s failed: %v", cmd.ShipSymbol, cmd.SellAt, err)
		return err
	}

	// Dock at the destination and sell the whole tranche.
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		response.AbortReason = fmt.Sprintf("dock at destination %s failed: %v", cmd.SellAt, err)
		return err
	}
	sellResp, err := h.legs.sell(ctx, cmd.ShipSymbol, cmd.Good, tranche, cmd.PlayerID)
	if err != nil {
		response.AbortReason = fmt.Sprintf("sell of %d %s at %s failed: %v", tranche, cmd.Good, cmd.SellAt, err)
		return err
	}
	response.TotalRevenue = sellResp.TotalRevenue
	response.UnitsTraded = sellResp.UnitsSold
	response.NetProfit = sellResp.TotalRevenue - response.TotalCost

	// sp-5nqx fix (c) — stranded cargo is a FAILURE, never a false success. A run that
	// bought a tranche but could not offload all of it (the destination could not
	// absorb the whole load) ends holding unsold units of the good it bought; that must
	// reflect as a container failure (a non-nil error here → the runner's
	// signalCompletionWithStatus(false)), NOT the success=true the incident logged with
	// 36 units stranded. The message carries good/units/location so the strand is
	// greppable and hand-recoverable.
	if stranded := tranche - sellResp.UnitsSold; stranded > 0 {
		response.AbortReason = fmt.Sprintf(
			"stranded cargo: %d unsold units of %s at %s (sold %d of %d) - reporting failure",
			stranded, cmd.Good, cmd.SellAt, sellResp.UnitsSold, tranche,
		)
		logger.Log("ERROR", response.AbortReason, map[string]interface{}{
			"action": "arb_stranded_cargo", "ship_symbol": cmd.ShipSymbol,
			"good": cmd.Good, "stranded": stranded, "sold": sellResp.UnitsSold,
			"tranche": tranche, "location": cmd.SellAt,
		})
		return fmt.Errorf("%s", response.AbortReason)
	}

	logger.Log("INFO", "One-shot arb complete", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "good": cmd.Good, "source": cmd.BuyAt, "dest": cmd.SellAt,
		"units": response.UnitsTraded, "cost": response.TotalCost, "revenue": response.TotalRevenue, "net": response.NetProfit,
	})
	// One shot: no loop. The container runner releases the hull on this return.
	return nil
}

// guardAndBuy runs the four pre-buy guards (location, min-margin, caps, spend-floor)
// and, if all clear, executes the one-shot buy, returning the units bought. A guarded
// refusal sets response.Aborted (+ the matching *Abort flag) and returns (0, nil) — a
// clean "did not trade" the caller surfaces as an abort. An operational failure returns
// a non-nil error. It runs ONLY on a FRESH attempt: a retry that already holds the good
// resumes past it (sp-5nqx) so a completed buy is never re-run.
func (h *RunArbCoordinatorHandler) guardAndBuy(
	ctx context.Context,
	cmd *RunArbCoordinatorCommand,
	response *RunArbCoordinatorResponse,
	reserve int,
	ship *navigation.Ship,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Guard 1 — location: never buy unless the hull is actually at BuyAt. A hull that
	// drifted (or was mis-specified) must not silently buy at the wrong market.
	actual := ship.CurrentLocation().Symbol
	response.ExpectedLocation = cmd.BuyAt
	response.ActualLocation = actual
	if actual != cmd.BuyAt {
		response.Aborted = true
		response.LocationAbort = true
		response.AbortReason = fmt.Sprintf("ship %s is at %s, not the intended buy-at %s - aborting before buy", cmd.ShipSymbol, actual, cmd.BuyAt)
		logger.Log("WARNING", response.AbortReason, map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "actual": actual, "expected": cmd.BuyAt,
		})
		return 0, nil
	}

	// Dock at the source so the buy can execute and the live source market read
	// returns docked-market prices (dock survives the post-arrival nav-cache race).
	if err := h.legs.dock(ctx, ship, cmd.PlayerID); err != nil {
		response.AbortReason = fmt.Sprintf("dock at source %s failed: %v", cmd.BuyAt, err)
		return 0, err
	}

	// Guard 2 — min-margin: re-read prices at buy time and refuse a buy whose spread
	// does not clear the floor. Live-refresh the source market first (the hull is
	// docked there); a wired refresher that ERRORS fails CLOSED — a one-shot,
	// captain-directed buy must not proceed on an unverified basis (this deliberately
	// diverges from the continuous circuit's fail-open staleAsk guard, which gets
	// another visit to recover; this run gets one shot). No refresher wired → the
	// guard's live-refresh is simply inactive and the gate runs on the cached basis.
	if h.marketRefresher != nil {
		if rerr := h.marketRefresher.ScanAndSaveMarket(ctx, uint(cmd.PlayerID), cmd.BuyAt); rerr != nil {
			response.Aborted = true
			response.MarginAbort = true
			response.AbortReason = fmt.Sprintf("could not live-refresh source market %s to verify margin - aborting before buy (fail-closed): %v", cmd.BuyAt, rerr)
			logger.Log("WARNING", response.AbortReason, map[string]interface{}{
				"waypoint": cmd.BuyAt, "good": cmd.Good, "error": rerr.Error(),
			})
			return 0, nil
		}
	}

	srcGood, err := h.legs.observeGood(ctx, cmd.BuyAt, cmd.Good, cmd.PlayerID)
	if err != nil {
		// No source price at all → fail closed (never buy on an unknown ask).
		response.Aborted = true
		response.MarginAbort = true
		response.AbortReason = fmt.Sprintf("could not read source ask for %s at %s - aborting before buy (fail-closed): %v", cmd.Good, cmd.BuyAt, err)
		return 0, nil
	}
	dstGood, err := h.legs.observeGood(ctx, cmd.SellAt, cmd.Good, cmd.PlayerID)
	if err != nil {
		// No destination price at all → fail closed (never buy on an unknown bid).
		response.Aborted = true
		response.MarginAbort = true
		response.AbortReason = fmt.Sprintf("could not read destination bid for %s at %s - aborting before buy (fail-closed): %v", cmd.Good, cmd.SellAt, err)
		return 0, nil
	}

	sourceAsk := srcGood.SellPrice()   // what the hull PAYS to buy at the source
	destBid := dstGood.PurchasePrice() // what the hull RECEIVES selling at the destination
	marginPerUnit := destBid - sourceAsk
	response.SourceAsk = sourceAsk
	response.DestBid = destBid
	response.MarginPerUnit = marginPerUnit
	response.MinMarginFloor = cmd.MinMargin

	// A non-positive margin is always refused; a positive-but-thin margin is refused
	// only when it misses the caller's explicit floor.
	if marginPerUnit <= 0 || (cmd.MinMargin > 0 && marginPerUnit < cmd.MinMargin) {
		response.Aborted = true
		response.MarginAbort = true
		response.AbortReason = fmt.Sprintf("margin %d/unit (%d bid − %d ask) below floor %d - aborting before buy", marginPerUnit, destBid, sourceAsk, cmd.MinMargin)
		logger.Log("WARNING", response.AbortReason, map[string]interface{}{
			"good": cmd.Good, "source": cmd.BuyAt, "dest": cmd.SellAt,
			"source_ask": sourceAsk, "dest_bid": destBid, "margin": marginPerUnit, "min_margin": cmd.MinMargin,
		})
		return 0, nil
	}

	// Guard 3 — caps: size the tranche to the tightest of hold space, MaxUnits, and
	// MaxSpend/ask, so the buy can never exceed any cap the captain set.
	units := ship.AvailableCargoSpace()
	if cmd.MaxUnits > 0 && cmd.MaxUnits < units {
		units = cmd.MaxUnits
	}
	if cmd.MaxSpend > 0 {
		affordable := cmd.MaxSpend / sourceAsk
		if affordable < units {
			units = affordable
		}
	}
	if units <= 0 {
		response.Aborted = true
		response.AbortReason = fmt.Sprintf("no units to buy after caps (hold space %d, max-units %d, max-spend %d @ ask %d)", ship.AvailableCargoSpace(), cmd.MaxUnits, cmd.MaxSpend, sourceAsk)
		logger.Log("WARNING", response.AbortReason, map[string]interface{}{
			"hold_space": ship.AvailableCargoSpace(), "max_units": cmd.MaxUnits, "max_spend": cmd.MaxSpend, "source_ask": sourceAsk,
		})
		return 0, nil
	}

	// Guard 4 — spend-floor (mirrors sp-bp6f): never execute a buy that would drop
	// live treasury below the working-capital reserve. Fails CLOSED on any live-read
	// failure (missing token or GetAgent error). projectedCost is bounded by MaxSpend
	// already (the sizing above), so this only adds the treasury-floor protection.
	projectedCost := units * sourceAsk
	if h.spendFloorBreached(ctx, projectedCost, reserve, response) {
		if response.AbortReason == "" {
			response.AbortReason = fmt.Sprintf("buy of %d @ %d (=%d) would breach the working-capital floor %d - aborting before spending", units, sourceAsk, projectedCost, reserve)
		}
		return 0, nil
	}

	// --- past every guard: execute the one-shot buy ---
	buyResp, err := h.legs.purchase(ctx, cmd.ShipSymbol, cmd.Good, units, cmd.PlayerID)
	if err != nil {
		response.AbortReason = fmt.Sprintf("purchase of %d %s at %s failed: %v", units, cmd.Good, cmd.BuyAt, err)
		return 0, err
	}
	response.UnitsTraded = buyResp.UnitsAdded
	response.TotalCost = buyResp.TotalCost
	return buyResp.UnitsAdded, nil
}

// unitsOfGoodAboard reports how many units of good the hull currently holds (0 if the
// hold is empty or holds none of it) — the cumulative ACTUAL a retry reads to know it
// has already bought this tranche and must not buy it again (sp-5nqx).
func unitsOfGoodAboard(ship *navigation.Ship, good string) int {
	cargo := ship.Cargo()
	if cargo == nil {
		return 0
	}
	return cargo.GetItemUnits(good)
}

// spendFloorBreached mirrors the trade-route working-capital guard (sp-bp6f) for the
// one-shot buy: it reports whether executing a buy of projectedCost would drop live
// treasury below reserve, and fails CLOSED on any live-read failure (a missing player
// token or a GetAgent error) rather than risk spending blind. A nil apiClient
// disables the guard entirely (fail-open on the missing port). On a confirmed breach
// it records the live treasury figure; on a blind fail-closed abort it leaves
// TreasuryAtAbort at zero, since no live figure was ever obtained.
func (h *RunArbCoordinatorHandler) spendFloorBreached(
	ctx context.Context,
	projectedCost int,
	reserve int,
	response *RunArbCoordinatorResponse,
) bool {
	logger := common.LoggerFromContext(ctx)
	if h.apiClient == nil {
		return false
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("WARNING", "Could not resolve player token for spend-floor check - aborting buy (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.Aborted = true
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		logger.Log("WARNING", "Could not read live treasury for spend-floor check - aborting buy (fail-closed)", map[string]interface{}{
			"error": err.Error(),
		})
		response.Aborted = true
		response.SpendFloorAbort = true
		response.ReserveFloor = reserve
		return true
	}

	if agentData.Credits-projectedCost < reserve {
		logger.Log("WARNING", "Buy would breach the working-capital reserve floor - aborting before spending", map[string]interface{}{
			"treasury": agentData.Credits, "projected_cost": projectedCost, "reserve": reserve,
		})
		response.Aborted = true
		response.SpendFloorAbort = true
		response.TreasuryAtAbort = agentData.Credits
		response.ReserveFloor = reserve
		return true
	}

	return false
}
