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

// Sourcing cost-optimizer (sp-1z2h). Contracts previously sourced at whatever
// market FindPurchaseMarket picked inside the delivery system, with zero margin
// awareness — one ELECTRONICS contract laddered a post-crush SCARCE ask into a
// −891,812 NET fill. Two defenses, both decision-time only (no new persisted
// state — every pass recomputes from the live contract + market cache, so a
// daemon restart re-derives the same decision, RULINGS #2):
//
//   1. Cheapest REACHABLE market: candidates now include cross-gate export
//      markets (travel() is jump-capable both directions). Weighting is
//      deliberately simple and documented on CrossGateSourcingPenalty below.
//   2. DEFER-while-negative: a sourcing run whose projected net is worse than
//      −SourcingDeferThresholdPct of the payout is parked (not skipped —
//      RULINGS #1) until asks revert, re-checked every coordinator pass,
//      unless the contract deadline is inside SourcingDeferSafetyWindow, in
//      which case it sources anyway and logs the override.
const (
	// CrossGateSourcingPenalty is the flat credit penalty charged to a sourcing
	// candidate in a DIFFERENT system than the delivery destination. It prices
	// the jump-gate round trip: gate fuel/antimatter, jump cooldown, and the
	// extra wall-clock the hull spends off-lane. A flat constant (rather than a
	// distance model) is deliberate: the ask deltas this optimizer exists to
	// arbitrate are 100k–3M (evidence on sp-5bmq: GQ92-F44 ELECTRONICS at 2,367
	// vs home's laddered 6k+ ≈ 2.9M on 804 units), so the only structurally
	// meaningful travel term is "does this cross a gate at all". In-system
	// distance differences are noise at contract scale and are priced at zero.
	// A cross-gate candidate therefore wins only when its total-goods saving
	// exceeds this penalty — it can never win on a few hundred credits.
	CrossGateSourcingPenalty = 25_000

	// SourcingDeferThresholdPct is the bead's defer line (sp-1z2h acceptance):
	// a sourcing run may not EXECUTE at projected net worse than −20% of the
	// contract payout without a logged defer/override decision. Percent of
	// payout, integer math.
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
	// projected unit ask (the trade-analyst's number on sp-5bmq lever 3). The
	// −891k class is a buyer laddering a SCARCE ask upward tranche after
	// tranche; when a purchase trip realizes worse than this cap the loop stops
	// buying, delivers what is aboard, and the remainder re-gates through the
	// defer projection on the coordinator's next pass at live prices.
	SourcingLadderCapNumer = 3
	SourcingLadderCapDenom = 2
)

// CrossSystemMarketFinder is the optional repository upgrade that unlocks
// cross-gate sourcing candidates. Kept as a separate narrow interface (not a
// MarketRepository method) so the many existing MarketRepository fakes keep
// compiling; a repository that doesn't implement it simply scopes sourcing to
// the delivery system, exactly as before.
type CrossSystemMarketFinder interface {
	// FindCheapestMarketsSellingAllSystems returns up to limit markets selling
	// goodSymbol across ALL scanned systems, cheapest first. Market data only
	// exists for systems scouts have flown, which is this engine's working
	// definition of "reachable".
	FindCheapestMarketsSellingAllSystems(ctx context.Context, goodSymbol string, playerID int, limit int) ([]market.CheapestMarketResult, error)
}

// crossSystemCandidateLimit bounds the all-systems scan; the reduce below keeps
// only the cheapest market per system, and a handful of systems have data.
const crossSystemCandidateLimit = 25

// SourcingPlan is the chosen sourcing decision for a contract's first
// unfulfilled delivery: where to buy, at what cached ask, and what the run is
// projected to cost including the travel penalty term.
type SourcingPlan struct {
	Good           string
	Market         string // waypoint symbol of the chosen market
	UnitAsk        int    // cached ask at the chosen market
	UnitsRemaining int    // units still to source for the delivery
	GoodsCost      int    // UnitAsk × UnitsRemaining
	TravelPenalty  int    // 0 in-system; CrossGateSourcingPenalty cross-gate
	EffectiveCost  int    // GoodsCost + TravelPenalty — the defer projection basis
	CrossSystem    bool   // true when the chosen market is outside the delivery system
}

