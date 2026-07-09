package contract

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Idle-gap arbitrage (sp-1z2h). The contract fleet's dedicated hulls sit
// homed+unclaimed 89% of wall-time (sp-5bmq: 5.9 active hull-hours of 54);
// the pipeline is SERIAL (one contract worker ever), so at most one hull is
// contract-active while the rest park at their hub standby stations. This
// dispatcher harvests that idle time with hub-local ONE-SHOT arb legs run
// through the proven guarded arb-run machinery (sp-p4ua), dispatched by the
// CONTRACT coordinator itself — l7h2 exclusivity stays intact because the arb
// containers claim with the contract fleet's own identity, through the same
// atomic operation-checked ClaimShip every contract worker uses.
//
// WHY NO RECALLABLE LEASE: the brief allowed either a lease-with-instant-recall
// primitive or "an arbing hull isn't counted as available and the claim takes
// the next hull". The second is strictly smaller and gives a hard bound, so it
// wins. The RESERVE rule makes it airtight:
//
//   - The dispatcher never claims a hull while claimable idle hulls ≤
//     ReserveHulls, and it RECOUNTS from the repository before EVERY claim.
//   - Contract claims are serial (one worker at a time), and every contract
//     completion releases a hull back to idle.
//   - Therefore a contract claim always finds ≥ReserveHulls unclaimed, homed
//     hulls: added claim latency from arb = ZERO. The only idle-reducing event
//     inside the recount→claim race window is the coordinator's own claim —
//     which by definition already succeeded, un-delayed. If that race lands,
//     the pool transiently dips below reserve with NO waiting claim, and
//     refills within one hub-local leg (≤ ~8 min at DefaultIdleArbHubRadius)
//     — well inside the ~18 min claim cadence. A recall primitive would add
//     persisted lease state (RULINGS #2), a recall protocol, and mid-leg
//     cargo cleanup to improve on a bound that is already zero.
//
// HUB-LOCAL is physical, not advisory: the leg's BuyAt is the hull's CURRENT
// waypoint (its hub) — the arb run's location guard refuses to buy anywhere
// else — and SellAt must sit within HubRadius in the same system, so a hull is
// never more than one short hop from home. Guards are inherited wholesale from
// the arb run: min-margin (live re-read, fail-closed), max-spend, the
// non-tunable working-capital floor, stranded-cargo=failure. Nothing here
// weakens them (RULINGS #4); this file only DECIDES lanes, it never spends.
//
// The dispatcher itself holds no persisted state: every tick recomputes from
// live discovery, and the launched containers are ordinary recovery-safe
// arb_run rows (rebuilt or cleanly released on daemon restart — RULINGS #2/#3).

// IdleArbConfig parametrizes the dispatcher (RULINGS #5: operational values
// are config, not constants — these flow from the coordinator's persisted
// launch config, with the defaults below when unset).
type IdleArbConfig struct {
	// ReserveHulls is the number of idle dedicated hulls the dispatcher must
	// always leave unclaimed for instant contract claims. The serial pipeline
	// needs at most one hull at a time, so 1 preserves the zero-latency bound.
	ReserveHulls int
	// HubRadius is the maximum in-system distance (distance units) from the
	// hull's current waypoint to the leg's sell market. Bounds both leg
	// duration and how far a hull can drift from its hub.
	HubRadius float64
	// MaxSpendPerLeg caps each leg's buy (the arb run's --max-spend guard).
	MaxSpendPerLeg int
	// MinMarginPerUnit is the per-unit floor handed to the arb run's margin
	// gate (which re-reads live prices and fails closed).
	MinMarginPerUnit int
	// Interval is the dispatch tick.
	Interval time.Duration
}

// Idle-arb defaults. Sizing notes: radius 250 ≈ a 5-8 minute leg at the
// fleet's observed ~35 units/min (sp-5bmq's far-source autopsy), the analyst's
// micro-run shape; spend 100k/leg × ≤5 concurrent legs bounds exposure at
// ~500k against a multi-million treasury, before the arb run's own
// working-capital floor (non-tunable, sp-bp6f) even engages; margin floor 1
// because any positive spread beats a parked hull — the run's live margin
// gate, not this floor, is the capital protection.
const (
	DefaultIdleArbReserveHulls = 1
	DefaultIdleArbHubRadius    = 250.0
	DefaultIdleArbMaxSpend     = 100_000
	DefaultIdleArbMinMargin    = 1
	DefaultIdleArbInterval     = 90 * time.Second
)

// WithDefaults fills zero-valued fields with the package defaults.
func (c IdleArbConfig) WithDefaults() IdleArbConfig {
	if c.ReserveHulls <= 0 {
		c.ReserveHulls = DefaultIdleArbReserveHulls
	}
	if c.HubRadius <= 0 {
		c.HubRadius = DefaultIdleArbHubRadius
	}
	if c.MaxSpendPerLeg <= 0 {
		c.MaxSpendPerLeg = DefaultIdleArbMaxSpend
	}
	if c.MinMarginPerUnit <= 0 {
		c.MinMarginPerUnit = DefaultIdleArbMinMargin
	}
	if c.Interval <= 0 {
		c.Interval = DefaultIdleArbInterval
	}
	return c
}

