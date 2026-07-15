package commands

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultArbSellFloorFraction (sp-lbbm) is the per-tranche sell floor's default
// fraction of the quoted bid: a sell tranche aborts the remainder when the live
// bid falls below 80% of the quote. It mirrors the buy-side live-verify default
// (idle-arb's MarginVerifyFraction), so an unarmed captain arb-run is still
// guarded. A caller (idle-arb, or a future captain knob) overrides via
// RunArbCoordinatorCommand.SellFloorFraction.
const defaultArbSellFloorFraction = 0.80

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
	// SellFloorFraction (sp-lbbm) arms the per-tranche sell floor: each sell tranche
	// aborts the remainder (held aboard) when the LIVE bid falls below this fraction
	// of the QUOTED bid. It reuses the same 80% knob the buy-side live-verify uses,
	// applied uniformly to arb-run and idle-arb (both run this coordinator). 0 →
	// defaultArbSellFloorFraction, so even a captain arb-run with no knob set is
	// guarded.
	SellFloorFraction float64
	// QuotedDestBid (sp-lbbm) carries the quoted destination bid — the healthy
	// anchor the sell floor measures against — across an IN-PROCESS retry, exactly
	// as PriorAttemptCost carries the buy cost: a fresh buy records it here so the
	// retry (same command object, cargo already aboard) floors against the real
	// quote instead of a since-crushed one. It is REPORTING/guard state, never a
	// spend input. A daemon-restart rebuild (which reconstructs the command from
	// config and does not carry this) leaves it 0, and the coordinator falls back
	// to a fresh pre-sell bid observation as the anchor.
	QuotedDestBid int
	// PriorAttemptCost carries a prior attempt's already-incurred buy cost across a
	// resume so the completion P&L is honest (sp-dkj7). On a FRESH run it is 0 and the
	// buy sets TotalCost live; the run then persists that cost (container config +
	// this field) so a resume — the retry re-runs the whole Handle and skips the buy,
	// which otherwise leaves TotalCost=0 and over-reports NetProfit by the full basis —
	// reads it back here and reports the true net. It is REPORTING ONLY: no guard reads
	// it (the spend caps read live state), so it can never gate or resize a buy.
	PriorAttemptCost int
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

	// Routability guard (sp-7gr2): set when a cross-system sell leg was refused
	// pre-buy because no jump-gate route from the buy system to the sell system
	// exists (or could not be verified). The incident bought first and discovered
	// the missing route only after crashing laden at the home gate; this refuses
	// the buy BEFORE spending. AbortReason names both systems.
	RoutabilityAbort bool

	// Sell-floor guard (sp-lbbm): set when the per-tranche sell floor aborted the
	// sale mid-tranche because the LIVE bid fell below the floor, leaving the
	// remainder held aboard. This is an HONEST failure completion (Handle returns a
	// non-nil error), NOT a false success — the held cargo is picked up by the next
	// plan/liquidation leg. Distinct from a routability/margin/spend abort, which
	// all refuse BEFORE buying and hold nothing.
	SellFloorAbort bool
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
	// costPersister durably records a fresh buy's cost so a resumed run reports honest
	// P&L across a daemon restart (sp-dkj7, RULINGS #2). Optional; nil disables the
	// persistence (a cross-restart resume then under-reports cost exactly as before this
	// fix — fail-open, matching the sibling optional-port contract). The daemon injects a
	// container-config-backed persister via SetCostPersister.
	costPersister ArbCostPersister
	// absorptionLedger (sp-78ai L2) converts this leg's PLANNED absorption hold into
	// an EXECUTED recovery shadow at sale completion, so the depth this dump occupies
	// is visible to every engine while it regrows (untagged sinks / zero-unit sales
	// leave none — trade-analyst Q2). Optional: nil disables the convert (the PLANNED
	// row, if any, is then reclaimed by the ledger's dead-container sweep / TTL when
	// the container exits). The daemon injects the DB-backed ledger via
	// SetAbsorptionLedger; a captain-directed arb run with no PLANNED row converts
	// nothing (the update matches zero rows) — harmless.
	absorptionLedger absorption.Ledger
}

