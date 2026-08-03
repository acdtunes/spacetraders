package contractscaler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// eraWaypointRow mirrors ONE REAL row of the daemon's waypoints table (see testdata/README.md):
// the DURABLE charted facts only — symbol, type, coordinates, charted traits, on-site fuel. No
// market-scan data, because the era-invariant anchors must resolve from the charted template
// alone (before any dock scan), which is exactly what the standby placement must survive.
type eraWaypointRow struct {
	Symbol  string   `json:"symbol"`
	Type    string   `json:"type"`
	X       float64  `json:"x"`
	Y       float64  `json:"y"`
	Traits  []string `json:"traits"`
	HasFuel bool     `json:"has_fuel"`
}

// loadEraWaypoints reads one era's REAL home-system waypoint rows as the []WaypointMarket the
// role lookup consumes. Exports/Imports stay EMPTY on purpose: an era template that only
// resolves once markets are dock-scanned is not era-invariant.
func loadEraWaypoints(t *testing.T, era string) []WaypointMarket {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", era+"_waypoints.json"))
	if err != nil {
		t.Fatalf("read %s fixture: %v", era, err)
	}
	var rows []eraWaypointRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode %s fixture: %v", era, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s fixture is empty — the fixture, not the resolver, is broken", era)
	}
	markets := make([]WaypointMarket, 0, len(rows))
	for _, row := range rows {
		markets = append(markets, WaypointMarket{
			Symbol:        row.Symbol,
			X:             row.X,
			Y:             row.Y,
			Type:          row.Type,
			Traits:        row.Traits,
			HasFuel:       row.HasFuel,
			IsMarketplace: rowHasTrait(row, "MARKETPLACE"),
		})
	}
	return markets
}

func rowHasTrait(row eraWaypointRow, trait string) bool {
	for _, t := range row.Traits {
		if t == trait {
			return true
		}
	}
	return false
}

// waypointBySymbol indexes a loaded era so a test can assert facts about a resolved anchor
// (its coordinates, whether it is fuelled) without restating the fixture.
func waypointBySymbol(markets []WaypointMarket) map[string]WaypointMarket {
	index := make(map[string]WaypointMarket, len(markets))
	for _, m := range markets {
		index[m.Symbol] = m
	}
	return index
}
