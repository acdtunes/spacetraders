package shared

import "testing"

// ChartPriority decides the SEQUENCE of an exhaustive tour, never its
// membership. These tests pin the relative order rather than the raw numbers,
// because the numbers are an implementation detail and the order is the
// contract.
//
// The first gap is the one that matters most. A charted shipyard makes its
// system buyable, which funds local spares, which stage more seeds; a charted
// market lets a parked scanner start producing trade data while the tour
// continues. Everything after that is "not yet known to be worth anything".
func TestChartPriority_ShipyardsThenMarketsThenTheRestThenBarren(t *testing.T) {
	station := ChartPriority(WaypointTypeOrbitalStation)
	market := ChartPriority(WaypointTypeFuelStation)
	unproven := ChartPriority(WaypointTypeGasGiant)
	barren := ChartPriority(WaypointTypeAsteroid)

	if !(station < market) {
		t.Fatalf("ORBITAL_STATION(%d) must sort before market-bearing types(%d): stations hold 523 of the 567 shipyards ever seen, and a yard is what makes a system buyable",
			station, market)
	}
	if !(market < unproven) {
		t.Fatalf("market-bearing types(%d) must sort before GAS_GIANT(%d): a FUEL_STATION is 1129-for-1129 a market, a gas giant is 72 of 546",
			market, unproven)
	}
	if !(unproven < barren) {
		t.Fatalf("GAS_GIANT(%d) must sort before ASTEROID(%d): 13%% beats 0-for-3297", unproven, barren)
	}
}

// Every type the census shows reliably carrying a market belongs in ONE tier
// together, behind the shipyards and ahead of everything unproven. Asserting
// them individually is what stops one being quietly dropped to the tail later.
func TestChartPriority_AllMarketBearingTypesShareTheMarketTier(t *testing.T) {
	station := ChartPriority(WaypointTypeOrbitalStation)
	unproven := ChartPriority(WaypointTypeGasGiant)

	for _, waypointType := range []string{
		WaypointTypeMoon,               // 1521 markets, and the only 44 yards not on a station
		WaypointTypePlanet,             // 1252 of 1501
		WaypointTypeFuelStation,        // 1129 of 1129
		WaypointTypeJumpGate,           // 373 of 373
		WaypointTypeAsteroidBase,       // 345 of 345
		WaypointTypeEngineeredAsteroid, // 22 of 22
	} {
		got := ChartPriority(waypointType)
		if got <= station {
			t.Fatalf("ChartPriority(%q) = %d, want > ORBITAL_STATION(%d): shipyards still come first", waypointType, got, station)
		}
		if got >= unproven {
			t.Fatalf("ChartPriority(%q) = %d, want < GAS_GIANT(%d): this type reliably carries a market, so a scanner can be parked on it",
				waypointType, got, unproven)
		}
	}
}

// ASTEROID is the ONLY type in the barren tier. The two types that look like
// asteroids are not asteroids: ASTEROID_BASE is 345-for-345 market-bearing and
// ENGINEERED_ASTEROID is 22-for-22, so an implementation matching on the
// ASTEROID prefix rather than the exact type would bury 367 guaranteed markets
// behind every barren rock in the system.
func TestChartPriority_OnlyTheBareAsteroidIsRankedBarren(t *testing.T) {
	barren := ChartPriority(WaypointTypeAsteroid)

	for _, waypointType := range []string{
		WaypointTypeAsteroidBase,
		WaypointTypeEngineeredAsteroid,
		WaypointTypeMoon,
		WaypointTypePlanet,
		WaypointTypeFuelStation,
		WaypointTypeOrbitalStation,
		WaypointTypeGasGiant,
		WaypointTypeJumpGate,
	} {
		if got := ChartPriority(waypointType); got >= barren {
			t.Fatalf("ChartPriority(%q) = %d, want < ASTEROID(%d): only the bare ASTEROID is 0-for-3297",
				waypointType, got, barren)
		}
	}
}

// An unrecognised type sorts with the unproven, NOT with the barren. Not being
// recognised is not evidence of being worthless, and a type the server adds
// later should not land behind fifty asteroids on a guess.
func TestChartPriority_AnUnknownTypeIsNotTreatedAsBarren(t *testing.T) {
	barren := ChartPriority(WaypointTypeAsteroid)
	market := ChartPriority(WaypointTypeFuelStation)

	for name, got := range map[string]int{
		"NEBULA_ANOMALY": ChartPriority("NEBULA_ANOMALY"),
		"the empty type": ChartPriority(""),
	} {
		if got >= barren {
			t.Fatalf("ChartPriority(%s) = %d, want < ASTEROID(%d): an unknown type has not earned the tail", name, got, barren)
		}
		if got <= market {
			t.Fatalf("ChartPriority(%s) = %d, want > the market tier(%d): it has not earned a scanner's priority either", name, got, market)
		}
	}
}
