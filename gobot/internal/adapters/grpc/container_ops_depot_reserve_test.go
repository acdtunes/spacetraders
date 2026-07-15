package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// deliveryIntent is a tiny constructor for a delivery-hull launch intent, so the pure reserve-floor
// tests read as "these ships are declared delivery hulls, in this order".
func deliveryIntent(ship string) depotLaunchIntent {
	return depotLaunchIntent{role: depot.RoleDeliveryHull, shipSymbol: ship, targetWaypoint: "X1-J58-" + ship}
}

// inPool turns a list of ship symbols into a deliveryPinBudget.InPool membership set.
func inPool(ships ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ships {
		m[s] = true
	}
	return m
}

// sp-mzdk core guard: a topology that would pin the WHOLE home general pool as depot-delivery is
// capped at the floor — pinning stops at (Available - Floor), and the LAST declared delivery hulls
// are RESERVED (left undedicated + available to the contract grab) so an unbuffered-good contract
// still has a sourcing worker. The within-budget hulls are NOT reserved, so buffered delivery keeps
// its pinned fleet (regression). This is the exact live incident: 5 haulers, floor 2 -> reserve 2.
func TestReserveHomeContractWorkers_CapsFreshPinsAtFloorLeavingReserveUndedicated(t *testing.T) {
	intents := []depotLaunchIntent{
		deliveryIntent("DLV-1"), deliveryIntent("DLV-2"), deliveryIntent("DLV-3"),
		deliveryIntent("DLV-4"), deliveryIntent("DLV-5"),
	}
	budget := deliveryPinBudget{Available: 5, Floor: 2, InPool: inPool("DLV-1", "DLV-2", "DLV-3", "DLV-4", "DLV-5")}

	reserved := reserveHomeContractWorkers(intents, budget)

	require.Len(t, reserved, 2, "with 5 home haulers and a floor of 2, exactly 2 (== floor) stay undedicated")
	require.True(t, reserved["DLV-4"] && reserved["DLV-5"],
		"the LAST declared delivery hulls are reserved once the Available-Floor budget is spent")
	for _, pinned := range []string{"DLV-1", "DLV-2", "DLV-3"} {
		require.False(t, reserved[pinned],
			"delivery hulls within the budget still pin (buffered delivery keeps its fleet) — %s", pinned)
	}
}

// sp-mzdk degenerate budgets (Mandate 5, parametrized): floor 0 pins everything; fewer haulers than
// the floor reserves every fresh conversion (never pins a negative); a delivery hull NOT in the home
// general pool — already depot-delivery pinned, foreign-fleet, or off-home — is never reserved even
// when the budget is exhausted (a reload re-pins the existing fleet unchanged); and a non-delivery
// intent is never the floor's concern.
func TestReserveHomeContractWorkers_DegenerateBudgets(t *testing.T) {
	warehouseIntent := depotLaunchIntent{role: depot.RoleWarehouse, shipSymbol: "WH-1", targetWaypoint: "X1-J58-WH"}
	cases := []struct {
		name     string
		intents  []depotLaunchIntent
		budget   deliveryPinBudget
		reserved []string // ships expected in the reserved set (order-independent)
	}{
		{
			name:     "floor zero reserves nothing",
			intents:  []depotLaunchIntent{deliveryIntent("A"), deliveryIntent("B"), deliveryIntent("C")},
			budget:   deliveryPinBudget{Available: 3, Floor: 0, InPool: inPool("A", "B", "C")},
			reserved: nil,
		},
		{
			name:     "fewer haulers than the floor reserves every fresh conversion",
			intents:  []depotLaunchIntent{deliveryIntent("A"), deliveryIntent("B"), deliveryIntent("C")},
			budget:   deliveryPinBudget{Available: 3, Floor: 6, InPool: inPool("A", "B", "C")},
			reserved: []string{"A", "B", "C"},
		},
		{
			name: "already-pinned and foreign hulls are never reserved even at zero budget",
			// PINNED-B / FOREIGN-C are declared delivery hulls but NOT in the undedicated pool, so
			// they pin regardless (a reload re-positions the existing fleet); only the in-pool A is
			// gated, and with Available<=Floor it is reserved.
			intents:  []depotLaunchIntent{deliveryIntent("A"), deliveryIntent("PINNED-B"), deliveryIntent("FOREIGN-C")},
			budget:   deliveryPinBudget{Available: 1, Floor: 6, InPool: inPool("A")},
			reserved: []string{"A"},
		},
		{
			name:     "a non-delivery intent is never reserved",
			intents:  []depotLaunchIntent{warehouseIntent, deliveryIntent("A")},
			budget:   deliveryPinBudget{Available: 1, Floor: 6, InPool: inPool("WH-1", "A")},
			reserved: []string{"A"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			reserved := reserveHomeContractWorkers(tc.intents, tc.budget)
			require.Len(t, reserved, len(tc.reserved))
			for _, ship := range tc.reserved {
				require.True(t, reserved[ship], "expected %s reserved", ship)
			}
		})
	}
}

