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
// its pinned fleet (regression).
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

// sp-7zoq reclaim planner (Mandate 5, parametrized): reclaimPinnedForFloor re-dedicates already-pinned
// delivery hulls to "contract" ONLY for the shortfall the fresh reserve could not cover — Floor minus
// the already-contract hulls minus the hulls freshly reserved this launch — drawn from Pinned in stable
// order, capped at the shortfall (never over-dedicates past the floor) and at how many pinned hulls
// exist. It NEVER un-dedicates a hull to the pool; every returned symbol is one to fleet-assign to
// "contract". This is the deferred sp-mzdk temp-un-pin, done as a re-dedication.
func TestReclaimPinnedForFloor_ReclaimsOnlyTheShortfallToContract(t *testing.T) {
	cases := []struct {
		name            string
		budget          deliveryPinBudget
		freshlyReserved int
		want            []string
	}{
		{
			name:            "fresh reserve already meets the floor reclaims nothing",
			budget:          deliveryPinBudget{Floor: 2, ContractDedicated: 0, Pinned: []string{"P-1", "P-2"}},
			freshlyReserved: 2,
			want:            nil,
		},
		{
			name:            "already-contract hulls count toward the floor so nothing is reclaimed",
			budget:          deliveryPinBudget{Floor: 3, ContractDedicated: 3, Pinned: []string{"P-1"}},
			freshlyReserved: 0,
			want:            nil,
		},
		{
			name:            "shortfall reclaims exactly that many pinned hulls in stable order",
			budget:          deliveryPinBudget{Floor: 6, ContractDedicated: 1, Pinned: []string{"P-1", "P-2", "P-3", "P-4"}},
			freshlyReserved: 2, // need 6-1-2 = 3 more; reclaim the first 3 pinned
			want:            []string{"P-1", "P-2", "P-3"},
		},
		{
			name:            "shortfall larger than the pinned pool reclaims every pinned hull, no more",
			budget:          deliveryPinBudget{Floor: 6, ContractDedicated: 0, Pinned: []string{"P-1", "P-2"}},
			freshlyReserved: 0, // need 6, only 2 pinned exist
			want:            []string{"P-1", "P-2"},
		},
		{
			name:            "no pinned hulls to reclaim yields nothing even under shortfall",
			budget:          deliveryPinBudget{Floor: 6, ContractDedicated: 0, Pinned: nil},
			freshlyReserved: 0,
			want:            nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, reclaimPinnedForFloor(tc.budget, tc.freshlyReserved))
		})
	}
}

// sp-7zoq reclaim wiring (port-to-port through launchDepotCoordinators + spy sink): when the census
// reports too few undedicated hulls to reach the floor but home hulls are already pinned to
// depot-delivery, the launch RECLAIMS the shortfall — re-dedicating the pinned hulls to the exclusive
// "contract" fleet (dedicated TO contract, NEVER un-dedicated to the poachable pool). Here Floor 4 with
// 1 undedicated in-pool hull (freshly reserved) + 2 already pinned: the 1 fresh reserve plus both
// reclaimed pins reach 3, capped by the pinned pool; the reclaim never re-pins the hulls it dedicates.
func TestLaunchDepotCoordinators_ReclaimsPinnedHullsToContractWhenReserveShort(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		nil,
		[]depot.Element{
			{Waypoint: "X1-J58-H1", ShipSymbol: "FREE-1"},   // undedicated in-pool → freshly reserved
			{Waypoint: "X1-J58-H2", ShipSymbol: "PINNED-1"}, // already pinned → reclaim candidate
			{Waypoint: "X1-J58-H3", ShipSymbol: "PINNED-2"}, // already pinned → reclaim candidate
		},
		nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	// Undedicated pool holds only FREE-1 (Available 1 < Floor 4); PINNED-1/2 are the reclaim pool.
	sink := &spyDepotSink{reserve: deliveryPinBudget{
		Available:         1,
		Floor:             4,
		InPool:            inPool("FREE-1"),
		ContractDedicated: 0,
		Pinned:            []string{"PINNED-1", "PINNED-2"},
	}}

	launchDepotCoordinators(context.Background(), reg, 7, sink)

	// FREE-1 is the fresh reserve; PINNED-1/2 are reclaimed — all THREE dedicated to "contract",
	// none re-pinned (the reclaimed hulls never reach the delivery-pin sink).
	require.ElementsMatch(t, []string{"FREE-1", "PINNED-1", "PINNED-2"}, sink.dedicated,
		"the fresh reserve AND the reclaimed pins are all dedicated to the contract fleet")
	pinnedShips := map[string]bool{}
	for _, d := range sink.deliveries {
		pinnedShips[d.ship] = true
	}
	require.False(t, pinnedShips["PINNED-1"] || pinnedShips["PINNED-2"],
		"a reclaimed hull is re-dedicated to contract, never re-pinned to depot-delivery (no churn)")
	require.False(t, pinnedShips["FREE-1"], "the fresh reserve hull is dedicated, never pinned")
}

