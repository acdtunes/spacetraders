// Package outfitting holds the pure selection model behind the guarded auto-outfit
// coordinator: the module analogue of hull acquisition. Given a per-hull
// saturation/range read (aggregated from tour_leg_telemetry) and a catalog of
// available ship modules, it picks the highest-marginal-value (hull, module) pair —
// the hull whose measured bottleneck an available module most relieves per credit.
//
// The load-bearing insight: the right hull to upgrade is the SATURATED one, not
// the busiest one. A hull running 40/80 has spare capacity a
// cargo module would leave idle; a hull running 76/80 is genuinely capacity-bound, so
// the same module is immediately used. Benefit is therefore gated by MEASURED
// saturation, and a hull with too little telemetry to trust is never upgraded at all
// (fail closed — never spend on a bottleneck you cannot measure).
//
// This package is pure: no I/O, no clock, no ports. The coordinator reads telemetry,
// the catalog, and hull facts through its ports and hands the folded values here.
package outfitting

import "strings"

// ModuleClass is the capacity lever a ship module pulls. The auto-outfit actuator acts
// only on the two capacity classes; everything else (reactors, jump/warp drives,
// mining lasers, processors) is Other and never auto-installed.
type ModuleClass int

const (
	// ModuleClassOther is any module the auto-outfit actuator does not act on.
	ModuleClassOther ModuleClass = iota
	// ModuleClassCargo is a cargo-hold module (MODULE_CARGO_HOLD_*): +cargo capacity.
	ModuleClassCargo
	// ModuleClassFuel is a fuel-tank module (MODULE_FUEL_TANK*): +range.
	ModuleClassFuel
)

const (
	cargoHoldPrefix = "MODULE_CARGO_HOLD"
	fuelTankPrefix  = "MODULE_FUEL_TANK"
)

// refuelPressureReference is the refuel-stops-per-leg that reads as fully
// range-bound (pressure 1.0). Two mid-leg refuels per leg is a hull spending as much
// time tanking as trading — a saturated range bottleneck.
const refuelPressureReference = 2.0

// ClassifyModule maps a module symbol to the capacity lever it pulls. Prefix-based, so
// every tier (CARGO_HOLD_I/II/III, FUEL_TANK) classifies without an enumerated table.
func ClassifyModule(symbol string) ModuleClass {
	switch {
	case strings.HasPrefix(symbol, cargoHoldPrefix):
		return ModuleClassCargo
	case strings.HasPrefix(symbol, fuelTankPrefix):
		return ModuleClassFuel
	default:
		return ModuleClassOther
	}
}

// knownModuleCapacity holds the capacity each catalog-tracked module grants
// (SpaceTraders game constants, matching the codebase's client_module fixtures). The
// catalog adapter reads it to set an offer's CapacityGained, since the market good row
// carries only price, not the module's capacity bonus.
var knownModuleCapacity = map[string]int{
	"MODULE_CARGO_HOLD_I":   40,
	"MODULE_CARGO_HOLD_II":  80,
	"MODULE_CARGO_HOLD_III": 120,
	"MODULE_FUEL_TANK":      400,
}

// KnownModuleCapacity returns the capacity a module grants, or 0 for an unknown symbol.
// An unknown module yields CapacityGained 0, which the scorer rejects — so an unrecognized
// module is never auto-installed (fail closed).
func KnownModuleCapacity(symbol string) int {
	return knownModuleCapacity[symbol]
}

// HullBottleneck is one hull's measured saturation/range read — the demand signal an
// upgrade is scored against. CargoSaturation is realized_units/capacity averaged over
// the measured load legs; CargoLegs is the sample size the thin-telemetry fail-closed
// gate reads. ThroughputPerHour is the hull's realized value-per-unit-capacity-per-hour
// (turns/hr × margin/unit) — a faster hull turns the same capacity gain into more value.
type HullBottleneck struct {
	ShipSymbol         string
	Role               string
	IsCargoHauler      bool // role can carry cargo (HAULER/COMMAND), not a scout
	IsRangeConstrained bool // range-limited hull a FUEL_TANK would relieve
	CargoCapacity      int
	FreeModuleSlots    int
	CargoLegs          int
	CargoSaturation    float64 // 0..1, realized/capacity over the measured load legs
	RangeLegs          int
	RefuelStopsPerLeg  float64
	ThroughputPerHour  float64
}

