package contract

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// idleArbSkipReason names why a hull's would-be leg was refused at dispatch, so
// the skip counters can attribute pressure by cause.
type idleArbSkipReason int

const (
	skipNone idleArbSkipReason = iota
	skipReasonBlacklist
	skipReasonContractGood
	skipReasonLeash
	skipReasonLaneHeld
	// skipReasonReserved: the (good, sink) is occupied in the cross-engine
	// absorption ledger — a PLANNED leg (any engine) is in flight there, or a
	// recovering EXECUTED shadow still blocks above its floor. This is the
	// lane-mutex's guarantee generalized CROSS-ENGINE and across a restart: a tour or
	// another dispatcher's leg the in-memory mutex cannot see is caught here.
	skipReasonReserved
	// skipReasonUnprofitable: at current LIVE prices the lane's per-unit net
	// (spread − fuel) is below the binding profitability floor. This is the per-trip
	// money guard that stops the fleet's own self-inflated thin lanes from flying
	// net-negative; a below-floor lane auto-re-enters when its price recovers.
	skipReasonUnprofitable
)

// String names the skip reason for the per-candidate verdict line. It
// mirrors the reason names the harvest-summary counters already use, so a
// candidate's "skipped:<reason>" reads consistently with the cumulative totals.
func (r idleArbSkipReason) String() string {
	switch r {
	case skipReasonBlacklist:
		return "blacklist"
	case skipReasonContractGood:
		return "contract-good"
	case skipReasonLeash:
		return "leash"
	case skipReasonLaneHeld:
		return "lane-held"
	case skipReasonReserved:
		return "reserved"
	case skipReasonUnprofitable:
		return "unprofitable"
	default:
		return "none"
	}
}

// logLevel is INFO for a verdict a hull's price/position alone can't explain —
// an explicit exclusion or contention over a (good, sink) with another
// engine's leg. Routine per-trip economics and an eligible-but-unchosen
// candidate (its winner logs its own INFO line once launched) are DEBUG.
func (r idleArbSkipReason) logLevel() string {
	switch r {
	case skipReasonBlacklist, skipReasonContractGood, skipReasonLaneHeld, skipReasonReserved:
		return "INFO"
	default: // skipNone (eligible), skipReasonLeash, skipReasonUnprofitable
		return "DEBUG"
	}
}

// logCandidate emits one terse per-candidate verdict line in MESSAGE TEXT
// (the CLI renderer drops metadata maps). It carries every
// value the leash decision turned on so a masked mis-pick is impossible to hide:
// the good, the buy/sell waypoints WITH the coordinates the distance was measured
// between, that computed distance against the live leash/hub radii, the projected
// CRUISE leg-time against the cap, the quoted margin (bid−ask), the per-unit net
// after fuel against the binding profitability floor (so a
// skipped:unprofitable verdict shows the numbers that refused it), the buy/sell
// market read ages, and the verdict (eligible, or skipped:<reason>). This is the
// surface an all-pairs analyst scan is diffed against. It is LOG-ONLY: it reads
// no new state and changes no pick, threshold, or guard (RULINGS #4).
// laneCandidate is one hub->sink lane under evaluation: the quoted lane, the
// hull that would fly it, the endpoints and market rows it was priced from, and
// the sink depth plus tranche the absorption consult weighs.
type laneCandidate struct {
	hull         *navigation.Ship
	lane         *IdleArbLane
	buy          *shared.Waypoint
	sell         *shared.Waypoint
	buyMarket    *market.Market
	sellMarket   *market.Market
	sinkDepthCap int
	legUnits     int
}

func (d *IdleArbDispatcher) logCandidate(ctx context.Context, c laneCandidate, reason idleArbSkipReason) {
	verdict := "eligible"
	if reason != skipNone {
		verdict = "skipped:" + reason.String()
		// For a lane-held verdict, name the holding hull and (once its leg
		// has terminated) when the lane frees, so a collision the mutex prevented is
		// legible in the same candidate line the analyst scan is diffed against.
		if reason == skipReasonLaneHeld {
			if holder, freesAt, flying := d.lanes.describe(laneKey{good: c.lane.Good, sink: c.lane.SellAt}); holder != "" {
				if flying {
					verdict += fmt.Sprintf(" (%s flying)", holder)
				} else {
					verdict += fmt.Sprintf(" (%s dumped, frees ~%s)", holder, freesAt.Format("15:04:05"))
				}
			}
		}
	}
	now := d.clock.Now()
	legSeconds := shared.FlightModeCruise.TravelTime(c.lane.Distance, c.hull.EngineSpeed())
	logger := common.LoggerFromContext(ctx)
	logger.Log(reason.logLevel(), fmt.Sprintf(
		"Idle-arb candidate: %s %s buy@%s(%.0f,%.0f) -> sell@%s(%.0f,%.0f) dist %.0fu (leash %.0f, hub %.0f), leg %ds (cap %s), margin %d/u (bid %d - ask %d), net %d/u after fuel %d (floor %d), age buy %s/sell %s, verdict %s",
		c.hull.ShipSymbol(), c.lane.Good,
		c.buy.Symbol, c.buy.X, c.buy.Y, c.sell.Symbol, c.sell.X, c.sell.Y,
		c.lane.Distance, d.cfg.LeashRadius, d.cfg.HubRadius,
		legSeconds, d.cfg.MaxLegDuration,
		c.lane.MarginPerUnit, c.lane.DestBid, c.lane.SourceAsk,
		d.laneNetPerUnit(c.lane.SourceAsk, c.lane.DestBid), d.cfg.FuelCostPerUnit, d.netProfitFloor(c.lane.SourceAsk),
		now.Sub(c.buyMarket.LastUpdated()).Round(time.Second), now.Sub(c.sellMarket.LastUpdated()).Round(time.Second),
		verdict,
	), nil)
}