// sp-mzdk + sp-7zoq wiring (port-to-port through launchDepotCoordinators + spy sink): the launch
// consults the reserve census and dispatches launchDepotDelivery for ONLY the within-budget delivery
// hulls (regression: pin cap Available-Floor still holds) — the reserved ones are never handed to the
// delivery-pin sink but are instead DEDICATED to the exclusive "contract" fleet (sp-7zoq), so they are
// poach-proof rather than left in the shared idle pool. The warehouse/stocker launches are untouched.
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
	require.Len(t, sink.deliveries, 3, "only Available-Floor = 3 delivery hulls are pinned; the floor of 2 is reserved (regression)")
	require.True(t, pinned["DLV-1"] && pinned["DLV-2"] && pinned["DLV-3"], "within-budget delivery hulls still pin")
	require.False(t, pinned["DLV-4"] || pinned["DLV-5"], "the reserved delivery hulls are never dispatched to the pin sink")
	// sp-7zoq: the reserved hulls are DEDICATED to the exclusive "contract" fleet (poach-proof), not
	// merely left undedicated — the floor count N=2 is dedicated, and the within-budget pins are not.
	require.ElementsMatch(t, []string{"DLV-4", "DLV-5"}, sink.dedicated,
		"exactly the reserved floor hulls are dedicated to the contract fleet — not the pinned ones, not more")
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

	insertDepotDeliveryHull(t, db, "HOME-A", playerID, "", "X1-J58-A", false)                         // undedicated, home
	insertDepotDeliveryHull(t, db, "HOME-B", playerID, "", "X1-J58-B", false)                         // undedicated, home
	insertDepotDeliveryHull(t, db, "FOREIGN-C", playerID, "", "X1-ZZ99-C", false)                     // undedicated but FOREIGN system
	insertDepotDeliveryHull(t, db, "PINNED-D", playerID, depot.DeliveryHullFleet, "X1-J58-D", false)  // already depot-delivery pinned (reclaim pool)
	insertDepotDeliveryHull(t, db, "CONTRACT-E", playerID, contractDedicatedFleet, "X1-J58-E", false) // already contract-dedicated (counts toward floor)
	insertDepotDeliveryHull(t, db, "CONTRACT-F", playerID, "depot-delivery", "X1-ZZ99-F", false)      // pinned but FOREIGN — not home cover

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
	// sp-7zoq census additions: an already-contract-dedicated HOME hauler counts toward the floor (so
	// the fresh reserve tops UP, never over-dedicates); a HOME depot-delivery pin is a reclaim
	// candidate; a FOREIGN pin is neither counted nor reclaimable (a reclaim must keep a HOME worker).
	require.Equal(t, 1, budget.ContractDedicated, "the home contract-dedicated hull counts toward the floor")
	require.Equal(t, []string{"PINNED-D"}, budget.Pinned, "only the HOME depot-delivery pin is a reclaim candidate — never the foreign one")
}

