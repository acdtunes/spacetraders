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
//   1. Cheapest REACHABLE market: cross-gate export markets are candidates ONLY
//      when the caller proves the consumer can route there (the
//      SystemReachability predicate). Contract sourcing is SINGLE-SYSTEM by
//      standing ruling (RULINGS #14, origin sp-9hu8: the serial one-active-
//      contract clock can't afford cross-gate round trips — cross-system
//      logistics belongs to the parallel trade engine, not the contract worker),
//      so both contract call sites pass in-system-only and a cross-gate source
//      can never be selected for a contract — an unreachable source is
//      UNSELECTABLE, not dispatched then crashed ('waypoint not found in cache').
//      Weighting is documented on CrossGateSourcingPenalty below.
//   2. Negative-margin sourcing (formerly DEFER-while-negative): when a
//      sourcing run's projected net is worse than −SourcingDeferThresholdPct of
//      the payout, the contract path now SOURCES ANYWAY and logs the loss (the
//      Overridden decision) — it NEVER parks. RULINGS #1 never-skip governs the
//      contract path over the profit guard (explicit Admiral override, sp-x8ck);
//      the old park-until-asks-revert behavior deadlocked the serial one-active-
//      contract pipeline for the whole deadline window whenever a contract good
//      had no in-system source at a profit (live: 6 ANTIMATTER, sole in-system
//      market an IMPORT priced above payout). The SourcingDeferSafetyWindow /
//      SourcingDeferRecheckInterval / DeferMessage machinery below is retained
//      but inert on the contract path (the sole caller); safe to prune later.
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

// SystemReachability reports whether a candidate source SYSTEM can be reached by
// the consumer of the plan. Only CROSS-system candidates are gated by it — the
// in-system candidate (the delivery system, which the consumer must reach to
// deliver at all) is always kept. A nil SystemReachability means IN-SYSTEM ONLY:
// the cross-system scan is skipped entirely and no cross-gate source can be
// selected. That is the contract worker's mode (sp-9hu8) — its trip is in-system
// NavigateAndDock/NavigateRouteCommand with zero jump capability, so a
// cross-system source is unreachable and must be UNSELECTABLE, not
// dispatched-then-crashed ('waypoint not found in cache'). Contract sourcing is
// single-system by standing ruling (RULINGS #14), so both contract call sites
// pass nil permanently; the predicate parameter exists so a jump-capable,
// non-contract consumer could gate cross-system candidates by GateGraph
// routability (fail closed on unknown systems). nil also fails closed: a caller
// that forgets to pass a predicate gets in-system-only, never an unreachable
// pick.
type SystemReachability func(system string) bool

// SourcingPlan is the chosen sourcing decision for a contract's first
// unfulfilled delivery: where to buy, at what cached ask, and what the run is
// projected to cost including the travel penalty term.
type SourcingPlan struct {
	Good           string
	Market         string // waypoint symbol of the chosen source (market OR storage waypoint for INVENTORY)
	UnitAsk        int    // cached ask at the chosen market; 0 for INVENTORY (sunk cost)
	UnitsRemaining int    // units still to source for the delivery
	GoodsCost      int    // UnitAsk × UnitsRemaining; 0 for INVENTORY
	TravelPenalty  int    // 0 in-system; CrossGateSourcingPenalty cross-gate
	EffectiveCost  int    // GoodsCost + TravelPenalty — the defer projection basis
	CrossSystem    bool   // true when the chosen market is outside the delivery system

	// Source distinguishes a MARKET buy from an INVENTORY withdrawal (sp-dchv
	// Lane D). Defaults to SourceMarket, so every pre-existing plan is a market
	// plan and existing behavior is unchanged.
	Source SourcingSource

	// StorageOperationID is the warehouse operation to withdraw from when
	// Source == SourceInventory (empty otherwise). It is informational for the
	// projection/logging path; the executor re-consults the finder at withdrawal
	// time for the freshest reservation (fail-open, RULINGS #1).
	StorageOperationID string
}

// PlanSourcing picks the cheapest REACHABLE market for the contract's first
// unfulfilled delivery and returns the costed plan. Candidates are the cheapest
// market inside the delivery system plus, when the caller supplies a
// SystemReachability predicate AND the repository supports the wider scan, the
// cheapest reachable market in each other scanned system; each candidate is
// weighed as
//
//	effective cost = units × ask + travel penalty
//
// (see CrossGateSourcingPenalty for why the penalty is flat). Ties go to the
// in-system candidate. A nil reachable scopes selection to the delivery system —
// contract sourcing is single-system by ruling (RULINGS #14, origin sp-9hu8). An
// error means no REACHABLE market sells the good yet — callers treat that exactly
// like the old FindPurchaseMarket miss (wait for scouts / re-project next pass),
// never a skip (RULINGS #1).
func PlanSourcing(
	ctx context.Context,
	contract *domainContract.Contract,
	marketRepo market.MarketRepository,
	playerID int,
	reachable SystemReachability,
	opts ...SourcingOption,
) (*SourcingPlan, error) {
	for _, delivery := range contract.Terms().Deliveries {
		if delivery.UnitsRequired-delivery.UnitsFulfilled == 0 {
			continue
		}
		return PlanDeliverySourcing(ctx, delivery, marketRepo, playerID, reachable, opts...)
	}

	return nil, fmt.Errorf("no unfulfilled deliveries found in contract")
}

