package contractscaler

import (
	"reflect"
	"testing"
)

// The coord-dedup collapses co-located parks (a planet and its moons share one coordinate)
// to a single representative so a homing spread never wastes two slots on one planet. Its
// input variations: the highest-demand waypoint wins its location; equal-demand ties resolve
// to the smallest symbol (a pure function, never map/slice order); empty input → empty map.
func TestDedupeCoLocatedParks(t *testing.T) {
	tests := []struct {
		name  string
		parks []CentralPark
		want  map[string]float64
	}{
		{
			name: "co-located pair collapses to the highest-demand representative",
			parks: []CentralPark{
				{Symbol: "X1-UM5-G47", X: 54, Y: -33, Demand: 180},
				{Symbol: "X1-UM5-G49", X: 54, Y: -33, Demand: 340},
				{Symbol: "X1-UM5-K83", X: 8, Y: 104, Demand: 260},
			},
			want: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260},
		},
		{
			name: "equal-demand tie broken by smallest symbol",
			parks: []CentralPark{
				{Symbol: "X1-UM5-D41", X: -73, Y: -39, Demand: 120},
				{Symbol: "X1-UM5-D40", X: -73, Y: -39, Demand: 120},
			},
			want: map[string]float64{"X1-UM5-D40": 120},
		},
		{
			name:  "empty input yields empty map",
			parks: nil,
			want:  map[string]float64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DedupeCoLocatedParks(tc.parks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("deduped demand = %v, want %v", got, tc.want)
			}
		})
	}
}

// era4CentralParks is the LIVE era-4 X1-UM5 central-band importer set (dist<=300,
// player-4 market_data, import-volume demand) — the real per-planet co-located groups.
// Symbols regenerate every era; this fixture pins the CURRENT era to validate the
// role/band+dedup pipeline against real data, never as a hardcode.
func era4CentralParks() []CentralPark {
	return []CentralPark{
		{Symbol: "X1-UM5-G49", X: 54, Y: -33, Demand: 340}, {Symbol: "X1-UM5-G47", X: 54, Y: -33, Demand: 180},
		{Symbol: "X1-UM5-K83", X: 8, Y: 104, Demand: 260},
		{Symbol: "X1-UM5-F46", X: -74, Y: 13, Demand: 240}, {Symbol: "X1-UM5-F45", X: -74, Y: 13, Demand: 180},
		{Symbol: "X1-UM5-H55", X: -33, Y: -31, Demand: 240}, {Symbol: "X1-UM5-H52", X: -33, Y: -31, Demand: 200}, {Symbol: "X1-UM5-H53", X: -33, Y: -31, Demand: 12},
		{Symbol: "X1-UM5-E43", X: 50, Y: 24, Demand: 240}, {Symbol: "X1-UM5-E44", X: 50, Y: 24, Demand: 200}, {Symbol: "X1-UM5-E42", X: 50, Y: 24, Demand: 140},
		{Symbol: "X1-UM5-A1", X: -22, Y: 15, Demand: 146}, {Symbol: "X1-UM5-A4", X: -22, Y: 15, Demand: 140}, {Symbol: "X1-UM5-A3", X: -22, Y: 15, Demand: 120}, {Symbol: "X1-UM5-A2", X: -22, Y: 15, Demand: 12},
		{Symbol: "X1-UM5-D41", X: -73, Y: -39, Demand: 120}, {Symbol: "X1-UM5-D40", X: -73, Y: -39, Demand: 120},
		{Symbol: "X1-UM5-C38", X: 149, Y: -36, Demand: 52},
	}
}

// The rewritten validation anchor (Path B, team-lead approved 2026-07-22): run against
// the CURRENT era's real central-park data, the coord-dedup must yield ONE demand-ranked
// representative per distinct LOCATION — the 18 co-located waypoints collapse to 8 distinct
// homing targets, each the highest-demand of its planet. (Supersedes the stale p-median
// {H52,E43,K83,D41,F46,G49} doc anchor, which import-volume demand cannot reproduce.)
func TestDedupeCoLocatedParks_Era4YieldsDistinctLocationRepresentatives(t *testing.T) {
	got := DedupeCoLocatedParks(era4CentralParks())

	want := map[string]float64{
		"X1-UM5-G49": 340,
		"X1-UM5-K83": 260,
		"X1-UM5-F46": 240,
		"X1-UM5-H55": 240,
		"X1-UM5-E43": 240,
		"X1-UM5-A1":  146,
		"X1-UM5-D40": 120,
		"X1-UM5-C38": 52,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("era-4 deduped central parks = %v,\nwant one highest-demand rep per distinct location %v", got, want)
	}

	// Every representative must sit at a DISTINCT coordinate — the spatial-spread invariant.
	coords := map[[2]float64]string{}
	bySymbol := map[string][2]float64{}
	for _, p := range era4CentralParks() {
		bySymbol[p.Symbol] = [2]float64{p.X, p.Y}
	}
	for sym := range got {
		c := bySymbol[sym]
		if other, clash := coords[c]; clash {
			t.Fatalf("representatives %s and %s share coordinate %v — dedup failed to spread across distinct locations", sym, other, c)
		}
		coords[c] = sym
	}
}