// PlanSourcing picks the cheapest REACHABLE market for the contract's first
// unfulfilled delivery and returns the costed plan. Candidates are the cheapest
// market inside the delivery system plus, when the repository supports it, the
// cheapest market in each other scanned system; each candidate is weighed as
//
//	effective cost = units × ask + travel penalty
//
// (see CrossGateSourcingPenalty for why the penalty is flat). Ties go to the
// in-system candidate. An error means no market anywhere sells the good yet —
// callers treat that exactly like the old FindPurchaseMarket miss (wait for
// scouts).
func PlanSourcing(
	ctx context.Context,
	contract *domainContract.Contract,
	marketRepo market.MarketRepository,
	playerID int,
) (*SourcingPlan, error) {
	for _, delivery := range contract.Terms().Deliveries {
		if delivery.UnitsRequired-delivery.UnitsFulfilled == 0 {
			continue
		}
		return PlanDeliverySourcing(ctx, delivery, marketRepo, playerID)
	}

	return nil, fmt.Errorf("no unfulfilled deliveries found in contract")
}

// PlanDeliverySourcing costs and picks the cheapest reachable market for ONE
// delivery (see PlanSourcing for the weighting). Exposed separately so the
// worker's profitability evaluation can price each delivery of a multi-delivery
// contract at its own chosen market.
func PlanDeliverySourcing(
	ctx context.Context,
	delivery domainContract.Delivery,
	marketRepo market.MarketRepository,
	playerID int,
) (*SourcingPlan, error) {
	logger := common.LoggerFromContext(ctx)

	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	deliverySystem := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

	// In-system candidate (the pre-sp-1z2h behavior).
	inSystem, err := marketRepo.FindCheapestMarketSelling(ctx, delivery.TradeSymbol, deliverySystem, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
	}

	best := candidateOrNil(inSystem, unitsRemaining, deliverySystem)

	// Cross-gate candidates, when the repository supports the wider scan.
	if finder, ok := marketRepo.(CrossSystemMarketFinder); ok {
		all, err := finder.FindCheapestMarketsSellingAllSystems(ctx, delivery.TradeSymbol, playerID, crossSystemCandidateLimit)
		if err != nil {
			// The wider scan failing must not break sourcing — fall back to
			// the in-system candidate and say so.
			logger.Log("WARNING", fmt.Sprintf(
				"Cross-system market scan for %s failed - sourcing candidates limited to delivery system %s: %v",
				delivery.TradeSymbol, deliverySystem, err), nil)
		} else {
			seenSystems := map[string]bool{deliverySystem: true}
			for i := range all {
				sys := shared.ExtractSystemSymbol(all[i].WaypointSymbol)
				if seenSystems[sys] {
					continue // rows are cheapest-first; first row per system wins
				}
				seenSystems[sys] = true
				c := candidateOrNil(&all[i], unitsRemaining, deliverySystem)
				if c != nil && (best == nil || c.EffectiveCost < best.EffectiveCost) {
					best = c
				}
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no market found selling %s in system %s", delivery.TradeSymbol, deliverySystem)
	}

	logger.Log("INFO", fmt.Sprintf(
		"Sourcing plan for %s: %d units @ %d ask at %s (goods %d + travel %d = effective %d, cross-system=%v)",
		best.Good, best.UnitsRemaining, best.UnitAsk, best.Market,
		best.GoodsCost, best.TravelPenalty, best.EffectiveCost, best.CrossSystem,
	), map[string]interface{}{
		"action":         "plan_sourcing",
		"trade_symbol":   best.Good,
		"market":         best.Market,
		"unit_ask":       best.UnitAsk,
		"units":          best.UnitsRemaining,
		"effective_cost": best.EffectiveCost,
		"cross_system":   best.CrossSystem,
	})

	return best, nil
}

// candidateOrNil costs one market candidate into a SourcingPlan, or nil for a
// nil market.
func candidateOrNil(m *market.CheapestMarketResult, units int, deliverySystem string) *SourcingPlan {
	if m == nil {
		return nil
	}
	cross := shared.ExtractSystemSymbol(m.WaypointSymbol) != deliverySystem
	penalty := 0
	if cross {
		penalty = CrossGateSourcingPenalty
	}
	goods := m.SellPrice * units
	return &SourcingPlan{
		Good:           m.TradeSymbol,
		Market:         m.WaypointSymbol,
		UnitAsk:        m.SellPrice,
		UnitsRemaining: units,
		GoodsCost:      goods,
		TravelPenalty:  penalty,
		EffectiveCost:  goods + penalty,
		CrossSystem:    cross,
	}
}

// SourcingDeferDecision is the outcome of projecting a sourcing run against the
// bead's −20%-of-payout line. Exactly one of three shapes:
//   - proceed:            Defer=false, Overridden=false (projection clears the line)
//   - defer (park):       Defer=true                    (negative projection, runway remains)
//   - override (source):  Defer=false, Overridden=true  (negative projection, deadline too close)
type SourcingDeferDecision struct {
	Defer      bool
	Overridden bool

	ProjectedNet int       // payout − plan.EffectiveCost
	Payout       int       // OnAccepted + OnFulfilled
	Threshold    int       // −SourcingDeferThresholdPct% of payout
	Deadline     time.Time // parsed contract deadline (zero when unparseable)
	Runway       time.Duration
}

// EvaluateSourcingDefer projects the sourcing run's net and decides
// proceed/defer/override. Pure — callers supply now — so the decision table is
// directly testable. An unparseable deadline never defers (fail toward
// fulfilling: with no deadline to sequence inside, parking would risk the
// contract for margin, inverting RULINGS #1).
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

	deadline, err := time.Parse(time.RFC3339, contract.Terms().Deadline)
	if err != nil {
		d.Overridden = true // no parseable runway → source anyway, loudly
		return d
	}
	d.Deadline = deadline
	d.Runway = deadline.Sub(now)

	if d.Runway > SourcingDeferSafetyWindow {
		d.Defer = true
		return d
	}

	d.Overridden = true
	return d
}

// DeferMessage renders the Guard-1-style defer line: every number an operator
// needs — projected net, payout, best ask, market — lives in the MESSAGE TEXT,
// not only in structured fields (sp-1z2h acceptance).
func (d SourcingDeferDecision) DeferMessage(plan *SourcingPlan) string {
	return fmt.Sprintf(
		"Sourcing deferred: projected net %d worse than %d (-%d%% of payout %d) - %d units of %s @ best ask %d at %s (travel penalty %d) - parking until asks revert, re-projecting every pass (deadline %s, runway %s; never-skip stands)",
		d.ProjectedNet, d.Threshold, SourcingDeferThresholdPct, d.Payout,
		plan.UnitsRemaining, plan.Good, plan.UnitAsk, plan.Market, plan.TravelPenalty,
		d.Deadline.Format(time.RFC3339), d.Runway.Round(time.Minute),
	)
}

// OverrideMessage renders the deadline-override line with the same numbers in
// the text: the run is knowingly sourcing at a projected loss because the
// deadline is inside the safety window (or unparseable) and never-skip outranks
// margin.
func (d SourcingDeferDecision) OverrideMessage(plan *SourcingPlan) string {
	deadline := "unparseable"
	if !d.Deadline.IsZero() {
		deadline = fmt.Sprintf("%s, runway %s", d.Deadline.Format(time.RFC3339), d.Runway.Round(time.Minute))
	}
	return fmt.Sprintf(
		"Sourcing override: projected net %d worse than %d (-%d%% of payout %d) but deadline inside the %s safety window (%s) - sourcing %d units of %s @ ask %d at %s anyway (never-skip, RULINGS #1)",
		d.ProjectedNet, d.Threshold, SourcingDeferThresholdPct, d.Payout,
		SourcingDeferSafetyWindow, deadline,
		plan.UnitsRemaining, plan.Good, plan.UnitAsk, plan.Market,
	)
}