// ArbCostPersister durably records a one-shot arb run's already-incurred buy cost
// (keyed by container) so a resumed run — which skips the completed buy and would
// otherwise start its P&L accounting at TotalCost=0 — can restore the prior attempt's
// cost and report an honest NetProfit (sp-dkj7). It exists because the cost is NOT
// otherwise recoverable on resume: cargo carries no cost basis and the launch config
// holds only the lane/caps, so per RULINGS #2 the run must persist it and reload it on
// boot. The daemon backs this with the container config (the same map recovery rebuilds
// the command from). Reporting only — no guard consults the persisted value.
type ArbCostPersister interface {
	// PersistBuyCost records cost as the run's buy cost for the container, so a later
	// resume (in-process retry OR a daemon-restart rebuild) reads it back. A returned
	// error is advisory: the buy has already succeeded, so the caller logs and continues
	// (a persistence failure degrades resume reporting, never fails a completed buy).
	PersistBuyCost(ctx context.Context, containerID string, playerID, cost int) error
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

// SetGateGraph wires the multi-jump gate-graph resolver (sp-7gr2) into the
// delegated movement handler (so travel crosses multi-hop gaps) AND enables this
// coordinator's pre-buy routability guard, which route-checks a cross-system
// sell leg through the SAME instance before spending. Left unset (nil), both the
// legacy single-jump travel and the guard's fail-open skip stay in place, so no
// existing caller or test changes. Mirrors the SetSpendLedger injection idiom.
func (h *RunArbCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.legs.SetGateGraph(g)
}

// SetEventSubscriber wires the ship-arrival event bus into the delegated movement
// handler so the resume path waits out a hull re-adopted mid-transit before
// attempting the jump (sp-8l3o) instead of 4214'ing and burning the restart budget.
// Left unset (nil), the pre-movement in-transit wait is skipped, so no existing
// caller or test changes. Mirrors the SetGateGraph delegation.
func (h *RunArbCoordinatorHandler) SetEventSubscriber(subscriber navigation.ShipEventSubscriber) {
	h.legs.SetEventSubscriber(subscriber)
}

// SetCostPersister wires the durable buy-cost store (sp-dkj7) so a fresh run records its
// tranche cost for an honest resume P&L. Left unset (nil), a resumed run reports NetProfit
// without the prior attempt's cost, exactly as before this fix (fail-open). Mirrors the
// SetGateGraph optional-injection idiom.
func (h *RunArbCoordinatorHandler) SetCostPersister(p ArbCostPersister) {
	h.costPersister = p
}

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L2) so a
// completed sale converts this leg's PLANNED hold into a recovery shadow. Left unset
// (nil), the convert is skipped (a leg's PLANNED row is reclaimed by the ledger's own
// sweep on container exit). Mirrors the SetCostPersister optional-injection idiom.
func (h *RunArbCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger) {
	h.absorptionLedger = ledger
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

	// sp-ieqj: stamp this run's operation context so every ledger row AND every refuel
	// it writes inherits operation_type="arb_run" instead of the 'manual' fallback. The
	// one-shot buy→travel→sell delegates to the shared trade-route legs, which are
	// ctx-transparent (they record whatever operation context rides the ctx), and
	// travel's RouteExecutor propagates this ctx verbatim to every RefuelShipCommand it
	// fires. Before this stamp arb's PURCHASE_CARGO/SELL_CARGO and its travel refuels
	// landed unattributed, crediting arbitrage P&L to no engine. Mirrors how every
	// sibling coordinator tags its writes at the boundary (trade_route/tour/stocker/…).
	// A run built without a ContainerID (direct/CLI) yields a nil context and stays
	// 'manual' — the honest ad-hoc default.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(cmd.ContainerID, "arb_run"))

	// Load the hull the daemon container runner claimed for this run (loadShip does
	// not claim/release — the runner owns that lifecycle).
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}
	// Expose this run's intent on the read-only flow feed (fire-and-forget; a missed
	// publish never touches the trade path — RULINGS #4).
	flowfeed.Publish(buildArbFlow(cmd, shipCargoItems(ship), time.Now().UTC()))

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
		// sp-dkj7 — the buy is DONE (its tranche is physically aboard), so restore the
		// prior attempt's cost the run persisted before it was interrupted. Without this
		// the resumed run's accounting starts at TotalCost=0 and the completion line
		// reports NetProfit as the full sale revenue, silently omitting the basis it
		// already paid. PriorAttemptCost is 0 only when the cost was never persisted (no
		// persister wired, or the crash beat the persist) — the honest fail-open floor,
		// the pre-fix behavior, never an over-count.
		response.TotalCost = cmd.PriorAttemptCost
		logger.Log("INFO", fmt.Sprintf(
			"Resuming arb: %d units of %s already aboard from a prior attempt — skipping the buy, delivering to %s (retry-safe, no re-buy; prior cost %d)",
			tranche, cmd.Good, cmd.SellAt, cmd.PriorAttemptCost,
		), map[string]interface{}{
			"action": "arb_resume_no_rebuy", "ship_symbol": cmd.ShipSymbol,
			"good": cmd.Good, "held": tranche, "dest": cmd.SellAt, "prior_cost": cmd.PriorAttemptCost,
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
		// sp-dkj7 — the buy just succeeded; durably record its cost so if this run is
		// interrupted before it sells, the resume (in-process retry or daemon-restart
		// rebuild) restores it above and reports honest P&L. Reporting-only and
		// best-effort: a persist failure never fails a completed buy.
		h.persistBuyCostForResume(ctx, cmd, response.TotalCost)
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
	// PER-TRANCHE SELL FLOOR (sp-lbbm): arm the sale with a per-unit floor so a bid
	// our own tranches (or a colliding hull) crush mid-sale aborts the remainder
	// instead of dumping it — the H50 fix (five tranches for 27 credits). The floor
	// is ceil(fraction × QUOTED bid); the quote is the healthy anchor: a fresh run
	// has it live in DestBid, an in-process retry carries it on the command, and a
	// daemon-restart resume (neither available) falls back to a fresh pre-sell
	// observation. No obtainable anchor → floor disabled for this sale (fail-open on
	// a missing basis, matching the buy guard's cached-basis path) rather than
	// refuse a legitimate sale we cannot anchor.
	sellFloorFraction := cmd.SellFloorFraction
	if sellFloorFraction <= 0 {
		sellFloorFraction = defaultArbSellFloorFraction
	}
	quotedBid := response.DestBid
	if quotedBid <= 0 {
		quotedBid = cmd.QuotedDestBid
	}
	if quotedBid <= 0 {
		if g, oerr := h.legs.observeGood(ctx, cmd.SellAt, cmd.Good, cmd.PlayerID); oerr == nil {
			quotedBid = g.PurchasePrice()
		}
	}
	minBidPerUnit := 0
	if quotedBid > 0 {
		minBidPerUnit = int(math.Ceil(sellFloorFraction * float64(quotedBid)))
	}

	sellResp, err := h.legs.sellWithFloor(ctx, cmd.ShipSymbol, cmd.Good, tranche, cmd.PlayerID, minBidPerUnit)
	if err != nil {
		response.AbortReason = fmt.Sprintf("sell of %d %s at %s failed: %v", tranche, cmd.Good, cmd.SellAt, err)
		return err
	}
	response.TotalRevenue = sellResp.TotalRevenue
	response.UnitsTraded = sellResp.UnitsSold
	response.NetProfit = sellResp.TotalRevenue - response.TotalCost

	// sp-78ai L2: convert this leg's PLANNED absorption hold into an EXECUTED recovery
	// shadow with what ACTUALLY sold, before the held-cargo failure check below so a
	// partial sale still records the depth it consumed. A zero-unit or untagged sale
	// records nothing and releases the hold (Q2). No-op for a captain-directed arb run
	// that never reserved (the update matches zero PLANNED rows).
	h.convertAbsorptionShadow(ctx, cmd, sellResp.UnitsSold)

	// A held remainder is a FAILURE, never a false success (sp-5nqx fix c, sp-lbbm).
	// It arises two ways, both the stranded-veto situation: the sell floor aborted
	// the sale (the bid crashed — sp-lbbm), or the destination could not absorb the
	// whole tranche. Either leaves unsold units aboard, which must reflect as a
	// container failure (a non-nil error → the runner's signalCompletionWithStatus(
	// false)), NOT the success=true the incident logged with cargo stranded — so the
	// m5kv/next-leg liquidation path picks the held cargo up. The sell-floor case is
	// named distinctly (SellFloorAbort) so a deliberate money-guard hold reads apart
	// from a destination-capacity strand; both carry good/units/location for
	// greppable hand-recovery.
	if held := tranche - sellResp.UnitsSold; held > 0 {
		if sellResp.FloorAborted {
			response.SellFloorAbort = true
			response.AbortReason = fmt.Sprintf(
				"sell-floor abort: live bid %d < floor %d/unit (%.0f%% of quoted bid %d) at %s - sold %d of %d, %d units of %s held aboard for later liquidation",
				sellResp.FloorObservedBid, minBidPerUnit, sellFloorFraction*100, quotedBid, cmd.SellAt,
				sellResp.UnitsSold, tranche, held, cmd.Good,
			)
			logger.Log("WARNING", response.AbortReason, map[string]interface{}{
				"action": "arb_sell_floor_abort", "ship_symbol": cmd.ShipSymbol,
				"good": cmd.Good, "sell_at": cmd.SellAt, "live_bid": sellResp.FloorObservedBid,
				"floor": minBidPerUnit, "quoted_bid": quotedBid, "sold": sellResp.UnitsSold, "held": held,
			})
			return fmt.Errorf("%s", response.AbortReason)
		}
		response.AbortReason = fmt.Sprintf(
			"stranded cargo: %d unsold units of %s at %s (sold %d of %d) - reporting failure",
			held, cmd.Good, cmd.SellAt, sellResp.UnitsSold, tranche,
		)
		logger.Log("ERROR", response.AbortReason, map[string]interface{}{
			"action": "arb_stranded_cargo", "ship_symbol": cmd.ShipSymbol,
			"good": cmd.Good, "stranded": held, "sold": sellResp.UnitsSold,
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

	// Guard 0 — routability (sp-7gr2): never buy a tranche whose sell leg sits in a
	// system we cannot reach. The incident bought at C37, flew to the home gate, then
	// discovered there was NO jump route to JP61 and crashed laden — spend first, learn
	// unroutable after. This inverts that order: a cross-system sell leg is route-checked
	// over the gate graph BEFORE the buy and refuses it (fail CLOSED) if unroutable OR if
	// the check itself cannot be completed. A same-system lane needs no jump and skips the
	// check; no gate graph wired skips it too (fail-open on the missing port, matching the
	// sibling guards' optional-port contract).
	if gateGraph := h.legs.gateGraphResolver(); gateGraph != nil {
		buySystem := shared.ExtractSystemSymbol(cmd.BuyAt)
		sellSystem := shared.ExtractSystemSymbol(cmd.SellAt)
		if buySystem != sellSystem {
			routable, rerr := gateGraph.Routable(ctx, buySystem, sellSystem, cmd.PlayerID)
			if rerr != nil {
				response.Aborted = true
				response.RoutabilityAbort = true
				response.AbortReason = fmt.Sprintf("could not verify a jump-gate route from %s to %s - aborting before buy (fail-closed): %v", buySystem, sellSystem, rerr)
				logger.Log("WARNING", response.AbortReason, map[string]interface{}{
					"buy_system": buySystem, "sell_system": sellSystem, "buy_at": cmd.BuyAt, "sell_at": cmd.SellAt, "error": rerr.Error(),
				})
				return 0, nil
			}
			if !routable {
				response.Aborted = true
				response.RoutabilityAbort = true
				response.AbortReason = fmt.Sprintf("no jump-gate route from %s (buy %s) to %s (sell %s) - refusing to buy cargo that cannot be delivered", buySystem, cmd.BuyAt, sellSystem, cmd.SellAt)
				logger.Log("WARNING", response.AbortReason, map[string]interface{}{
					"buy_system": buySystem, "sell_system": sellSystem, "buy_at": cmd.BuyAt, "sell_at": cmd.SellAt,
				})
				return 0, nil
			}
		}
	}

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
	// sp-lbbm: record the quoted bid as the sell floor's healthy anchor, so an
	// in-process retry (same command object, tranche already aboard, DestBid not
	// re-read) still floors against the real quote rather than a since-crushed bid.
	cmd.QuotedDestBid = destBid

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

	// --- past every guard: execute the one-shot buy under the per-tranche buy ceiling ---
	// sp-9mkf: the margin guard above re-read the live source ask ONCE, but this buy
	// splits into market-limited sub-tranches and the ask ladders up WITHIN them — the
	// D39 incident walked 3,985→~7k inside a single dispatch, realising −3,430/u. Arm
	// the per-tranche ceiling at the max ask that still clears the lane's justifying
	// margin: the quoted dest bid minus the min-margin floor (or one credit when no
	// floor is set, so a non-positive-margin unit can never be bought). Each sub-tranche
	// re-reads the live ask and aborts the remainder once it ladders past this ceiling.
	// destBid is the quoted (cached) bid — the sell floor (sp-lbbm) guards the far-side
	// bid-crash; together they bound the loss, per RULINGS #4 layered defense.
	maxAskPerUnit := destBid - cmd.MinMargin
	if cmd.MinMargin <= 0 {
		maxAskPerUnit = destBid - 1
	}
	buyResp, err := h.legs.purchaseWithCeiling(ctx, cmd.ShipSymbol, cmd.Good, units, cmd.PlayerID, maxAskPerUnit)
	if err != nil {
		response.AbortReason = fmt.Sprintf("purchase of %d %s at %s failed: %v", units, cmd.Good, cmd.BuyAt, err)
		return 0, err
	}
	response.UnitsTraded = buyResp.UnitsAdded
	response.TotalCost = buyResp.TotalCost
	// The ceiling tripped before ANY unit was bought → the ask had already laddered past
	// the lane's justifying margin at the top of the book. Treat as a clean margin abort
	// (no travel, no sell) rather than proceed to sell nothing.
	if buyResp.UnitsAdded == 0 && buyResp.CeilingAborted {
		response.Aborted = true
		response.MarginAbort = true
		response.AbortReason = fmt.Sprintf(
			"buy ceiling: live ask %d > ceiling %d/unit (quoted bid %d − min-margin %d) at %s - aborting before spend, zero bought",
			buyResp.CeilingObservedAsk, maxAskPerUnit, destBid, cmd.MinMargin, cmd.BuyAt)
		logger.Log("WARNING", response.AbortReason, map[string]interface{}{
			"action": "arb_buy_ceiling_abort", "ship_symbol": cmd.ShipSymbol, "good": cmd.Good,
			"source": cmd.BuyAt, "live_ask": buyResp.CeilingObservedAsk, "ceiling": maxAskPerUnit,
			"quoted_bid": destBid, "min_margin": cmd.MinMargin,
		})
		return 0, nil
	}
	return buyResp.UnitsAdded, nil
}

// persistBuyCostForResume durably records a fresh buy's cost so a resumed run reports
// honest P&L (sp-dkj7). It writes to TWO places, covering both restart shapes:
//
//   - cmd.PriorAttemptCost, in memory, for an IN-PROCESS retry: the container runner
//     re-runs the SAME command object on a backoff, so the next Handle's resume branch
//     reads the cost straight off the command without touching the store.
//   - the durable store (container config), for a DAEMON-RESTART rebuild: recovery
//     reconstructs the command from persisted config, so the cost must survive there for
//     buildArbCoordinatorCommand to reload it (RULINGS #2).
//
// It is strictly reporting bookkeeping and best-effort: a zero/negative cost or a missing
// container ID is nothing to persist, a nil store means persistence is disabled, and a
// store error is logged but NEVER propagated — the buy has already succeeded, and a
// completed buy must never be reported as a failure over a P&L-durability miss.
func (h *RunArbCoordinatorHandler) persistBuyCostForResume(ctx context.Context, cmd *RunArbCoordinatorCommand, cost int) {
	cmd.PriorAttemptCost = cost
	if h.costPersister == nil || cmd.ContainerID == "" || cost <= 0 {
		return
	}
	if err := h.costPersister.PersistBuyCost(ctx, cmd.ContainerID, cmd.PlayerID, cost); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"could not persist arb buy cost %d for container %s - a cross-restart resume will under-report NetProfit by this basis: %v",
			cost, cmd.ContainerID, err,
		), map[string]interface{}{
			"action": "arb_cost_persist_failed", "container_id": cmd.ContainerID, "cost": cost, "error": err.Error(),
		})
	}
}

// convertAbsorptionShadow converts this leg's PLANNED absorption hold into an
// EXECUTED recovery shadow at sale completion (sp-78ai L2), keyed by the container +
// sink. It reads the sink good's LIVE activity tier and trade_volume (post-sale, the
// cache is fresh) so the shadow decays on the right curve and sizes its own recovery
// floor. Best-effort and fail-open, exactly like persistBuyCostForResume: a nil
// ledger disables it, a missing container ID has nothing to convert, and a convert
// error is logged but NEVER propagated — the sale has completed, and it must not be
// reported as a failure over a ledger miss (the sell floor + live-verify are the hard
// guards; the shadow is advisory coordination). A zero-unit or untagged-sink sale
// records no shadow and releases the hold (trade-analyst Q2), which the ledger's
// ConvertByContainer encodes.
func (h *RunArbCoordinatorHandler) convertAbsorptionShadow(ctx context.Context, cmd *RunArbCoordinatorCommand, realizedUnits int) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return
	}
	tier := ""
	tradeVolume := 0
	if g, err := h.legs.observeGood(ctx, cmd.SellAt, cmd.Good, cmd.PlayerID); err == nil && g != nil {
		if a := g.Activity(); a != nil {
			tier = *a
		}
		tradeVolume = g.TradeVolume()
	}
	key := absorption.LaneKey{Waypoint: cmd.SellAt, Good: cmd.Good, Side: absorption.SideSell}
	if err := h.absorptionLedger.ConvertByContainer(ctx, cmd.ContainerID, cmd.PlayerID, key, realizedUnits, tier, tradeVolume); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"could not convert absorption shadow for container %s at %s/%s (sale completed; ledger coordination degraded, guards intact): %v",
			cmd.ContainerID, cmd.SellAt, cmd.Good, err,
		), map[string]interface{}{
			"action": "arb_absorption_convert_failed", "container_id": cmd.ContainerID,
			"sell_at": cmd.SellAt, "good": cmd.Good, "error": err.Error(),
		})
	}
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
