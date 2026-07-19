package contract

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Sourcing cost-optimizer. Contract sourcing is HOME-SYSTEM ONLY by standing
// ruling (RULINGS #14): a contract's goods are sourced exclusively within the
// delivery's HOME system. The serial one-active-contract worker flies in-system
// NavigateAndDock/NavigateRouteCommand with zero jump capability, so a source in
// ANY other system is unreachable and must be UNSELECTABLE — never
// dispatched-then-crashed ('waypoint not found in cache'). Cross-system
// logistics belongs to the parallel trade engine, never the contract worker.
// Every pass recomputes from the live contract + market cache — no new
// persisted state — so a daemon restart re-derives the same decision (RULINGS #2).
//
// Negative-margin sourcing: when a sourcing run's projected net is worse than
// −SourcingDeferThresholdPct of the payout, the contract path SOURCES ANYWAY and
// logs the loss (the Overridden decision) — it NEVER parks. RULINGS #1
// never-skip governs the contract path over the profit guard. The
// SourcingDeferSafetyWindow / SourcingDeferRecheckInterval / DeferMessage
// machinery below is retained but inert on the contract path (the sole caller).
const (
	// SourcingDeferThresholdPct: a sourcing run may not EXECUTE at projected net
	// worse than −20% of the contract payout without a logged defer/override
	// decision. Percent of payout, integer math.
	SourcingDeferThresholdPct = 20

	// SourcingDeferSafetyWindow is how much runway must remain before the
	// contract deadline for a defer to be allowed. Inside the window the engine
	// sources at ANY projected margin rather than risk missing fulfillment —
	// never-skip (RULINGS #1) outranks margin. Deadlines run ~7 days; feed
	// crushes revert in hours, so 24h of protected runway is generous.
	SourcingDeferSafetyWindow = 24 * time.Hour

	// SourcingDeferRecheckInterval is how long the coordinator parks between
	// defer re-projections. Market scans land on the scout cadence, so
	// re-checking faster than this only burns passes.
	SourcingDeferRecheckInterval = 60 * time.Second

	// SourcingLadderCapNumer/Denom cap the intra-run ask ladder at 1.5× the
	// projected unit ask. A buyer laddering a SCARCE ask upward tranche after
	// tranche can otherwise blow the budget; when a purchase trip realizes worse
	// than this cap the loop stops buying, delivers what is aboard, and the
	// remainder re-gates through the defer projection on the coordinator's next
	// pass at live prices.
	SourcingLadderCapNumer = 3
	SourcingLadderCapDenom = 2
)

// SourcingPlan is the chosen sourcing decision for a contract's first
// unfulfilled delivery: where to buy, at what cached ask, and what the run is
// projected to cost. Contract sourcing is HOME-system only (RULINGS #14), so the
// chosen market is always inside the delivery's HOME system.
type SourcingPlan struct {
	Good           string
	Market         string // waypoint symbol of the chosen source (market OR storage waypoint for INVENTORY)
	UnitAsk        int    // cached ask at the chosen market; 0 for INVENTORY (sunk cost)
	UnitsRemaining int    // units still to source for the delivery
	GoodsCost      int    // UnitAsk × UnitsRemaining; 0 for INVENTORY
	EffectiveCost  int    // the defer projection basis; equals GoodsCost (home-system only, no travel term)

	// Source distinguishes a MARKET buy from an INVENTORY withdrawal. Defaults
	// to SourceMarket, so every pre-existing plan is a market plan and existing
	// behavior is unchanged.
	Source SourcingSource

	// StorageOperationID is the warehouse operation to withdraw from when
	// Source == SourceInventory (empty otherwise). It is informational for the
	// projection/logging path; the executor re-consults the finder at withdrawal
	// time for the freshest reservation (fail-open, RULINGS #1).
	StorageOperationID string
}

// PlanSourcing picks the cheapest market in the delivery's HOME system for the
// contract's first unfulfilled delivery and returns the costed plan. Contract
// sourcing is single-system by ruling (RULINGS #14): only markets in the
// delivery system are candidates, so a cross-system source can never be
// selected. An error means no market in the home system sells the good yet —
// callers treat that exactly like a FindPurchaseMarket miss (wait for scouts /
// re-project next pass), never a skip (RULINGS #1).
func PlanSourcing(
	ctx context.Context,
	contract *domainContract.Contract,
	marketRepo market.MarketRepository,
	playerID int,
	opts ...SourcingOption,
) (*SourcingPlan, error) {
	for _, delivery := range contract.Terms().Deliveries {
		if delivery.UnitsRequired-delivery.UnitsFulfilled == 0 {
			continue
		}
		return PlanDeliverySourcing(ctx, delivery, marketRepo, playerID, opts...)
	}

	return nil, fmt.Errorf("no unfulfilled deliveries found in contract")
}

