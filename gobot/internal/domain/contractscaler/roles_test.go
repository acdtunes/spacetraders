package contractscaler

import (
	"reflect"
	"sort"
	"testing"
)

// sortedCopy returns a symbol-sorted copy so role-order assertions do not depend
// on input order — a test-only helper (moved out of production roles.go in C2b).
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// A home-system fixture with the invariant band geometry (star at origin): a
// central cluster of contract sinks (importers), a far ring of raw exporters,
// and a J-outlier consumer. Names are ARBITRARY per-era symbols; the lookup must
// resolve roles by MARKET ROLE + geometry, never by letter/number.
func eraOneMarkets() []WaypointMarket {
	return []WaypointMarket{
		// central sinks (import finished goods) — inner band (<=300 from star)
		{Symbol: "X1-AA-C39", X: 120, Y: 40, Imports: []string{"ELECTRONICS", "CLOTHING"}},
		{Symbol: "X1-AA-A3", X: -80, Y: 90, Imports: []string{"MEDICINE"}},
		{Symbol: "X1-AA-K83", X: 60, Y: -150, Imports: []string{"FUEL", "MACHINERY", "FOOD"}},
		// far ring raw exporters (ores/drugs source) — beyond the central band
		{Symbol: "X1-AA-B12", X: 300, Y: 180, Exports: []string{"IRON_ORE", "COPPER_ORE"}},
		{Symbol: "X1-AA-I7", X: -320, Y: 60, Exports: []string{"PRECIOUS_STONES"}},
		// far J outlier consumer (imports finished goods) ~720 out
		{Symbol: "X1-AA-J58", X: 500, Y: 520, Imports: []string{"POLYNUCLEOTIDES", "AMMONIA"}},
	}
}

func TestResolveRoles_ClassifiesCentralParksByInnerBandImport(t *testing.T) {
	roles := ResolveRoles(eraOneMarkets())

	// central parks = inner-band importers, deterministically ordered.
	want := []string{"X1-AA-A3", "X1-AA-C39", "X1-AA-K83"}
	if !reflect.DeepEqual(sortedCopy(roles.CentralParks), want) {
		t.Fatalf("central parks = %v, want the three inner-band sinks %v", roles.CentralParks, want)
	}
}

func TestResolveRoles_FarSourcesAreFarBandExporters(t *testing.T) {
	roles := ResolveRoles(eraOneMarkets())

	want := []string{"X1-AA-B12", "X1-AA-I7"}
	if !reflect.DeepEqual(sortedCopy(roles.FarSources), want) {
		t.Fatalf("far sources = %v, want the far-ring exporters %v", roles.FarSources, want)
	}
}

func TestResolveRoles_FarSinkIsFarthestImporter(t *testing.T) {
	roles := ResolveRoles(eraOneMarkets())

	if roles.FarSink != "X1-AA-J58" {
		t.Fatalf("far sink = %q, want the J-outlier consumer X1-AA-J58", roles.FarSink)
	}
}

// The SAME roles must resolve when every waypoint symbol regenerates — the "names
// and numbers both regenerate each era" invariant. Only geometry + trade role
// carry the roles, so a renamed fixture with identical geometry resolves
// identically (by position in the role slices).
func TestResolveRoles_IsIndifferentToEraNames(t *testing.T) {
	renamed := []WaypointMarket{
		{Symbol: "X9-ZZ-Q1", X: 120, Y: 40, Imports: []string{"ELECTRONICS", "CLOTHING"}},
		{Symbol: "X9-ZZ-Q2", X: -80, Y: 90, Imports: []string{"MEDICINE"}},
		{Symbol: "X9-ZZ-Q3", X: 60, Y: -150, Imports: []string{"FUEL", "MACHINERY", "FOOD"}},
		{Symbol: "X9-ZZ-R1", X: 300, Y: 180, Exports: []string{"IRON_ORE"}},
		{Symbol: "X9-ZZ-R2", X: -320, Y: 60, Exports: []string{"PRECIOUS_STONES"}},
		{Symbol: "X9-ZZ-J1", X: 500, Y: 520, Imports: []string{"POLYNUCLEOTIDES"}},
	}
	roles := ResolveRoles(renamed)

	if len(roles.CentralParks) != 3 || len(roles.FarSources) != 2 || roles.FarSink != "X9-ZZ-J1" {
		t.Fatalf("renamed era resolved to different roles: parks=%v sources=%v sink=%q",
			roles.CentralParks, roles.FarSources, roles.FarSink)
	}
}

// A pure raw producer (exports only, no imports) in the inner band is NOT a
// contract sink — parks are consumers (importers), not factories.
func TestResolveRoles_InnerBandPureExporterIsNotAPark(t *testing.T) {
	markets := []WaypointMarket{
		{Symbol: "X1-AA-C39", X: 120, Y: 40, Imports: []string{"ELECTRONICS"}},
		{Symbol: "X1-AA-F1", X: 50, Y: 50, Exports: []string{"FUEL"}}, // inner-band exporter, no imports
	}
	roles := ResolveRoles(markets)

	if !reflect.DeepEqual(roles.CentralParks, []string{"X1-AA-C39"}) {
		t.Fatalf("central parks = %v, want only the inner-band importer", roles.CentralParks)
	}
}
