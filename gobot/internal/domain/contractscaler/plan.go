package contractscaler

import "sort"

// The fixed fleet arrangement (design-time constants, event-validated era-3 —
// NOT runtime-computed). Delivery hulls are the PRIMARY, scarce lever: one per
// distinct central-cluster waypoint, saturating at ~7-8; the central far-source
// warehouse + stocker are the cheap add-on stocked centrally (no J depot).
const (
	// MaxDeliveryHulls caps the delivery pool at the p-median knee (design STEP 1d:
	// era-4 marginals 47/17/12/9/7 then 7%/2% → 6 haulers; "NEVER 8"). The 7th/8th
	// hull's positioning gain falls below a warehouse haul-saving, so surplus budget
	// reallocates to warehouse depth (see WarehouseUnits).
	MaxDeliveryHulls = 6
	// StockerUnits is the single central stocker running the far source-legs
	// out-of-band (event-sim: 1 = 2 = 3, count is not the bottleneck; buffer depth is).
	StockerUnits = 1
	// ScalerShipType is the light frame every scaler unit buys (delivery haulers,
	// warehouses, and the stocker are all light hulls; depth, not frame, is the lever).
	ScalerShipType = "SHIP_LIGHT_HAULER"
)

// WarehouseUnits is the DYNAMIC central far-source warehouse depth CAP: one warehouse per
// far-source good (each buffered ~2× contract qty — DepotUnitsPerGood). The reconcile budget
// DEEPENS the warehouse to fill the remaining ceiling (ceiling − delivery − stocker) UP TO
// this cap, so the live composition scales with the ceiling — the design curve (budget 10→3wh,
// 12→5wh, 14→7wh), capping at len(FarSourceGoods). Replaces the prior fixed-5 depth: with
// delivery capped at 6, the ceiling headroom past 7 hulls goes entirely to warehouse depth.
var WarehouseUnits = len(FarSourceGoods)

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
	slots := TopDeliverySlots(roles.CentralParks, parkDemand)

	plan := make([]PlanUnit, 0, len(slots)+WarehouseUnits+StockerUnits)
	for _, park := range slots {
		plan = append(plan, PlanUnit{Role: DeliveryHauler, ShipType: ScalerShipType, Target: park, Demand: parkDemand[park]})
	}

	// The central hub anchors the warehouse + stocker (central far-source storage,
	// NOT the far J sink). "" when the era has no central park — then the warehouse
	// bundle has no home and is omitted (delivery-only fallback).
	hub := ""
	if len(slots) > 0 {
		hub = slots[0]
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

// TopDeliverySlots is the FIXED delivery-hull placement set: the central parks ranked
// highest-demand first (symbol-stable on ties), capped at the MaxDeliveryHulls knee. It is the
// SINGLE selection both the scaler buys against (BuildPlan) AND the fixed homing zips hulls onto
// (the standby set), so the two positioning consumers agree on ONE ≤6 slot set with no drift.
// Demand is an input HERE, resolved ONCE at arm — the RUNTIME homing carries no demand at all
// (it zips hulls to these slots by symbol; see domain/contract.AssignedSlot). Co-located parks
// are deduped upstream by the resolver, so this ranks distinct LOCATIONS. Empty parks → empty.
func TopDeliverySlots(parks []string, demand map[string]float64) []string {
	ranked := rankParks(parks, demand)
	if len(ranked) > MaxDeliveryHulls {
		ranked = ranked[:MaxDeliveryHulls]
	}
	return ranked
}

// RoleTargets counts how many of each role the fixed plan holds — the per-role fill
// targets (delivery D, warehouse W, stocker S) the role-aware ramp fills IN ORDER. It
// lets the ramp reconcile the existing depot up to each role's target (adding only what
// is short) instead of treating every plan unit as a delivery buy (the pre-actuation bug
// where a warehouse/stocker plan index bought a plain delivery hull).
func RoleTargets(plan []PlanUnit) (delivery, warehouse, stocker int) {
	for _, unit := range plan {
		switch unit.Role {
		case DeliveryHauler:
			delivery++
		case Warehouse:
			warehouse++
		case Stocker:
			stocker++
		}
	}
	return delivery, warehouse, stocker
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