// ModuleOffer is one catalog entry: a module available to buy, its nearest source, its
// price (the market ask), the capacity it grants, and the reach hops from the fleet to
// that source (a logistics-cost proxy).
type ModuleOffer struct {
	Symbol         string
	Class          ModuleClass
	Price          int
	CapacityGained int
	Waypoint       string
	System         string
	ReachHops      int
}

// SelectionConfig carries the tunable gates the scorer applies.
type SelectionConfig struct {
	// MinTelemetrySamples is the thin-telemetry fail-closed floor: a hull with fewer
	// measured legs than this is never upgraded.
	MinTelemetrySamples int
	// InstallFeeEstimate and HopCost enter the cost side of the payback math (the
	// shipyard modification fee and the per-hop divert-to-source logistics cost).
	InstallFeeEstimate int
	HopCost            int
	// PaybackHorizonHours is the ABSOLUTE payback gate: the upgrade cost must be
	// recovered from the extra throughput within this many hours. 0 disables it.
	PaybackHorizonHours float64
	// NewHullCostPerUnit is the RELATIVE payback gate (the autosizer second-actuator
	// integration): the $/unit-capacity of buying a whole new hull. An upgrade that
	// costs at least this much per unit is refused — buy-new is the cheaper lever.
	// 0 (unreadable / off) leaves the comparison off; the spend guards still bind.
	NewHullCostPerUnit float64
}

// UpgradePick is the winning (hull, module) pair and the math behind it.
type UpgradePick struct {
	ShipSymbol          string
	Module              ModuleOffer
	Score               float64 // marginal value per hour — the ranking key
	ValuePerHour        float64
	Cost                int
	CostPerUnitCapacity float64
}

// SelectUpgrade picks the highest-marginal-value (hull, module) pair, or ok=false when
// no pair clears the hard filters, the fail-closed telemetry gate, and the payback
// gates. It scores every feasible pair and returns the single best — one upgrade per
// call, the credit spent where it relieves the most measured saturation.
func SelectUpgrade(hulls []HullBottleneck, catalog []ModuleOffer, cfg SelectionConfig) (UpgradePick, bool) {
	best := UpgradePick{}
	found := false
	for _, hull := range hulls {
		for _, offer := range catalog {
			pick, ok := scorePair(hull, offer, cfg)
			if !ok {
				continue
			}
			if !found || pick.Score > best.Score {
				best, found = pick, true
			}
		}
	}
	return best, found
}

// scorePair scores one (hull, offer) pair, returning ok=false when any hard filter,
// the thin-telemetry gate, or a payback gate rejects it. Guard clauses, one concern
// each, cheapest checks first.
func scorePair(hull HullBottleneck, offer ModuleOffer, cfg SelectionConfig) (UpgradePick, bool) {
	if offer.Class == ModuleClassOther {
		return UpgradePick{}, false
	}
	if offer.CapacityGained < 1 {
		return UpgradePick{}, false
	}
	if hull.FreeModuleSlots < 1 {
		return UpgradePick{}, false // no free frame slot the module fits
	}
	if !roleMatches(hull, offer.Class) {
		return UpgradePick{}, false // wrong role (cargo on a scout, fuel on a non-range hull)
	}
	saturation, legs, ok := bottleneckFor(hull, offer.Class)
	if !ok {
		return UpgradePick{}, false
	}
	if legs < cfg.MinTelemetrySamples {
		return UpgradePick{}, false // FAIL CLOSED: too little telemetry to trust the bottleneck
	}
	valuePerHour := float64(offer.CapacityGained) * saturation * throughput(hull)
	cost := offer.Price + cfg.InstallFeeEstimate + offer.ReachHops*cfg.HopCost
	if cfg.PaybackHorizonHours > 0 && valuePerHour*cfg.PaybackHorizonHours < float64(cost) {
		return UpgradePick{}, false // ABSOLUTE payback: cost not recovered within the horizon
	}
	costPerUnit := float64(cost) / float64(offer.CapacityGained)
	if cfg.NewHullCostPerUnit > 0 && costPerUnit >= cfg.NewHullCostPerUnit {
		return UpgradePick{}, false // RELATIVE payback: a new hull is cheaper per unit of capacity
	}
	return UpgradePick{
		ShipSymbol:          hull.ShipSymbol,
		Module:              offer,
		Score:               valuePerHour,
		ValuePerHour:        valuePerHour,
		Cost:                cost,
		CostPerUnitCapacity: costPerUnit,
	}, true
}