// PlanDeliverySourcing costs and picks the cheapest HOME-system market for ONE
// delivery (see PlanSourcing). Exposed separately so the worker's profitability
// evaluation can price each delivery of a multi-delivery contract at its own
// chosen market. Candidates are scoped to the delivery system — the worker's
// zero-jump reality — so the projection here matches what the executor can
// actually fly.
func PlanDeliverySourcing(
	ctx context.Context,
	delivery domainContract.Delivery,
	marketRepo market.MarketRepository,
	playerID int,
	opts ...SourcingOption,
) (*SourcingPlan, error) {
	logger := common.LoggerFromContext(ctx)
	cfg := newSourcingConfig(opts)

	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	deliverySystem := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

	// INVENTORY-FIRST: before any market candidate, consult the warehouse.
	// Stock of this good in the DELIVERY system is a zero-ask source — the
	// units are already paid for (deposit sunk), so the projection treats
	// them as free, which is economically correct for the contract engine's
	// park/proceed decision (a stocked contract is never in the runaway-ask
	// class the defer gate exists to park). Fail-open (RULINGS #1): a nil
	// finder, no stock, or any read error inside the finder yields nil here,
	// and sourcing falls through to the market candidate below
	// byte-identical — inventory only ever ADDS a cheaper source, it never
	// skips or parks a contract. Withdrawal is single-system by construction:
	// the finder only returns warehouses in deliverySystem (RULINGS #14).
	if cfg.inventory != nil {
		if src := cfg.inventory.FindInSystemInventory(ctx, playerID, deliverySystem, delivery.TradeSymbol); src != nil && src.UnitsAvailable > 0 {
			plan := &SourcingPlan{
				Good:               delivery.TradeSymbol,
				Market:             src.StorageWaypoint,
				UnitAsk:            0,
				UnitsRemaining:     unitsRemaining,
				GoodsCost:          0,
				EffectiveCost:      0,
				Source:             SourceInventory,
				StorageOperationID: src.OperationID,
			}
			logger.Log("INFO", fmt.Sprintf(
				"Sourcing plan for %s: %d units from INVENTORY @ 0 ask at warehouse %s (op %s, %d units on hand) - sunk cost, zero-ask projection",
				plan.Good, plan.UnitsRemaining, plan.Market, src.OperationID, src.UnitsAvailable,
			), map[string]interface{}{
				"action":          "plan_sourcing",
				"trade_symbol":    plan.Good,
				"market":          plan.Market,
				"unit_ask":        0,
				"units":           plan.UnitsRemaining,
				"effective_cost":  0,
				"source":          string(SourceInventory),
				"storage_op":      src.OperationID,
				"units_available": src.UnitsAvailable,
			})
			return plan, nil
		}
	}

	// HOME-system market: contract sourcing only ever considers the delivery's
	// HOME system (RULINGS #14). A source in any other system is unreachable by
	// the in-system worker and must be UNSELECTABLE — never
	// dispatched-then-crashed ('waypoint not found in cache for system ...').
	inSystem, err := marketRepo.FindCheapestMarketSelling(ctx, delivery.TradeSymbol, deliverySystem, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
	}

	best := candidateOrNil(inSystem, unitsRemaining)
	if best == nil {
		return nil, fmt.Errorf("no market found selling %s in system %s", delivery.TradeSymbol, deliverySystem)
	}

	logger.Log("INFO", fmt.Sprintf(
		"Sourcing plan for %s: %d units @ %d ask at %s (goods %d = effective %d, home system %s)",
		best.Good, best.UnitsRemaining, best.UnitAsk, best.Market,
		best.GoodsCost, best.EffectiveCost, deliverySystem,
	), map[string]interface{}{
		"action":         "plan_sourcing",
		"trade_symbol":   best.Good,
		"market":         best.Market,
		"unit_ask":       best.UnitAsk,
		"units":          best.UnitsRemaining,
		"effective_cost": best.EffectiveCost,
	})

	return best, nil
}

// candidateOrNil costs one HOME-system market candidate into a SourcingPlan, or
// nil for a nil market.
func candidateOrNil(m *market.CheapestMarketResult, units int) *SourcingPlan {
	if m == nil {
		return nil
	}
	goods := m.SellPrice * units
	return &SourcingPlan{
		Good:           m.TradeSymbol,
		Market:         m.WaypointSymbol,
		UnitAsk:        m.SellPrice,
		UnitsRemaining: units,
		GoodsCost:      goods,
		EffectiveCost:  goods,
		Source:         SourceMarket,
	}
}