// IdleArbSpec is one hub-local leg the dispatcher wants flown: the arb-run
// launch parameters plus the claim identity (Operation) the container must use
// so the atomic ClaimShip dedication check passes for the contract fleet's own
// hulls — and keeps rejecting everyone else's.
type IdleArbSpec struct {
	ShipSymbol string
	Good       string
	BuyAt      string // the hull's CURRENT waypoint (arb location guard enforces this)
	SellAt     string
	MaxSpend   int
	MinMargin  int
	PlayerID   int
	Operation  string // claim identity, e.g. "contract" (l7h2)
}

// IdleArbLauncher starts one recovery-safe, guarded one-shot arb container and
// confirms the hull is CLAIMED (atomically, operation-checked) before
// returning. Implemented by the daemon server; the dispatcher stays a pure
// decision loop (RULINGS #3: new operations are daemon containers, and the
// daemon remains the single writer of ship state).
type IdleArbLauncher interface {
	LaunchIdleArb(ctx context.Context, spec IdleArbSpec) (containerID string, err error)
}

// IdleArbLane is a scored hub-local lane candidate.
type IdleArbLane struct {
	Good          string
	SellAt        string
	MarginPerUnit int
	Distance      float64
	SourceAsk     int
	DestBid       int
}

// IdleArbDispatcher runs the idle-gap harvest for one coordinator's dedicated
// fleet.
type IdleArbDispatcher struct {
	shipRepo      navigation.ShipRepository
	marketRepo    market.MarketRepository
	graphProvider system.ISystemGraphProvider
	launcher      IdleArbLauncher
	clock         shared.Clock
	playerID      shared.PlayerID
	fleet         string
	cfg           IdleArbConfig
}

// NewIdleArbDispatcher wires a dispatcher for the given dedicated fleet.
func NewIdleArbDispatcher(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	graphProvider system.ISystemGraphProvider,
	launcher IdleArbLauncher,
	clock shared.Clock,
	playerID shared.PlayerID,
	fleet string,
	cfg IdleArbConfig,
) *IdleArbDispatcher {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &IdleArbDispatcher{
		shipRepo:      shipRepo,
		marketRepo:    marketRepo,
		graphProvider: graphProvider,
		launcher:      launcher,
		clock:         clock,
		playerID:      playerID,
		fleet:         fleet,
		cfg:           cfg.WithDefaults(),
	}
}

// Run ticks DispatchOnce every Interval until ctx is cancelled. Started as a
// goroutine by the fleet coordinator's Handle; the coordinator's own context
// bounds its life, so a stopped coordinator stops the harvest with it.
func (d *IdleArbDispatcher) Run(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf(
		"Idle-gap arb dispatcher running: fleet %q, reserve %d hull(s), hub radius %.0f, max spend %d/leg, min margin %d/unit, tick %s",
		d.fleet, d.cfg.ReserveHulls, d.cfg.HubRadius, d.cfg.MaxSpendPerLeg, d.cfg.MinMarginPerUnit, d.cfg.Interval,
	), nil)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.cfg.Interval):
		}
		d.DispatchOnce(ctx)
	}
}