// roleMatches reports whether a module's class matches the hull's role: a cargo hold
// belongs on a cargo hauler, a fuel tank on a range-constrained hull.
func roleMatches(hull HullBottleneck, class ModuleClass) bool {
	switch class {
	case ModuleClassCargo:
		return hull.IsCargoHauler
	case ModuleClassFuel:
		return hull.IsRangeConstrained
	default:
		return false
	}
}

// bottleneckFor returns the 0..1 measured bottleneck pressure and the sample size for
// the module's class: cargo saturation for a cargo hold, range pressure for a fuel tank.
func bottleneckFor(hull HullBottleneck, class ModuleClass) (pressure float64, legs int, ok bool) {
	switch class {
	case ModuleClassCargo:
		return hull.CargoSaturation, hull.CargoLegs, true
	case ModuleClassFuel:
		return rangePressure(hull.RefuelStopsPerLeg), hull.RangeLegs, true
	default:
		return 0, 0, false
	}
}

// rangePressure normalizes refuel stops per leg to a 0..1 bottleneck pressure,
// saturating at refuelPressureReference.
func rangePressure(refuelStopsPerLeg float64) float64 {
	pressure := refuelStopsPerLeg / refuelPressureReference
	if pressure > 1 {
		return 1
	}
	if pressure < 0 {
		return 0
	}
	return pressure
}

// throughput is the hull's value-per-unit-capacity-per-hour multiplier, floored at a
// neutral 1.0 when unmeasured so an absent rate signal neither zeroes nor inflates the
// benefit (the ranking then rests on saturation alone).
func throughput(hull HullBottleneck) float64 {
	if hull.ThroughputPerHour > 0 {
		return hull.ThroughputPerHour
	}
	return 1.0
}

// LegSaturation is the minimal per-leg telemetry the aggregator folds — the
// realized units a hull loaded on one trade leg and whether that leg was a load (buy).
// The coordinator maps persisted tour_leg_telemetry rows to this slim shape, keeping
// this package free of the trading domain.
type LegSaturation struct {
	ShipSymbol    string
	RealizedUnits int
	IsBuy         bool
}

// HullFacts is the per-hull static context the aggregator needs from the ship repo —
// telemetry carries realized units, not the hull's capacity, role, or free slots.
type HullFacts struct {
	Role               string
	IsCargoHauler      bool
	IsRangeConstrained bool
	CargoCapacity      int
	FreeModuleSlots    int
	ThroughputPerHour  float64
}

// AggregateBottlenecks folds raw per-leg tour telemetry into one HullBottleneck per
// hull. Cargo saturation is the mean of realized_units/capacity over the hull's LOAD
// (buy) legs — a buy leg fills the hold, so its realized fill is the saturation
// signal — and CargoLegs is the count of those legs (the thin-telemetry sample size).
// A hull with no facts (not in the fleet) or zero capacity is skipped. Insertion order
// is preserved so the read is deterministic.
func AggregateBottlenecks(legs []LegSaturation, facts map[string]HullFacts) []HullBottleneck {
	type accumulator struct {
		sumFill float64
		count   int
	}
	byShip := map[string]*accumulator{}
	order := []string{}
	for _, leg := range legs {
		if !leg.IsBuy {
			continue // only load legs carry the fill signal
		}
		hullFacts, known := facts[leg.ShipSymbol]
		if !known || hullFacts.CargoCapacity <= 0 {
			continue
		}
		acc := byShip[leg.ShipSymbol]
		if acc == nil {
			acc = &accumulator{}
			byShip[leg.ShipSymbol] = acc
			order = append(order, leg.ShipSymbol)
		}
		acc.sumFill += clampFill(float64(leg.RealizedUnits) / float64(hullFacts.CargoCapacity))
		acc.count++
	}

	out := make([]HullBottleneck, 0, len(order))
	for _, shipSymbol := range order {
		acc := byShip[shipSymbol]
		hullFacts := facts[shipSymbol]
		out = append(out, HullBottleneck{
			ShipSymbol:         shipSymbol,
			Role:               hullFacts.Role,
			IsCargoHauler:      hullFacts.IsCargoHauler,
			IsRangeConstrained: hullFacts.IsRangeConstrained,
			CargoCapacity:      hullFacts.CargoCapacity,
			FreeModuleSlots:    hullFacts.FreeModuleSlots,
			CargoLegs:          acc.count,
			CargoSaturation:    acc.sumFill / float64(acc.count),
			ThroughputPerHour:  hullFacts.ThroughputPerHour,
		})
	}
	return out
}

func clampFill(fill float64) float64 {
	if fill > 1 {
		return 1
	}
	if fill < 0 {
		return 0
	}
	return fill
}
