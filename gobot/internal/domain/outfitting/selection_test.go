package outfitting

import "testing"

// cargoOffer is the shared MODULE_CARGO_HOLD_II catalog entry the selection tests
// score hulls against: an 80-unit cargo hold at a nearby market.
func cargoOffer() ModuleOffer {
	return ModuleOffer{
		Symbol:         "MODULE_CARGO_HOLD_II",
		Class:          ModuleClassCargo,
		Price:          50000,
		CapacityGained: 80,
		Waypoint:       "X1-TORWIND-MKT",
		System:         "X1-TORWIND",
		ReachHops:      1,
	}
}

func baseCfg() SelectionConfig {
	return SelectionConfig{
		MinTelemetrySamples: 8,
		InstallFeeEstimate:  1000,
		HopCost:             100,
		PaybackHorizonHours: 0, // absolute payback gate off unless a test sets it
		NewHullCostPerUnit:  0, // relative new-hull gate off unless a test sets it
	}
}

// THE headline proof (live-validated, sp-buyd): marginal value picks the SATURATED
// hull, not the busiest-but-empty one. TORWIND-7 is the busiest hauler (highest
// throughput) yet runs half-empty (40/80 → 0.5): a cargo module would sit idle on it.
// TORWIND-16 runs nearly full (76/80 → 0.95): the added capacity is immediately used.
// So 16 must outrank 7 even though 7 is busier.
func TestSelectUpgrade_PicksSaturatedHullOverBusiestButEmpty(t *testing.T) {
	hulls := []HullBottleneck{
		{ShipSymbol: "TORWIND-7", IsCargoHauler: true, FreeModuleSlots: 1, CargoCapacity: 80,
			CargoLegs: 10, CargoSaturation: 0.5, ThroughputPerHour: 1.2},
		{ShipSymbol: "TORWIND-16", IsCargoHauler: true, FreeModuleSlots: 1, CargoCapacity: 80,
			CargoLegs: 10, CargoSaturation: 0.95, ThroughputPerHour: 1.0},
	}

	pick, ok := SelectUpgrade(hulls, []ModuleOffer{cargoOffer()}, baseCfg())

	if !ok {
		t.Fatalf("expected a pick, got none")
	}
	if pick.ShipSymbol != "TORWIND-16" {
		t.Fatalf("marginal value must pick the SATURATED hull TORWIND-16, not the busiest-but-empty TORWIND-7; got %q", pick.ShipSymbol)
	}
	if pick.Module.Symbol != "MODULE_CARGO_HOLD_II" {
		t.Fatalf("expected the cargo module, got %q", pick.Module.Symbol)
	}
}

// FAIL-CLOSED on thin telemetry (mutation-checked). A hull with too few measured legs
// is NEVER upgraded — we cannot trust its saturation, so we do not spend on it. The
// two cases pin the gate boundary: legs below the floor are rejected even when
// saturated; legs at the floor are eligible. This kills both the "drop the gate" and
// the "<= instead of <" mutants.
func TestSelectUpgrade_FailsClosedOnThinTelemetry(t *testing.T) {
	cases := []struct {
		name         string
		legs         int
		wantEligible bool
	}{
		{"below the sample floor is never upgraded", 3, false},
		{"at the sample floor is eligible", 8, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hulls := []HullBottleneck{
				{ShipSymbol: "TORWIND-16", IsCargoHauler: true, FreeModuleSlots: 1, CargoCapacity: 80,
					CargoLegs: tc.legs, CargoSaturation: 0.95, ThroughputPerHour: 1.0},
			}
			_, ok := SelectUpgrade(hulls, []ModuleOffer{cargoOffer()}, baseCfg())
			if ok != tc.wantEligible {
				t.Fatalf("legs=%d: eligible=%v, want %v (MinTelemetrySamples=%d)", tc.legs, ok, tc.wantEligible, baseCfg().MinTelemetrySamples)
			}
		})
	}
}

// Hard filters: the module class must match the hull role and the hull must have a
// free module slot. A CARGO_HOLD is never installed on a scout; a FUEL_TANK never on
// a non-range-constrained hull; and a full frame is skipped.
func TestSelectUpgrade_RoleAndSlotHardFilter(t *testing.T) {
	fuelOffer := ModuleOffer{Symbol: "MODULE_FUEL_TANK", Class: ModuleClassFuel, Price: 20000, CapacityGained: 100, ReachHops: 1}
	cases := []struct {
		name   string
		hull   HullBottleneck
		offer  ModuleOffer
		wantOk bool
	}{
		{"cargo module rejected on a scout (wrong role)",
			HullBottleneck{ShipSymbol: "PROBE-1", IsCargoHauler: false, FreeModuleSlots: 1, CargoCapacity: 40, CargoLegs: 10, CargoSaturation: 0.9}, cargoOffer(), false},
		{"cargo module rejected with no free slot",
			HullBottleneck{ShipSymbol: "TORWIND-16", IsCargoHauler: true, FreeModuleSlots: 0, CargoCapacity: 80, CargoLegs: 10, CargoSaturation: 0.95}, cargoOffer(), false},
		{"fuel module rejected on a non-range-constrained hull",
			HullBottleneck{ShipSymbol: "TORWIND-16", IsCargoHauler: true, IsRangeConstrained: false, FreeModuleSlots: 1, CargoCapacity: 80, RangeLegs: 10, RefuelStopsPerLeg: 2}, fuelOffer, false},
		{"cargo module accepted on a saturated hauler with a free slot",
			HullBottleneck{ShipSymbol: "TORWIND-16", IsCargoHauler: true, FreeModuleSlots: 1, CargoCapacity: 80, CargoLegs: 10, CargoSaturation: 0.95, ThroughputPerHour: 1.0}, cargoOffer(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := SelectUpgrade([]HullBottleneck{tc.hull}, []ModuleOffer{tc.offer}, baseCfg())
			if ok != tc.wantOk {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOk)
			}
		})
	}
}

