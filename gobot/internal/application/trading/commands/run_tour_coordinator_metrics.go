package commands

import (
	"context"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Burn-in instrumentation for the absorption ledger (sp-8cz9): two counters the L5/xmwn
// mutex-retirement ruling and the Admiral's heavy-#9 gate consume. Both are pure
// OBSERVATION — every function here derives from data the plan/execution ALREADY read
// (the netted absorption view, the plan, the snapshot) and emits through the nil-safe
// metrics globals, so a metrics miss can never alter a decision path (RULINGS #4).

// shadowSinkKey identifies a (waypoint, good) market. It doubles as the trade-volume
// lookup key and the ladder-incident probe set: a market carrying an outstanding EXECUTED
// recovery shadow at plan time is one the tour must not re-buy into (sp-8cz9 P2).
type shadowSinkKey struct {
	waypoint string
	good     string
}

// capBindingSample is one (side, outcome) classification for a touched, absorbed lane —
// the label pair emitted to absorption_cap_binding_total (sp-8cz9 P1).
type capBindingSample struct {
	side    string
	outcome string
}

// classifyCapBinding infers, per (waypoint, good, side) the accepted plan touches, whether
// the fleet-wide absorption cap CONSTRAINED the plan there (sp-8cz9 P1). It answers the
// decision "what fraction of profitable plans does the cap actually bind": a lane is
// scored only when it carries outstanding cross-container absorption (PLANNED + the
// decayed EXECUTED residual the netting fed the solver); it is bound when the plan's units
// there reached the netted availability ceiling (cap = tourACapTranches x trade_volume,
// less the outstanding depth), unbound when it stayed below. Lanes with no absorption are
// not scored — the cap could not have constrained them. DEPOSIT tranches are skipped
// (synthetic warehouse sink, no market depth), mirroring buildTourReserveEntries. This is
// a pure function over data the plan already produced; it reads no ledger and no proto.
func classifyCapBinding(
	plan *routing.TourPlan,
	snapshot []routing.TourGoodSnapshot,
	absorptionView []routing.TourMarketAbsorption,
) []capBindingSample {
	if plan == nil {
		return nil
	}

	tradeVolume := make(map[shadowSinkKey]int, len(snapshot))
	for _, s := range snapshot {
		tradeVolume[shadowSinkKey{s.Waypoint, s.Good}] = s.TradeVolume
	}

	type lane struct{ wp, good, side string }
	outstanding := make(map[lane]int, len(absorptionView))
	for _, a := range absorptionView {
		// PLANNED units are exact; the EXECUTED residual is a decayed float — round to the
		// nearest unit so a fractional shadow still counts as outstanding depth.
		outstanding[lane{a.Waypoint, a.Good, a.Side}] += a.PlannedUnits + int(math.Round(a.RecoveringUnits))
	}

	planned := map[lane]int{}
	order := make([]lane, 0)
	for _, leg := range plan.Legs {
		for _, tr := range leg.Trades {
			if tr.IsDeposit {
				continue // synthetic haul-to-storage sink: no market depth to cap
			}
			side := absorption.SideSell
			if tr.IsBuy {
				side = absorption.SideBuy
			}
			k := lane{leg.Waypoint, tr.Good, side}
			if _, seen := planned[k]; !seen {
				order = append(order, k)
			}
			planned[k] += tr.Units
		}
	}

	samples := make([]capBindingSample, 0, len(order))
	for _, k := range order {
		units := planned[k]
		if units <= 0 {
			continue // not actually touched
		}
		depth := outstanding[k]
		if depth <= 0 {
			continue // no absorption here — the cap cannot have constrained the plan
		}
		// Netted availability ceiling: the fleet-wide cap less others' outstanding depth
		// (this container's own PLANNED rows were released before netting). A missing
		// snapshot row leaves trade_volume 0 -> ceiling 0 -> any units read as bound, the
		// defensive worst case for a lane we know carries real absorption.
		ceiling := tourACapTranches*tradeVolume[shadowSinkKey{k.wp, k.good}] - depth
		if ceiling < 0 {
			ceiling = 0
		}
		outcome := "unbound"
		if units >= ceiling {
			outcome = "bound"
		}
		samples = append(samples, capBindingSample{side: k.side, outcome: outcome})
	}
	return samples
}

// shadowSinksFromAbsorption builds the set of (waypoint, good) markets carrying an
// outstanding EXECUTED recovery shadow (SideSell, decayed residual still above the floor)
// from the netted absorption view — the buy-time ladder probe reads it (sp-8cz9 P2).
// Recovery shadows are only ever written on the sell side (a dump crushes the bid), so a
// BUY landing on such a market is the fleet re-buying into ground still recovering. Nil
// when no shadows exist; nil-map reads are false, so the probe is inert then.
func shadowSinksFromAbsorption(absorptionView []routing.TourMarketAbsorption) map[shadowSinkKey]bool {
	var sinks map[shadowSinkKey]bool
	for _, a := range absorptionView {
		if a.Side == absorption.SideSell && a.RecoveringUnits > 0 {
			if sinks == nil {
				sinks = map[shadowSinkKey]bool{}
			}
			sinks[shadowSinkKey{a.Waypoint, a.Good}] = true
		}
	}
	return sinks
}

// recordCapBinding emits the cap-binding classification for an accepted plan (sp-8cz9 P1).
// Best-effort: the metrics globals no-op when the collector is unset, and this runs after
// the plan is already accepted, so it can never gate a trade.
func (h *RunTourCoordinatorHandler) recordCapBinding(
	_ context.Context,
	cmd *RunTourCoordinatorCommand,
	plan *routing.TourPlan,
	snapshot []routing.TourGoodSnapshot,
	absorptionView []routing.TourMarketAbsorption,
) {
	for _, s := range classifyCapBinding(plan, snapshot, absorptionView) {
		metrics.RecordAbsorptionCapBinding(cmd.PlayerID, s.side, s.outcome)
	}
}