// DispatchOnce runs one dispatch pass and returns how many legs it launched.
// Exported so the zero-missed-claims simulation can drive it deterministically.
func (d *IdleArbDispatcher) DispatchOnce(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	launched := 0
	// tried tracks hulls already handled this pass (launched, or skipped for
	// want of a lane) so the recount loop below always terminates. A skipped
	// hull stays idle and keeps padding the reserve — conservative.
	tried := map[string]bool{}

	for {
		// RECOUNT before every claim: the reserve check must see the
		// repository's current truth, not this pass's opening snapshot —
		// this is what shrinks the race window with the coordinator's own
		// claims to the recount→claim gap.
		idleShips, _, err := FindIdleShipsByFleet(ctx, d.playerID, d.shipRepo, d.fleet)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Idle-arb dispatch: idle discovery failed, skipping pass: %v", err), nil)
			return launched
		}

		var candidates []*navigation.Ship
		for _, s := range idleShips {
			if !tried[s.ShipSymbol()] {
				candidates = append(candidates, s)
			}
		}

		// The reserve is judged against ALL idle hulls (tried-but-skipped ones
		// still count — they are genuinely claimable by a contract), but only
		// untried hulls are dispatchable. Hulls launched earlier this pass are
		// already claimed by their containers, so the recount above has
		// excluded them — no separate subtraction needed.
		if len(idleShips) <= d.cfg.ReserveHulls || len(candidates) == 0 {
			return launched
		}

		hull := candidates[0]
		tried[hull.ShipSymbol()] = true

		lane := d.pickHubLocalLane(ctx, hull)
		if lane == nil {
			continue // no profitable local lane from this hull's hub right now
		}

		spec := IdleArbSpec{
			ShipSymbol: hull.ShipSymbol(),
			Good:       lane.Good,
			BuyAt:      hull.CurrentLocation().Symbol,
			SellAt:     lane.SellAt,
			MaxSpend:   d.cfg.MaxSpendPerLeg,
			MinMargin:  d.cfg.MinMarginPerUnit,
			PlayerID:   d.playerID.Value(),
			Operation:  d.fleet,
		}
		containerID, err := d.launcher.LaunchIdleArb(ctx, spec)
		if err != nil {
			// Losing the claim race (the coordinator took this hull for a
			// contract between recount and claim) is the system WORKING —
			// contract claims outrank arb. Log and move on.
			logger.Log("INFO", fmt.Sprintf(
				"Idle-arb dispatch: launch for %s declined (%v) - hull skipped this pass", hull.ShipSymbol(), err), nil)
			continue
		}
		launched++

		logger.Log("INFO", fmt.Sprintf(
			"Idle-gap arb leg launched: %s flies %s %s->%s (margin %d/unit = bid %d - ask %d, distance %.0f, max spend %d) in container %s",
			hull.ShipSymbol(), lane.Good, spec.BuyAt, lane.SellAt,
			lane.MarginPerUnit, lane.DestBid, lane.SourceAsk, lane.Distance, spec.MaxSpend, containerID,
		), map[string]interface{}{
			"action":       "idle_arb_launched",
			"ship_symbol":  hull.ShipSymbol(),
			"good":         lane.Good,
			"buy_at":       spec.BuyAt,
			"sell_at":      lane.SellAt,
			"margin":       lane.MarginPerUnit,
			"distance":     lane.Distance,
			"container_id": containerID,
		})
	}
}

// pickHubLocalLane scores every (good, sell-market) pair reachable from the
// hull's CURRENT waypoint within HubRadius and returns the best positive-
// margin lane, or nil when the hub offers none right now. Prices are the
// scanned cache — deliberately: the arb run itself live-refreshes the source
// and re-gates the margin fail-closed before any credit moves, so a stale
// pick here costs at worst a wasted (refused) leg, never money.
func (d *IdleArbDispatcher) pickHubLocalLane(ctx context.Context, hull *navigation.Ship) *IdleArbLane {
	logger := common.LoggerFromContext(ctx)
	origin := hull.CurrentLocation()
	if origin == nil {
		return nil
	}

	hubMarket, err := d.marketRepo.GetMarketData(ctx, origin.Symbol, d.playerID.Value())
	if err != nil || hubMarket == nil || hubMarket.GoodsCount() == 0 {
		return nil // the hub standby station isn't a scanned market — nothing to fly
	}

	systemSymbol := shared.ExtractSystemSymbol(origin.Symbol)
	graphResult, err := d.graphProvider.GetGraph(ctx, systemSymbol, false, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: no system graph for %s: %v", systemSymbol, err), nil)
		return nil
	}

	marketWaypoints, err := d.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, d.playerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb lane pick: market listing failed for %s: %v", systemSymbol, err), nil)
		return nil
	}

	var best *IdleArbLane
	bestScore := 0
	for _, wp := range marketWaypoints {
		if wp == origin.Symbol {
			continue
		}
		coord, ok := graphResult.Graph.Waypoints[wp]
		if !ok {
			continue
		}
		distance := origin.DistanceTo(coord)
		if distance > d.cfg.HubRadius {
			continue // hub-LOCAL only: the hull must stay a short hop from home
		}

		destMarket, err := d.marketRepo.GetMarketData(ctx, wp, d.playerID.Value())
		if err != nil || destMarket == nil {
			continue
		}

		for _, hubGood := range hubMarket.TradeGoods() {
			ask := hubGood.SellPrice() // what the hull pays at the hub
			if ask <= 0 {
				continue
			}
			destGood := destMarket.FindGood(hubGood.Symbol())
			if destGood == nil {
				continue
			}
			bid := destGood.PurchasePrice() // what the hull receives at the destination
			margin := bid - ask
			if margin < d.cfg.MinMarginPerUnit {
				continue
			}
			// Expected-gross score: per-unit margin × the tranche the leg can
			// actually carry/afford, so a fat-margin good the spend cap allows
			// two of doesn't outrank a solid lane with real volume.
			units := hull.AvailableCargoSpace()
			if affordable := d.cfg.MaxSpendPerLeg / ask; affordable < units {
				units = affordable
			}
			if units <= 0 {
				continue
			}
			score := margin * units
			if best == nil || score > bestScore {
				best = &IdleArbLane{
					Good:          hubGood.Symbol(),
					SellAt:        wp,
					MarginPerUnit: margin,
					Distance:      distance,
					SourceAsk:     ask,
					DestBid:       bid,
				}
				bestScore = score
			}
		}
	}

	return best
}