// PlanDeliverySourcing costs and picks the cheapest reachable market for ONE
// delivery (see PlanSourcing for the weighting). Exposed separately so the
// worker's profitability evaluation can price each delivery of a multi-delivery
// contract at its own chosen market. A nil reachable scopes candidates to the
// delivery system — the worker's in-system-only reality (sp-9hu8) — so the
// projection here matches what the executor can actually fly.
func PlanDeliverySourcing(
	ctx context.Context,
	delivery domainContract.Delivery,
	marketRepo market.MarketRepository,
	playerID int,
	reachable SystemReachability,
	opts ...SourcingOption,
) (*SourcingPlan, error) {
	logger := common.LoggerFromContext(ctx)
	cfg := newSourcingConfig(opts)

	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	deliverySystem := shared.ExtractSystemSymbol(delivery.DestinationSymbol)

	// INVENTORY-FIRST (sp-dchv Lane D): before any market candidate, consult the
	// warehouse. Stock of this good in the DELIVERY system is a zero-ask source —
	// the units are already paid for (deposit sunk), so the projection treats
	// them as free, which is economically correct for the contract engine's
	// park/proceed decision (a stocked contract is never in the runaway-ask class
	// the defer gate exists to park). Fail-open (RULINGS #1): a nil finder, no
	// stock, or any read error inside the finder yields nil here, and sourcing
	// falls through to the market candidate below byte-identical — inventory only
	// ever ADDS a cheaper source, it never skips or parks a contract. Withdrawal
	// is single-system by construction: the finder only returns warehouses in
	// deliverySystem (RULINGS #14).
	if cfg.inventory != nil {
		if src := cfg.inventory.FindInSystemInventory(ctx, playerID, deliverySystem, delivery.TradeSymbol); src != nil && src.UnitsAvailable > 0 {
			plan := &SourcingPlan{
				Good:               delivery.TradeSymbol,
				Market:             src.StorageWaypoint,
				UnitAsk:            0,
				UnitsRemaining:     unitsRemaining,
				GoodsCost:          0,
				TravelPenalty:      0,
				EffectiveCost:      0,
				CrossSystem:        false,
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

	// In-system candidate: always reachable (the consumer must reach the
	// delivery system to deliver at all), so it is never gated by reachable.
	inSystem, err := marketRepo.FindCheapestMarketSelling(ctx, delivery.TradeSymbol, deliverySystem, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market for %s: %w", delivery.TradeSymbol, err)
	}

	best := candidateOrNil(inSystem, unitsRemaining, deliverySystem)

	// Cross-gate candidates, only when the caller proves the consumer can route
	// cross-system (nil reachable ⇒ in-system only: the worker cannot jump yet,
	// sp-9hu8) AND the repository supports the wider scan. An unreachable source
	// is never considered — it must be UNSELECTABLE, not dispatched then crashed
	// ('waypoint not found in cache for system ...').
	if reachable != nil {
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
					if !reachable(sys) {
						continue // unreachable source — never selectable (sp-9hu8)
					}
					c := candidateOrNil(&all[i], unitsRemaining, deliverySystem)
					if c != nil && (best == nil || c.EffectiveCost < best.EffectiveCost) {
						best = c
					}
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
		Source:         SourceMarket,
	}
}

// SourcingDeferDecision is the outcome of projecting a sourcing run against the
// −SourcingDeferThresholdPct-of-payout line. On the contract sourcing path it is
// one of just two shapes — it NEVER parks (sp-x8ck Admiral override; RULINGS #1
// never-skip governs the contract path over the profit guard):
//   - proceed:                     Defer=false, Overridden=false (projection clears the line)
//   - override (source-at-a-loss): Defer=false, Overridden=true  (negative projection — source anyway, loudly)
//
// Defer (park) is the historical third shape. EvaluateSourcingDefer never raises
// it for a contract; the field is retained so the never-park invariant stays
// directly assertable (TestSourcing_UnsourceableAtProfit_StillSources) and so the
// coordinator's legacy Defer branch is provably inert.
type SourcingDeferDecision struct {
	Defer      bool // never raised on the contract path (sp-x8ck) — see type doc
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
// testable. It NEVER parks a contract: after the sp-x8ck Admiral override,
// RULINGS #1 (never-skip) governs the contract sourcing path over the profit
// guard, so a negative projection is flagged Overridden (source anyway, log the
// loss) and Defer is never raised. The old defer/park branch deadlocked the
// serial one-active-contract pipeline for the whole deadline window whenever a
// contract good had no in-system source at a profit.
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
	// path over this profit guard (explicit Admiral override for the contract
	// path only, sp-x8ck): an accepted contract is ALWAYS sourced+delivered+
	// fulfilled, even at a projected loss. The historical DEFER/park branch here
	// deadlocked the serial pipeline FOREVER whenever a contract good had no
	// in-system source at a profit (live: 6 ANTIMATTER whose only in-system
	// market was an IMPORT priced above payout), which is the exact never-skip
	// violation this override removes. So we never park — flag the run Overridden
	// (source at the loss, log it loudly) and proceed. The deadline is parsed
	// only to enrich the log.
	if deadline, err := time.Parse(time.RFC3339, contract.Terms().Deadline); err == nil {
		d.Deadline = deadline
		d.Runway = deadline.Sub(now)
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

// OverrideMessage renders the negative-margin sourcing line with the same
// numbers in the text: the run is knowingly sourcing at a projected loss because
// never-skip (RULINGS #1) governs the contract path over the profit guard — a
// contract always sources+delivers+fulfills regardless of margin (sp-x8ck). The
// deadline is shown for context only; it is not the reason the run proceeds.
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