// sp-7zoq ACCEPTANCE (real ship repo, was sp-mzdk): after a depot topology that declares EVERY home
// hauler as a delivery hull is launched with a floor of 2, the reserve floor DEDICATES 2 (== floor)
// of them to the exclusive "contract" fleet instead of merely leaving them undedicated. The dedicated
// reserve is POACH-PROOF — it drops OUT of the shared idle pool (FindIdleLightHaulers) that the goods
// factory / arb / reconciler-tier1 draw from — yet STILL serves contracts through the coordinator's
// OWN dedicated lookup (FindIdleShipsByFleet("contract")): dedication is TO contract, not a freeze, so
// there is no self-starvation. The delivery hulls ABOVE the floor ARE pinned to depot-delivery
// (regression: the pin cap Available-Floor still holds).
func TestLaunchDepotCoordinators_ReservedHaulerDedicatedToContractPoachProof(t *testing.T) {
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
	// Precondition: all five are in the shared grab pool (the poachable set) — undedicated is poachable.
	_, before, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.Subset(t, before, allHulls,
		"precondition: every undedicated home hauler is in the shared idle pool ANY coordinator can grab")

	// Uncrewed warehouse anchor (satisfies the depot invariant without launching a coordinator) + the
	// five declared delivery hulls that WOULD pin the whole pool.
	c, err := depot.NewContractDepot("j58", []depot.Element{{Waypoint: "X1-J58-WH"}}, nil, deliveryHulls, nil)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})

	launchDepotCoordinators(context.Background(), reg, playerID, s)

	// Count the reserve floor's result directly off the persisted dedication tags.
	contractDedicated, pinnedCount := 0, 0
	for _, sym := range allHulls {
		var got struct{ DedicatedFleet string }
		require.NoError(t, db.Table("ships").Select("dedicated_fleet").
			Where("ship_symbol = ? AND player_id = ?", sym, playerID).Scan(&got).Error)
		switch got.DedicatedFleet {
		case contractDedicatedFleet:
			contractDedicated++
		case depot.DeliveryHullFleet:
			pinnedCount++
		}
	}
	// ACCEPTANCE 1: >= min_home_contract_workers hulls are DedicatedFleet="contract" (exclusive), NOT undedicated.
	require.GreaterOrEqual(t, contractDedicated, 2, "the floor dedicates >= min_home_contract_workers hulls to the contract fleet")
	require.Equal(t, 2, contractDedicated, "exactly the floor count N=2 is dedicated — no over-dedication")
	// REGRESSION: the floor still caps depot-delivery pinning at Available(5) - Floor(2) = 3.
	require.Equal(t, 3, pinnedCount, "Available(5) - Floor(2) = 3 delivery hulls are pinned; 2 are the contract reserve")

	// POACH-PROOF: the dedicated reserve is OUT of the shared idle pool the factory/arb/reconciler draw from.
	_, generalPool, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.NotContains(t, generalPool, "DLV-D", "a contract-dedicated reserve hull is excluded from the shared idle pool (poach-proof)")
	require.NotContains(t, generalPool, "DLV-E", "a contract-dedicated reserve hull is excluded from the shared idle pool (poach-proof)")
	require.NotContains(t, generalPool, "DLV-A", "a pinned delivery hull is excluded from the shared idle pool")

	// NO SELF-STARVATION: the reserve is dedicated TO contract, so the contract coordinator's OWN
	// dedicated lookup still surfaces it to source an unbuffered-good contract.
	_, contractPool, err := appContract.FindIdleShipsByFleet(context.Background(), pid, s.shipRepo, contractDedicatedFleet)
	require.NoError(t, err)
	require.Subset(t, contractPool, []string{"DLV-D", "DLV-E"},
		"the dedicated reserve still serves contracts via the coordinator's own FindIdleShipsByFleet(\"contract\") lookup")
}