// SourcingDeferDecision is the outcome of projecting a sourcing run against the
// −SourcingDeferThresholdPct-of-payout line. On the contract sourcing path it is
// one of just two shapes — it NEVER parks (RULINGS #1 never-skip governs the
// contract path over the profit guard):
//   - proceed:                     Defer=false, Overridden=false (projection clears the line)
//   - override (source-at-a-loss): Defer=false, Overridden=true  (negative projection — source anyway, loudly)
//
// Defer (park) is the historical third shape. EvaluateSourcingDefer never raises
// it for a contract; the field is retained so the never-park invariant stays
// directly assertable (TestSourcing_UnsourceableAtProfit_StillSources) and so the
// coordinator's legacy Defer branch is provably inert.
type SourcingDeferDecision struct {
	Defer      bool // never raised on the contract path — see type doc
	Overridden bool

	ProjectedNet int       // payout − plan.EffectiveCost
	Payout       int       // OnAccepted + OnFulfilled
	Threshold    int       // −SourcingDeferThresholdPct% of payout
	Deadline     time.Time // parsed contract deadline (zero when unparseable)
	Runway       time.Duration
}

// EvaluateSourcingDefer projects the sourcing run's net against the
// −SourcingDeferThresholdPct-of-payout line and decides proceed vs
// source-at-a-loss. Pure — callers supply now — so the decision is directly
// testable. It NEVER parks a contract: RULINGS #1 (never-skip) governs the
// contract sourcing path over the profit guard, so a negative projection is
// flagged Overridden (source anyway, log the loss) and Defer is never raised.
func EvaluateSourcingDefer(plan *SourcingPlan, contract *domainContract.Contract, now time.Time) SourcingDeferDecision {
	payout := contract.Terms().Payment.OnAccepted + contract.Terms().Payment.OnFulfilled
	d := SourcingDeferDecision{
		Payout:       payout,
		ProjectedNet: payout - plan.EffectiveCost,
		Threshold:    -(payout * SourcingDeferThresholdPct) / 100,
	}

	if d.ProjectedNet >= d.Threshold {
		return d // projection clears the line — proceed normally
	}

	// Negative projection. RULINGS #1 never-skip GOVERNS the contract sourcing
	// path over this profit guard: an accepted contract is ALWAYS
	// sourced+delivered+fulfilled, even at a projected loss. So we never park —
	// flag the run Overridden (source at the loss, log it loudly) and proceed.
	// The deadline is parsed only to enrich the log.
	if deadline, err := time.Parse(time.RFC3339, contract.Terms().Deadline); err == nil {
		d.Deadline = deadline
		d.Runway = deadline.Sub(now)
	}
	d.Overridden = true
	return d
}

// DeferMessage renders the Guard-1-style defer line: every number an operator
// needs — projected net, payout, best ask, market — lives in the MESSAGE TEXT,
// not only in structured fields.
func (d SourcingDeferDecision) DeferMessage(plan *SourcingPlan) string {
	return fmt.Sprintf(
		"Sourcing deferred: projected net %d worse than %d (-%d%% of payout %d) - %d units of %s @ best ask %d at %s - parking until asks revert, re-projecting every pass (deadline %s, runway %s; never-skip stands)",
		d.ProjectedNet, d.Threshold, SourcingDeferThresholdPct, d.Payout,
		plan.UnitsRemaining, plan.Good, plan.UnitAsk, plan.Market,
		d.Deadline.Format(time.RFC3339), d.Runway.Round(time.Minute),
	)
}

// OverrideMessage renders the negative-margin sourcing line with the same
// numbers in the text: the run is knowingly sourcing at a projected loss because
// never-skip (RULINGS #1) governs the contract path over the profit guard — a
// contract always sources+delivers+fulfills regardless of margin. The deadline
// is shown for context only; it is not the reason the run proceeds.
func (d SourcingDeferDecision) OverrideMessage(plan *SourcingPlan) string {
	deadline := "unparseable"
	if !d.Deadline.IsZero() {
		deadline = fmt.Sprintf("%s, runway %s", d.Deadline.Format(time.RFC3339), d.Runway.Round(time.Minute))
	}
	return fmt.Sprintf(
		"Sourcing at negative margin: projected net %d worse than %d (-%d%% of payout %d) - sourcing %d units of %s @ ask %d at %s anyway (never-skip, RULINGS #1: contracts always source+deliver+fulfill even at a projected loss) (deadline %s)",
		d.ProjectedNet, d.Threshold, SourcingDeferThresholdPct, d.Payout,
		plan.UnitsRemaining, plan.Good, plan.UnitAsk, plan.Market, deadline,
	)
}
