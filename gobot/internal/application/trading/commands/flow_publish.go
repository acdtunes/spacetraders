// gobot/internal/application/trading/commands/flow_publish.go
package commands

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// shipCargoItems maps the hull's current hold into the flow-feed cargo shape,
// skipping empty slots. Returns a non-nil (possibly empty) slice.
func shipCargoItems(ship *navigation.Ship) []flowfeed.CargoItem {
	items := []flowfeed.CargoItem{}
	if ship == nil {
		return items
	}
	cargo := ship.Cargo()
	if cargo == nil {
		return items
	}
	for _, it := range cargo.Inventory {
		if it == nil || it.Units <= 0 {
			continue
		}
		items = append(items, flowfeed.CargoItem{Good: it.Symbol, Units: it.Units})
	}
	return items
}

// buildArbFlow maps a one-shot arb run's intent into a flow-feed snapshot. Arb's
// live position comes from the visualizer nav join, so currentLeg is null; the
// daemon's unique contribution is the sell hop (buy here now, sell that good at
// SellAt for up to MaxUnits near QuotedDestBid).
func buildArbFlow(cmd *RunArbCoordinatorCommand, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow {
	return flowfeed.Flow{
		ContainerID: cmd.ContainerID,
		Program:     flowfeed.ProgramArb,
		Ship:        cmd.ShipSymbol,
		TourID:      nil,
		CurrentLeg:  nil,
		Cargo:       cargo,
		RemainingHops: []flowfeed.Hop{{
			Waypoint: cmd.SellAt,
			Tranches: []flowfeed.Tranche{{
				Good:              cmd.Good,
				IsBuy:             false,
				Units:             cmd.MaxUnits,
				ExpectedUnitPrice: cmd.QuotedDestBid,
			}},
		}},
		Projected: nil,
		PlannedAt: now,
	}
}

// buildTourFlow maps a tour plan snapshot into a flow-feed Flow. currentLegIdx < 0
// means the plan was just adopted (no leg in progress): currentLeg is null and
// remainingHops is every planned leg. currentLegIdx >= 0 means the hull is flying
// that leg: currentLeg describes it (From derived from the previous leg, empty for
// the first leg where the nav join owns the origin) and remainingHops is the legs
// after it.
func buildTourFlow(cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, currentLegIdx int, arrivesAt time.Time, cargo []flowfeed.CargoItem, now time.Time) flowfeed.Flow {
	tourID := cmd.ContainerID
	var currentLeg *flowfeed.Leg
	remaining := plan.Legs
	if currentLegIdx >= 0 && currentLegIdx < len(plan.Legs) {
		from := ""
		if currentLegIdx > 0 {
			from = plan.Legs[currentLegIdx-1].Waypoint
		}
		currentLeg = &flowfeed.Leg{
			From:       from,
			To:         plan.Legs[currentLegIdx].Waypoint,
			DepartedAt: now,
			ArrivesAt:  arrivesAt,
		}
		remaining = plan.Legs[currentLegIdx+1:]
	}
	hops := make([]flowfeed.Hop, 0, len(remaining))
	for _, leg := range remaining {
		tranches := make([]flowfeed.Tranche, 0, len(leg.Trades))
		for _, tr := range leg.Trades {
			tranches = append(tranches, flowfeed.Tranche{
				Good:              tr.Good,
				IsBuy:             tr.IsBuy,
				Units:             tr.Units,
				ExpectedUnitPrice: tr.ExpectedUnitPrice,
			})
		}
		hops = append(hops, flowfeed.Hop{Waypoint: leg.Waypoint, Tranches: tranches})
	}
	return flowfeed.Flow{
		ContainerID:   cmd.ContainerID,
		Program:       flowfeed.ProgramTour,
		Ship:          cmd.ShipSymbol,
		TourID:        &tourID,
		CurrentLeg:    currentLeg,
		Cargo:         cargo,
		RemainingHops: hops,
		Projected:     &flowfeed.Projection{Profit: plan.ProjectedProfit, RatePerHour: plan.ProjectedCreditsPerHour},
		PlannedAt:     now,
	}
}

// buildTradeRouteFlow maps the lane a circuit just committed into a flow-feed Flow.
// A trade-route circuit is a single source->dest round trip; currentLeg carries the
// lane's from/to (position truth comes from the nav join, so arrivesAt is best-effort)
// and the sole remaining hop is the sell at the destination.
func buildTradeRouteFlow(cmd *RunTradeRouteCoordinatorCommand, lane trading.ArbitrageLane, ratePerHour float64, cargo []flowfeed.CargoItem, arrivesAt time.Time, now time.Time) flowfeed.Flow {
	return flowfeed.Flow{
		ContainerID: cmd.ContainerID,
		Program:     flowfeed.ProgramTradeRoute,
		Ship:        cmd.ShipSymbol,
		TourID:      nil,
		CurrentLeg: &flowfeed.Leg{
			From:       lane.SourceWaypoint,
			To:         lane.DestWaypoint,
			DepartedAt: now,
			ArrivesAt:  arrivesAt,
		},
		Cargo: cargo,
		RemainingHops: []flowfeed.Hop{{
			Waypoint: lane.DestWaypoint,
			Tranches: []flowfeed.Tranche{{
				Good:              lane.Good,
				IsBuy:             false,
				Units:             lane.VolumeCap,
				ExpectedUnitPrice: lane.DestBid,
			}},
		}},
		Projected: &flowfeed.Projection{Profit: int64(lane.CappedSpread), RatePerHour: ratePerHour},
		PlannedAt: now,
	}
}