// Payback gates. The ABSOLUTE gate refuses an upgrade whose cost is not recovered from
// the extra throughput within the payback horizon. The RELATIVE gate (the autosizer
// second-actuator integration) refuses an upgrade that costs MORE per unit of capacity
// than buying a whole new hull — buy-new is then the cheaper capacity lever.
func TestSelectUpgrade_PaybackGates(t *testing.T) {
	// cost = 50000 + 1000 install + 1 hop*100 = 51100; costPerUnit = 51100/80 = 638.75
	saturated := HullBottleneck{ShipSymbol: "TORWIND-16", IsCargoHauler: true, FreeModuleSlots: 1,
		CargoCapacity: 80, CargoLegs: 10, CargoSaturation: 0.95, ThroughputPerHour: 40}
	cases := []struct {
		name   string
		mutate func(c *SelectionConfig)
		wantOk bool
	}{
		{"absolute: cost recovered within a generous horizon → chosen",
			func(c *SelectionConfig) { c.PaybackHorizonHours = 24 }, true}, // 80*0.95*40*24 = 72960 >= 51100
		{"absolute: cost not recovered within a tiny horizon → refused",
			func(c *SelectionConfig) { c.PaybackHorizonHours = 1 }, false}, // 3040 < 51100
		{"relative: upgrade cheaper per unit than a new hull → chosen",
			func(c *SelectionConfig) { c.NewHullCostPerUnit = 1000 }, true}, // 638.75 < 1000
		{"relative: new hull cheaper per unit → upgrade refused",
			func(c *SelectionConfig) { c.NewHullCostPerUnit = 500 }, false}, // 638.75 >= 500
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg()
			tc.mutate(&cfg)
			_, ok := SelectUpgrade([]HullBottleneck{saturated}, []ModuleOffer{cargoOffer()}, cfg)
			if ok != tc.wantOk {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOk)
			}
		})
	}
}

// ClassifyModule maps a module symbol to the capacity lever it pulls; everything the
// actuator does not act on (reactors, jump drives, mining lasers) is Other.
func TestClassifyModule(t *testing.T) {
	cases := map[string]ModuleClass{
		"MODULE_CARGO_HOLD_I":        ModuleClassCargo,
		"MODULE_CARGO_HOLD_III":      ModuleClassCargo,
		"MODULE_FUEL_TANK":           ModuleClassFuel,
		"MODULE_JUMP_DRIVE_I":        ModuleClassOther,
		"MODULE_MINERAL_PROCESSOR_I": ModuleClassOther,
	}
	for symbol, want := range cases {
		if got := ClassifyModule(symbol); got != want {
			t.Errorf("ClassifyModule(%q)=%v, want %v", symbol, got, want)
		}
	}
}

// AggregateBottlenecks folds raw per-leg tour telemetry into a per-hull saturation
// read: cargo saturation = mean(realized_units / capacity) over the hull's BUY legs,
// and the leg count is the sample size the thin-telemetry gate reads. This is how the
// selector reads tour_leg_telemetry: TORWIND-16 loads 76 of 80 (0.95) while TORWIND-7
// loads 40 of 80 (0.5).
func TestAggregateBottlenecks_ReadsRealizedFillPerCapacity(t *testing.T) {
	legs := []LegSaturation{
		{ShipSymbol: "TORWIND-16", RealizedUnits: 76, IsBuy: true},
		{ShipSymbol: "TORWIND-16", RealizedUnits: 76, IsBuy: true},
		{ShipSymbol: "TORWIND-7", RealizedUnits: 40, IsBuy: true},
		{ShipSymbol: "TORWIND-7", RealizedUnits: 0, IsBuy: false}, // sell leg: ignored (not a load)
	}
	facts := map[string]HullFacts{
		"TORWIND-16": {IsCargoHauler: true, CargoCapacity: 80, FreeModuleSlots: 1},
		"TORWIND-7":  {IsCargoHauler: true, CargoCapacity: 80, FreeModuleSlots: 1},
	}

	got := AggregateBottlenecks(legs, facts)

	byShip := map[string]HullBottleneck{}
	for _, b := range got {
		byShip[b.ShipSymbol] = b
	}
	if b := byShip["TORWIND-16"]; b.CargoLegs != 2 || !approx(b.CargoSaturation, 0.95) {
		t.Fatalf("TORWIND-16: legs=%d saturation=%.3f, want 2 legs and 0.95", b.CargoLegs, b.CargoSaturation)
	}
	if b := byShip["TORWIND-7"]; b.CargoLegs != 1 || !approx(b.CargoSaturation, 0.5) {
		t.Fatalf("TORWIND-7: legs=%d saturation=%.3f, want 1 leg and 0.50", b.CargoLegs, b.CargoSaturation)
	}
}

// KnownModuleCapacity maps a module symbol to the capacity it grants (SpaceTraders game
// constants), 0 for a symbol whose capacity is unknown — the catalog then leaves the
// offer's CapacityGained at 0 and the scorer skips it (fail-closed on an unknown module).
func TestKnownModuleCapacity(t *testing.T) {
	cases := map[string]int{
		"MODULE_CARGO_HOLD_II":  80,
		"MODULE_CARGO_HOLD_III": 120,
		"MODULE_UNKNOWN_XYZ":    0,
	}
	for symbol, want := range cases {
		if got := KnownModuleCapacity(symbol); got != want {
			t.Errorf("KnownModuleCapacity(%q)=%d, want %d", symbol, got, want)
		}
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