// sp-mzdk wiring (port-to-port through launchDepotCoordinators + spy sink): the launch consults the
// reserve census and dispatches launchDepotDelivery for ONLY the within-budget delivery hulls — the
// reserved ones are never handed to the delivery-pin sink at all, so they stay undedicated. The
// warehouse/stocker launches are untouched by the floor.
func TestLaunchDepotCoordinators_ReservesFloorFromDeliveryPinning(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		[]depot.Element{
			{Waypoint: "X1-J58-H1", ShipSymbol: "DLV-1"}, {Waypoint: "X1-J58-H2", ShipSymbol: "DLV-2"},
			{Waypoint: "X1-J58-H3", ShipSymbol: "DLV-3"}, {Waypoint: "X1-J58-H4", ShipSymbol: "DLV-4"},
			{Waypoint: "X1-J58-H5", ShipSymbol: "DLV-5"},
		},
		nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	sink := &spyDepotSink{reserve: deliveryPinBudget{Available: 5, Floor: 2, InPool: inPool("DLV-1", "DLV-2", "DLV-3", "DLV-4", "DLV-5")}}

	launchDepotCoordinators(context.Background(), reg, 7, sink)

	pinned := map[string]bool{}
	for _, d := range sink.deliveries {
		pinned[d.ship] = true
	}
	require.Len(t, sink.deliveries, 3, "only Available-Floor = 3 delivery hulls are pinned; the floor of 2 is reserved")
	require.True(t, pinned["DLV-1"] && pinned["DLV-2"] && pinned["DLV-3"], "within-budget delivery hulls still pin")
	require.False(t, pinned["DLV-4"] || pinned["DLV-5"], "the reserved delivery hulls are never dispatched to the pin sink")
	require.Len(t, sink.warehouses, 1, "the warehouse launch is untouched by the reserve floor")
	require.Len(t, sink.stockers, 1, "the stocker launch is untouched by the reserve floor")
}

// sp-mzdk config resolution (Mandate 5, parametrized): the reserve floor resolves live>launch>default
// — a positive live (tune) value wins; else the config.yaml launch value; else the documented
// default. A zeroed live value (the `tune ... 0` revert) falls through to launch/default.
func TestResolveMinHomeContractWorkers_LiveOverLaunchOverDefault(t *testing.T) {
	cases := []struct {
		name   string
		live   map[string]interface{}
		launch int
		want   int
	}{
		{name: "live tune value wins over launch and default", live: map[string]interface{}{"min_home_contract_workers": 9}, launch: 4, want: 9},
		{name: "no live value falls to launch (config.yaml)", live: nil, launch: 4, want: 4},
		{name: "zeroed live value reverts to launch", live: map[string]interface{}{"min_home_contract_workers": 0}, launch: 4, want: 4},
		{name: "neither live nor launch falls to the documented default", live: nil, launch: 0, want: MinHomeContractWorkersDefault},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveMinHomeContractWorkers(tc.live, tc.launch))
		})
	}
}

