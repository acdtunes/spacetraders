package contractscaler

import "sort"

// The fixed fleet arrangement (design-time constants, event-validated era-3 —
// NOT runtime-computed). Delivery hulls are the PRIMARY, scarce lever: one per
// distinct central-cluster waypoint, saturating at ~7-8; the central far-source
// warehouse + stocker are the cheap add-on stocked centrally (no J depot).
const (
	// MaxDeliveryHulls caps the delivery pool at the number of distinct central
	// waypoints the serial engine can usefully spread across (~7-8; the curve
	// flattens past it).
	MaxDeliveryHulls = 8
	// WarehouseUnits is the depth of the ONE central far-source warehouse
	// (ores/precious/drugs buffered ~2 contracts per good; ~3 light-hull slots at
	// the 10-hull efficient arrangement).
	WarehouseUnits = 3
	// StockerUnits is the single central stocker running the far source-legs
	// out-of-band (event-sim: 1 = 2 = 3, count is not the bottleneck; buffer depth is).
	StockerUnits = 1
	// ScalerShipType is the light frame every scaler unit buys (delivery haulers,
	// warehouses, and the stocker are all light hulls; depth, not frame, is the lever).
	ScalerShipType = "SHIP_LIGHT_HAULER"
)

// UnitRole is a scaler hull's role in the fixed plan.
type UnitRole int

const (
	// DeliveryHauler is a central contract-delivery hull, homed to one distinct park.
	DeliveryHauler UnitRole = iota
	// Warehouse is a central far-source storage hull.
	Warehouse
	// Stocker is the central stocker running the far source-legs out-of-band.
	Stocker
)

// PlanUnit is one entry in the fixed buy sequence: the role, the light frame to
// buy, the waypoint it is placed/homed at, and (delivery hulls only) the demand
// weight that ranked its park.
type PlanUnit struct {
	Role     UnitRole
	ShipType string
	Target   string
	Demand   float64
}

// BuildPlan emits the FIXED buy sequence for this era's resolved roles: delivery
// hulls first (one per central park, ranked highest-demand first, capped at
// MaxDeliveryHulls), then the central far-source warehouse units, then the
// stocker — all anchored at the central hub (the top-demand park). The fill order
// is STRUCTURALLY MANDATED (delivery hulls to the knee before the lumpy warehouse
// bundle), not a marginal-$/hr greedy. Deterministic: demand-desc, symbol-stable.
func BuildPlan(roles EraRoles, parkDemand map[string]float64) []PlanUnit {
	ranked := rankParks(roles.CentralParks, parkDemand)
	if len(ranked) > MaxDeliveryHulls {
		ranked = ranked[:MaxDeliveryHulls]
	}

	plan := make([]PlanUnit, 0, len(ranked)+WarehouseUnits+StockerUnits)
	for _, park := range ranked {
		plan = append(plan, PlanUnit{Role: DeliveryHauler, ShipType: ScalerShipType, Target: park, Demand: parkDemand[park]})
	}

	// The central hub anchors the warehouse + stocker (central far-source storage,
	// NOT the far J sink). "" when the era has no central park — then the warehouse
	// bundle has no home and is omitted (delivery-only fallback).
	hub := ""
	if len(ranked) > 0 {
		hub = ranked[0]
	}
	if hub != "" {
		for i := 0; i < WarehouseUnits; i++ {
			plan = append(plan, PlanUnit{Role: Warehouse, ShipType: ScalerShipType, Target: hub})
		}
		for i := 0; i < StockerUnits; i++ {
			plan = append(plan, PlanUnit{Role: Stocker, ShipType: ScalerShipType, Target: hub})
		}
	}
	return plan
}

// StandbyDemand is the per-central-park demand weight map C1's demand-ranked idle
// homing consumes — the spread signal that lets the 3rd..Nth delivery hull pay
// (each covers a distinct pickup region). Derived here so the coordinator and the
// contract fleet coordinator share ONE demand definition.
func StandbyDemand(roles EraRoles, parkDemand map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(roles.CentralParks))
	for _, park := range roles.CentralParks {
		out[park] = parkDemand[park]
	}
	return out
}

// Target is the buy-loop stop: buy up to min(plan size, live ceiling). The ceiling
// (contract_fleet_max_hulls) is the single operator throttle; a zero ceiling buys
// nothing.
func Target(planSize, ceiling int) int {
	if ceiling < planSize {
		return ceiling
	}
	return planSize
}

// rankParks orders parks highest-demand first, symbol-ascending on ties — the
// stable order that makes the plan a pure function of its inputs (never map
// iteration order).
func rankParks(parks []string, demand map[string]float64) []string {
	out := append([]string(nil), parks...)
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := demand[out[i]], demand[out[j]]
		if di != dj {
			return di > dj
		}
		return out[i] < out[j]
	})
	return out
}
