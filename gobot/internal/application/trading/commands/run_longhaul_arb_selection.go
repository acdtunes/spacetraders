package commands

// run_longhaul_arb_selection.go — the worker's lane SELECTION + buy SIZING (sp-mepj §2/§3):
// the decision that ties discovery+pricing (rankLongHaulLanes) to the money envelope
// (spendCeiling/permitsBuy) and the hull's hold. Pure and unit-tested; the worker's execution
// orchestration drives it, then hands the chosen (lane, units) to the reused arb executor.

import "github.com/andrescamacho/spacetraders-go/internal/domain/trading"

// achievableUnits sizes the buy for a priced lane to the tightest of: the impact-optimal
// tranche, the hull's free hold, what the envelope's spend ceiling can afford at the source
// ask, and the sink's live absorption headroom (VolumeCap minus others' in-flight PLANNED
// units) — the design's "Achievable = min(cargo cap, spend_cap/avg-price, optimal_q, sink
// absorption headroom)". Fail-safe (RULINGS #4): any non-positive bound yields 0 (no buy). A
// negative absorptionHeadroom means "not consulted" (unbounded by absorption).
func achievableUnits(lane pricedLongHaulLane, holdSpace int, envelope longHaulEnvelope, absorptionHeadroom int) int {
	units := lane.OptimalUnits
	if holdSpace < units {
		units = holdSpace
	}
	if ask := lane.Lane.SourceAsk; ask > 0 {
		affordable := int(envelope.spendCeiling() / int64(ask))
		if affordable < units {
			units = affordable
		}
	}
	if absorptionHeadroom >= 0 && absorptionHeadroom < units {
		units = absorptionHeadroom
	}
	if units < 0 {
		return 0
	}
	return units
}

// selectedHaul is one viable (lane, sized units) the worker can trade — the unit selectHauls
// yields in ranked realized-$/hr order so the episode can try the NEXT candidate when the top
// pick's source turns out to be gate-unroutable (sp-e059j reachability fallback).
type selectedHaul struct {
	lane  pricedLongHaulLane
	units int
}

// selectHauls returns EVERY ranked lane the worker can actually trade — sized buy positive AND
// buy cost clears the money envelope (permitsBuy — per-haul cap + the 200k cushion fence,
// fail-closed) — preserving the input's realized-$/hr order. Returning all of them rather than
// only the best is the reachability fallback: the episode iterates these so a top lane whose
// SOURCE is structurally unreachable for this hull is skipped for the next viable lane instead
// of error-looping the same deterministic pick (sp-e059j). The floor/envelope/fit discipline is
// applied per candidate (not duplicated). absorptionHeadroom is the worker's live sink-depth
// consult (nil → unbounded by absorption). An unreadable treasury sizes every lane to zero, so
// this returns empty and the worker trades nothing (fail-closed).
func selectHauls(
	ranked []pricedLongHaulLane,
	holdSpace int,
	envelope longHaulEnvelope,
	absorptionHeadroom func(lane trading.ArbitrageLane) int,
) []selectedHaul {
	hauls := make([]selectedHaul, 0, len(ranked))
	for _, lane := range ranked {
		headroom := -1
		if absorptionHeadroom != nil {
			headroom = absorptionHeadroom(lane.Lane)
		}
		units := achievableUnits(lane, holdSpace, envelope, headroom)
		if units <= 0 {
			continue
		}
		buyCost := int64(units) * int64(lane.Lane.SourceAsk)
		if ok, _ := envelope.permitsBuy(buyCost); !ok {
			continue
		}
		hauls = append(hauls, selectedHaul{lane: lane, units: units})
	}
	return hauls
}