// sp-mzdk census correctness (real ship repo): the reserve floor is measured against ONLY the
// undedicated HOME general haulers — a hauler in a FOREIGN system (a would-be cross-gate grab) and a
// hull ALREADY pinned to depot-delivery are both excluded from the count, so the floor neither
// over-counts a foreign hull as home cover nor double-counts an already-pinned hull. The live floor
// (config.yaml launch tier here) flows into the census.
func TestHomeContractWorkerReserve_CountsOnlyUndedicatedHomeGeneralHaulers(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	s.contractConfig.MinHomeContractWorkers = 3

	insertDepotDeliveryHull(t, db, "HOME-A", playerID, "", "X1-J58-A", false)                        // undedicated, home
	insertDepotDeliveryHull(t, db, "HOME-B", playerID, "", "X1-J58-B", false)                        // undedicated, home
	insertDepotDeliveryHull(t, db, "FOREIGN-C", playerID, "", "X1-ZZ99-C", false)                    // undedicated but FOREIGN system
	insertDepotDeliveryHull(t, db, "PINNED-D", playerID, depot.DeliveryHullFleet, "X1-J58-D", false) // already depot-delivery pinned

	// Registry whose delivery hubs sit in X1-J58, so the home region is X1-J58.
	c, err := depot.NewContractDepot("j58", []depot.Element{{Waypoint: "X1-J58-WH"}}, nil,
		[]depot.Element{{Waypoint: "X1-J58-H1"}}, nil)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})

	budget := s.homeContractWorkerReserve(context.Background(), reg, playerID)

	require.Equal(t, 3, budget.Floor, "the config.yaml launch floor flows into the census")
	require.Equal(t, 2, budget.Available, "only the two undedicated HOME general haulers count toward the reserve")
	require.True(t, budget.InPool["HOME-A"] && budget.InPool["HOME-B"], "both home haulers are in the pool")
	require.False(t, budget.InPool["FOREIGN-C"], "a foreign-system hauler is not home cover (NOT a cross-gate grab)")
	require.False(t, budget.InPool["PINNED-D"], "an already depot-delivery pinned hull is not in the undedicated pool")
}

// sp-mzdk ACCEPTANCE (real ship repo): after a depot topology that declares EVERY home hauler as a
// delivery hull is launched with a floor of 2, the REAL census counts the undedicated home general
// pool, the reserve floor keeps 2 of them undedicated, and those 2 remain grabbable by the contract
// coordinator's own pool (FindIdleLightHaulers) to source an unbuffered-good contract — NOT starved,
// NOT a cross-gate foreign grab. The delivery hulls ABOVE the floor ARE pinned to depot-delivery, so
// buffered delivery still routes to them (regression). This is the live incident, inverted.
func TestLaunchDepotCoordinators_ReservedHaulerStaysAvailableToContractGrab(t *testing.T) {
	s, db, playerID, _ := newDepotDeliveryTestServer(t)
	s.contractConfig.MinHomeContractWorkers = 2
	pid := shared.MustNewPlayerID(playerID)

	// Five undedicated home general haulers, idle at their hubs in the depot's home system (X1-J58).
	// Symbols avoid the "-1" suffix, which IsCommandHull reserves for the command frigate.
	allHulls := []string{"DLV-A", "DLV-B", "DLV-C", "DLV-D", "DLV-E"}
	deliveryHulls := []depot.Element{}
	for _, sym := range allHulls {
		hub := "X1-J58-" + sym
		insertDepotDeliveryHull(t, db, sym, playerID, "", hub, false) // undedicated, idle, AT its hub
		deliveryHulls = append(deliveryHulls, depot.Element{Waypoint: hub, ShipSymbol: sym})
	}
	// Precondition: all five are in the contract coordinator's general grab pool.
	_, before, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.Subset(t, before, allHulls,
		"precondition: every undedicated home hauler is grabbable by the contract coordinator")

	// Uncrewed warehouse anchor (satisfies the depot invariant without launching a coordinator) + the
	// five declared delivery hulls that WOULD pin the whole pool.
	c, err := depot.NewContractDepot("j58", []depot.Element{{Waypoint: "X1-J58-WH"}}, nil, deliveryHulls, nil)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})

	launchDepotCoordinators(context.Background(), reg, playerID, s)

	// The reserve floor kept 2 home haulers undedicated and grabbable (the LAST two declared: D, E).
	_, generalPool, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.Subset(t, generalPool, []string{"DLV-D", "DLV-E"},
		"the reserved home haulers stay in the contract grab pool for unbuffered-good sourcing")
	require.NotContains(t, generalPool, "DLV-A", "a pinned delivery hull is excluded from the grab pool")

	// The delivery hulls ABOVE the floor were pinned to depot-delivery (buffered delivery routes to them).
	pinnedCount := 0
	for _, sym := range allHulls {
		var got struct{ DedicatedFleet string }
		require.NoError(t, db.Table("ships").Select("dedicated_fleet").
			Where("ship_symbol = ? AND player_id = ?", sym, playerID).Scan(&got).Error)
		if got.DedicatedFleet == depot.DeliveryHullFleet {
			pinnedCount++
		}
	}
	require.Equal(t, 3, pinnedCount, "Available(5) - Floor(2) = 3 delivery hulls are pinned; 2 stay reserved")
}
