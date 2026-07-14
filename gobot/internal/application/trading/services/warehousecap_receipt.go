package services

// ReceiptCandidate is one good a DESTINATION receives via contract deliveries — the
// receipt-demand signal for a destination-side (contract cluster) warehouse's auto-cap
// knapsack (bead sp-u9xa). It is the sibling of the source-side
// persistence.DemandCandidate: the SAME 0/1 knapsack, but the value is keyed on what the
// destination RECEIVES (recurrence × payment) times the residual HAUL-LEG the buffer
// moves off the serialized contract critical path — dist(destination-warehouse, source) —
// NOT the source buy-leg.
type ReceiptCandidate struct {
	Good string
	// ContractCount is how many recent contracts DELIVERED this good to the destination
	// (recurrence; never speculative).
	ContractCount int
	// Payment is the contract value signal for the good (higher = worth pre-staging).
	Payment float64
	// SourceWaypoint is where the good is sourced; the residual HAUL-LEG saved is
	// dist(destination-warehouse, SourceWaypoint). Empty/unresolvable fails open.
	SourceWaypoint string
	// SourceSystem is the source's system; a cross-system source is the max-haul-saved case
	// (the serialized worker would otherwise fly the whole cross-gate leg).
	SourceSystem string
	// MaxContractUnits is the single-contract fill size s_G; DemandUnits is the fallback size
	// when the single-contract size is unknown.
	MaxContractUnits int
	DemandUnits      int
}

// PlanReceiptCaps solves the warehouse auto-cap knapsack for a DESTINATION-side cluster
// warehouse, keyed on RECEIPT demand (bead sp-u9xa). It is the sibling of PlanWarehouseCaps
// (which stays keyed on the SOURCE buy-leg — that behavior is untouched): the same pure
// ComputeWarehouseCaps knapsack and the same distance→residual ramp, but the demand is what
// the destination RECEIVES and the residual is the HAUL-LEG the buffer relocates onto
// parallel stockers — dist(destination-warehouse, source). A far/cross-system source (a
// long haul saved) ranks high; a near/co-located source (little haul saved) ranks low and
// is dropped first when capacity is tight. Coordinates unavailable FAIL OPEN to the coarse
// in/cross-system residual (RULINGS #1); the pure optimizer still excludes a 0-residual good.
func PlanReceiptCaps(
	candidates []ReceiptCandidate,
	capacity int,
	destinationSystem string,
	destinationWaypoint string,
	coords WaypointCoordsLookup,
	prior map[string]float64,
	current map[string]int,
	params WarehouseCapParams,
) WarehouseCapResult {
	knobs := params.residualKnobs()

	var destX, destY float64
	destKnown := false
	if coords != nil && destinationWaypoint != "" {
		destX, destY, destKnown = coords(destinationWaypoint)
	}

	goods := make([]GoodDemand, 0, len(candidates))
	for _, c := range candidates {
		size := c.MaxContractUnits
		if size <= 0 {
			size = c.DemandUnits // fall back to summed demand only when the single-contract size is unknown
		}
		if size <= 0 {
			continue // nothing to size — cannot buffer
		}
		residual := receiptResidualLeg(c, destinationSystem, destX, destY, destKnown, coords, knobs)
		goods = append(goods, GoodDemand{
			Good:           c.Good,
			Recurrence:     c.ContractCount,
			Payment:        c.Payment,
			ResidualBuyLeg: residual,
			Size:           size,
		})
	}

	return ComputeWarehouseCaps(WarehouseCapInput{
		Capacity:       capacity,
		Goods:          goods,
		PriorSmoothed:  prior,
		CurrentTargets: current,
	}, params)
}

// receiptResidualLeg assigns one received good's residual HAUL-LEG saved for the knapsack
// value — the destination-side analog of residualBuyLeg:
//
//   - a CROSS-system source keeps the MAX (cross) residual: the serialized worker would
//     otherwise fly the whole cross-gate leg, so the destination buffer is most valuable there.
//   - an IN-system source with resolvable coordinates gets a REAL dist(destination-warehouse,
//     source) mapped onto the [floor, ceiling] ramp, so a FAR source (a long haul the buffer
//     relocates) out-ranks a NEAR one (a homed hauler reaches it cheaply anyway).
//   - coordinates unavailable — a nil lookup, an unknown destination position, or an uncached
//     source waypoint — FAIL OPEN to the coarse in-system residual (RULINGS #1).
func receiptResidualLeg(
	c ReceiptCandidate,
	destinationSystem string,
	destX, destY float64,
	destKnown bool,
	coords WaypointCoordsLookup,
	knobs residualKnobs,
) float64 {
	if c.SourceSystem != "" && c.SourceSystem != destinationSystem {
		return knobs.crossResidual // cross-system: the buffer's highest-value case
	}
	if !destKnown || coords == nil || c.SourceWaypoint == "" {
		return knobs.inResidual
	}
	srcX, srcY, ok := coords(c.SourceWaypoint)
	if !ok {
		return knobs.inResidual
	}
	return residualForDistance(euclidDist(destX, destY, srcX, srcY), knobs.floor, knobs.ceiling, knobs.saturation)
}
