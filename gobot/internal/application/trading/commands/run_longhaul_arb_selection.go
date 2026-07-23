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

// selectHaul picks the highest realized-$/hr lane the worker can actually trade: the first
// ranked lane whose sized buy is positive AND whose buy cost clears the money envelope
// (permitsBuy — per-haul cap + the 200k cushion fence, fail-closed; a redundant backstop to
// the spend-ceiling sizing, load-bearing when no fence is wired). absorptionHeadroom is the
// worker's live sink-depth consult (nil → unbounded by absorption). Returns (lane, units,
// true) on a tradeable lane, or ok=false when none clears — an unreadable treasury sizes
// every lane to zero, so the worker trades nothing (fail-closed) rather than spend blind.
func selectHaul(
	ranked []pricedLongHaulLane,
	holdSpace int,
	envelope longHaulEnvelope,
	absorptionHeadroom func(lane trading.ArbitrageLane) int,
) (pricedLongHaulLane, int, bool) {
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
		return lane, units, true
	}
	return pricedLongHaulLane{}, 0, false
}
