package depot

import (
	"sort"
	"testing"
)

// toHubsOf collects the destination hubs of a migration set, sorted, so a test asserts WHERE
// idle hulls were sent without depending on slice order.
func toHubsOf(migrations []Migration) []string {
	hubs := make([]string, 0, len(migrations))
	for _, m := range migrations {
		hubs = append(hubs, m.ToHub)
	}
	sort.Strings(hubs)
	return hubs
}

// The demand-driven rebalance MECHANISM: because contracts are serialized (one
// active at a time), "rebalance" means keeping IDLE delivery hulls optimally PRE-POSITIONED across
// hubs for the NEXT contract — idle hulls migrate toward hubs with rising observed contract
// arrival and away from cold ones, so fewer hulls cover more hot clusters with less inter-contract
// repositioning latency. PlanRebalance is the PURE decision: (idle placements x observed per-hub
// arrivals x policy knobs) -> migrations. The POLICY (cap, threshold) is CONFIG the economy-analyst
// tunes; the zero policy is OFF (regression-safe: rebalancing is opt-in). The observation source
// and the migration executor are the deferred wiring half — this function bakes in neither.
func TestPlanRebalance_MigratesIdleHullsTowardHotHubs(t *testing.T) {
	cases := []struct {
		name        string
		idle        []Element
		arrivals    HubArrivals
		policy      RebalancePolicy
		wantToHubs  []string          // destination hubs of the proposed migrations (sorted)
		wantMoved   map[string]string // shipSymbol -> ToHub, for the specific-hull assertions
		wantNoMoves bool
	}{
		{
			// THE MECHANISM: an idle hull sitting at a cold hub migrates to the hot, uncovered hub.
			name:       "idle hull migrates from a cold hub to a hot uncovered hub",
			idle:       []Element{{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-1"}},
			arrivals:   HubArrivals{"X1-VB74-HOT": 10, "X1-VB74-COLD": 0},
			policy:     RebalancePolicy{MaxMigrations: 3},
			wantToHubs: []string{"X1-VB74-HOT"},
			wantMoved:  map[string]string{"DLV-1": "X1-VB74-HOT"},
		},
		{
			// The cap knob: three idle hulls, three uncovered hot hubs, but MaxMigrations=2 moves
			// only the two hottest — a config ceiling on churn per pass.
			name: "MaxMigrations caps the number of hulls moved to the hottest hubs",
			idle: []Element{
				{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-1"},
				{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-2"},
				{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-3"},
			},
			arrivals:   HubArrivals{"X1-VB74-H1": 10, "X1-VB74-H2": 9, "X1-VB74-H3": 8, "X1-VB74-COLD": 0},
			policy:     RebalancePolicy{MaxMigrations: 2},
			wantToHubs: []string{"X1-VB74-H1", "X1-VB74-H2"}, // the two HOTTEST uncovered hubs
		},
		{
			// Regression-safe default: the zero policy (MaxMigrations 0) proposes nothing, so an
			// unconfigured / degraded deployment never migrates — rebalancing is strictly opt-in.
			name:        "disabled by default: MaxMigrations 0 proposes no migration",
			idle:        []Element{{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-1"}},
			arrivals:    HubArrivals{"X1-VB74-HOT": 10, "X1-VB74-COLD": 0},
			policy:      RebalancePolicy{MaxMigrations: 0},
			wantNoMoves: true,
		},
		{
			// The anti-churn threshold: the target hub is hotter, but not by MinArrivalGap, so the
			// hull stays put — small demand swings don't thrash the fleet.
			name:        "gap below MinArrivalGap blocks the migration",
			idle:        []Element{{Waypoint: "X1-VB74-WARM", ShipSymbol: "DLV-1"}},
			arrivals:    HubArrivals{"X1-VB74-HOT": 10, "X1-VB74-WARM": 8},
			policy:      RebalancePolicy{MaxMigrations: 3, MinArrivalGap: 5}, // gap 2 < 5
			wantNoMoves: true,
		},
		{
			// Never strip a hotter hub to cover a cooler one: the only idle hull already sits at the
			// HOT hub (covered); the uncovered hub is COOLER, so moving there would uncover demand.
			name:        "does not uncover a hotter hub to cover a cooler one",
			idle:        []Element{{Waypoint: "X1-VB74-HOT", ShipSymbol: "DLV-1"}},
			arrivals:    HubArrivals{"X1-VB74-HOT": 10, "X1-VB74-COOL": 5},
			policy:      RebalancePolicy{MaxMigrations: 3},
			wantNoMoves: true,
		},
		{
			name:        "no idle hulls: nothing to migrate",
			idle:        nil,
			arrivals:    HubArrivals{"X1-VB74-HOT": 10},
			policy:      RebalancePolicy{MaxMigrations: 3},
			wantNoMoves: true,
		},
		{
			name:        "no observed arrivals: no hot hub to migrate toward",
			idle:        []Element{{Waypoint: "X1-VB74-COLD", ShipSymbol: "DLV-1"}},
			arrivals:    nil,
			policy:      RebalancePolicy{MaxMigrations: 3},
			wantNoMoves: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			migrations := PlanRebalance(tc.idle, tc.arrivals, tc.policy)

			if tc.wantNoMoves {
				if len(migrations) != 0 {
					t.Fatalf("expected NO migrations, got %+v", migrations)
				}
				return
			}
			if got := toHubsOf(migrations); !equalStrings(got, tc.wantToHubs) {
				t.Fatalf("migration destination hubs = %v, want %v (migrations: %+v)", got, tc.wantToHubs, migrations)
			}
			for ship, wantHub := range tc.wantMoved {
				if hub := toHubFor(migrations, ship); hub != wantHub {
					t.Errorf("hull %s migrated to %q, want %q", ship, hub, wantHub)
				}
			}
			// A migration must never be a no-op (source == destination) and must move a real hull.
			for _, m := range migrations {
				if m.FromHub == m.ToHub {
					t.Errorf("migration %+v is a no-op (from == to)", m)
				}
				if m.ShipSymbol == "" {
					t.Errorf("migration %+v moves no hull", m)
				}
			}
		})
	}
}

func toHubFor(migrations []Migration, ship string) string {
	for _, m := range migrations {
		if m.ShipSymbol == ship {
			return m.ToHub
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