// laneSkipReason applies the dispatch-time exclusions to one (good, market)
// candidate and returns the FIRST reason it is refused, or skipNone if it may
// fly. Order: blacklist → open-contract good → leash (the LeashRadius bound,
// then the projected CRUISE leg-time from the hull's engine speed against
// MaxLegDuration). None weakens the pre-existing HubRadius filter; each only
// tightens (RULINGS #4).
func (d *IdleArbDispatcher) laneSkipReason(c laneCandidate, excludedContractGoods map[string]struct{}, consult absorptionConsult) idleArbSkipReason {
	if d.isBlacklisted(c.lane.Good) {
		return skipReasonBlacklist
	}
	if _, ok := excludedContractGoods[c.lane.Good]; ok {
		return skipReasonContractGood
	}
	if c.lane.Distance > d.cfg.LeashRadius {
		return skipReasonLeash
	}
	legSeconds := shared.FlightModeCruise.TravelTime(c.lane.Distance, c.hull.EngineSpeed())
	if time.Duration(legSeconds)*time.Second > d.cfg.MaxLegDuration {
		return skipReasonLeash
	}
	// LANE MUTEX: a (good, sink) already worked by a live or still-
	// recovering leg — including one launched earlier THIS pass — is held. Checked
	// before the ledger consult so a sink THIS dispatcher already holds keeps
	// reporting lane-held (existing behavior), and because pickHubLocalLane scans ALL
	// (good, sink) pairs and keeps the best NON-skipped one, a held best lane makes
	// the hull fall back to its next-best unheld sink rather than collide.
	if d.lanes.held(laneKey{good: c.lane.Good, sink: c.lane.SellAt}) {
		return skipReasonLaneHeld
	}
	// ABSORPTION LEDGER (depth-aware): a sink the in-memory mutex
	// does NOT hold but another engine has reserved in flight (only blocking when the
	// remaining depth can't fit this leg's tranche), or a recovering shadow still
	// blocks — the cross-engine / cross-restart generalization of the mutex, closed
	// here from the market-absorption side.
	if consult.reserved(c.lane.Good, c.lane.SellAt, c.sinkDepthCap, c.legUnits) {
		metrics.RecordAbsorptionConsultVerdict(d.playerID.Value(), "skip_reserved", absorptionEngineIdleArb)
		return skipReasonReserved
	}
	metrics.RecordAbsorptionConsultVerdict(d.playerID.Value(), "pass", absorptionEngineIdleArb)
	return skipNone
}

// recordSkip increments the cumulative counter for a dispatch-time guard skip
// and reports whether it was one. skipNone is not a skip — the hull
// simply had no profitable local lane this tick.
func (d *IdleArbDispatcher) recordSkip(reason idleArbSkipReason) bool {
	switch reason {
	case skipReasonBlacklist:
		d.skipBlacklist++
	case skipReasonContractGood:
		d.skipContractGood++
	case skipReasonLeash:
		d.skipLeash++
	case skipReasonLaneHeld:
		d.skipLaneHeld++
	case skipReasonReserved:
		d.skipReserved++
	case skipReasonUnprofitable:
		d.skipUnprofitable++
	default:
		return false
	}
	return true
}

// logHarvestSummary emits the harvest observability line in MESSAGE TEXT (not a
// metadata map — the CLI renderer drops metadata), carrying the attempt rate and
// the cumulative per-reason skip counts. Margin-aborts are a POST-launch refusal
// the arb run logs per-leg in its own message text ("... aborting before buy");
// they are not summed here because the dispatcher's launch is fire-and-forget
// and never observes the run's outcome. Emitted only when the pass did
// something, to keep idle ticks quiet.
func (d *IdleArbDispatcher) logHarvestSummary(ctx context.Context, launchedThisPass, skipsThisPass, rehomedThisPass int) {
	if launchedThisPass == 0 && skipsThisPass == 0 && rehomedThisPass == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	rate := 0.0
	if elapsed := d.clock.Now().Sub(d.startTime).Hours(); elapsed > 0 {
		rate = float64(d.attempts) / elapsed
	}
	logger.Log("INFO", fmt.Sprintf(
		"Idle-arb harvest: %d leg(s) launched this pass; %d hull(s) re-homed this pass; %d attempt(s) total at %.1f/hr; "+
			"skipped legs - blacklist %d, contract-good %d, leash %d, lane-held %d, reserved %d, unprofitable %d; reserve-floor holds %d; re-homed %d total (cumulative; margin-aborts logged per-leg by the arb run)",
		launchedThisPass, rehomedThisPass, d.attempts, rate,
		d.skipBlacklist, d.skipContractGood, d.skipLeash, d.skipLaneHeld, d.skipReserved, d.skipUnprofitable, d.heldReserveFloor, d.rehomed,
	), nil)
}
