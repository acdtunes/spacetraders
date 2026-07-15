package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// spyDepotSink records the coordinator launches the depot orchestrator dispatches,
// standing in for the real container-launch infrastructure (StartWarehouse / StartStocker)
// so the wiring is proven without spawning container goroutines or requiring idle hulls.
type spyDepotSink struct {
	warehouses []depotLaunchRecord
	stockers   []depotLaunchRecord
	deliveries []depotLaunchRecord
}

type depotLaunchRecord struct {
	ship      string
	waypoint  string
	coLocated []string
	playerID  int
}

func (s *spyDepotSink) launchDepotWarehouse(_ context.Context, shipSymbol, warehouseWaypoint string, coLocatedWarehouseShips []string, playerID int) error {
	s.warehouses = append(s.warehouses, depotLaunchRecord{ship: shipSymbol, waypoint: warehouseWaypoint, coLocated: coLocatedWarehouseShips, playerID: playerID})
	return nil
}

func (s *spyDepotSink) launchDepotStocker(_ context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	s.stockers = append(s.stockers, depotLaunchRecord{ship: shipSymbol, waypoint: warehouseWaypoint, playerID: playerID})
	return nil
}

func (s *spyDepotSink) launchDepotDelivery(_ context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	s.deliveries = append(s.deliveries, depotLaunchRecord{ship: shipSymbol, waypoint: hubWaypoint, playerID: playerID})
	return nil
}

// fakeReceiptMiner is a Lane A demand-miner double that records the system it was mined for
// (to prove the depot warehouse mines DESTINATION receipts, not some other anchor) and
// returns a fixed candidate set.
type fakeReceiptMiner struct {
	rows          []persistence.DemandCandidate
	queriedSystem string
}

func (m *fakeReceiptMiner) Mine(_ context.Context, homeSystem string, _ int, _ *int, _ persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	m.queriedSystem = homeSystem
	return m.rows, nil
}

// Gap 1 (the load-bearing fix): a loaded depot registry with a declared warehouse + stocker
// must LAUNCH both coordinators — a warehouse on its hull at the warehouse waypoint, and a
// stocker pointed at that same destination warehouse waypoint as its deposit anchor. Before
// this fix the topology was inert (nothing read .Warehouses()/.Stockers() to launch anything),
// so the warehouse never filled and contract routing always fell through to the fresh-source
// fallback — zero cycle-time compression.
func TestLaunchDepotCoordinators_LaunchesWarehouseAndStockerAnchoredAtWarehouse(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		nil,
		nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	sink := &spyDepotSink{}

	launchDepotCoordinators(context.Background(), reg, 7, sink)

	require.Len(t, sink.warehouses, 1, "the depot's declared warehouse element must launch a warehouse coordinator")
	require.Equal(t, "WH-1", sink.warehouses[0].ship)
	require.Equal(t, "X1-J58-WH", sink.warehouses[0].waypoint)
	require.Equal(t, 7, sink.warehouses[0].playerID)

	require.Len(t, sink.stockers, 1, "the depot's declared stocker element must launch a stocker coordinator")
	require.Equal(t, "ST-1", sink.stockers[0].ship)
	require.Equal(t, "X1-J58-WH", sink.stockers[0].waypoint,
		"the depot stocker must deposit into the depot's destination warehouse waypoint (the anchor)")
}

// sp-9j9c #2: a depot declaring N delivery hulls at N hub waypoints must LAUNCH (position) each —
// one delivery launch per crewed delivery hull, parked at its OWN hub waypoint — so the multi-hub
// fleet is actually PRESENT at its hubs for the nearest-selection router (#1) to route each
// cluster's contract to its LOCAL hull. Before this, delivery hulls were config-only (inert):
// planDepotLaunches started nothing for them, so a declared multi-hub fleet never deployed.
func TestLaunchDepotCoordinators_PlacesEachDeliveryHullAtItsHub(t *testing.T) {
	c, err := depot.NewContractDepot(
		"vb74",
		[]depot.Element{{Waypoint: "X1-VB74-J58", ShipSymbol: "WH-1"}}, // destination warehouse (routing anchor)
		nil, // stockers
		[]depot.Element{
			{Waypoint: "X1-VB74-J58", ShipSymbol: "DLV-J58"}, // hull placed at the J58 hub (co-located)
			{Waypoint: "X1-VB74-A1", ShipSymbol: "DLV-A1"},   // hull placed at the A1 hub
		},
		nil, // source hubs
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	sink := &spyDepotSink{}

	launchDepotCoordinators(context.Background(), reg, 7, sink)

	require.Len(t, sink.deliveries, 2, "each declared, crewed delivery hull launches (positioned at its own hub)")
	byShip := map[string]string{}
	for _, d := range sink.deliveries {
		byShip[d.ship] = d.waypoint
		require.Equal(t, 7, d.playerID)
	}
	require.Equal(t, "X1-VB74-J58", byShip["DLV-J58"], "the J58 delivery hull is placed at its own J58 hub")
	require.Equal(t, "X1-VB74-A1", byShip["DLV-A1"], "the A1 delivery hull is placed at its own A1 hub")
}

// A depot with MULTIPLE stockers points every one of them at the shared destination
// warehouse anchor, and each warehouse element launches its own coordinator — the parametrized
// topology (sp-u9xa: counts are parameters) drives the launch fan-out with no hardcoded count.
func TestLaunchDepotCoordinators_FansOutAcrossStockersToTheAnchorWarehouse(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{
			{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"},
			{Waypoint: "X1-SRC-2", ShipSymbol: "ST-2"},
		},
		nil,
		nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	sink := &spyDepotSink{}

	launchDepotCoordinators(context.Background(), reg, 7, sink)

	require.Len(t, sink.stockers, 2, "every declared stocker launches")
	for _, st := range sink.stockers {
		require.Equal(t, "X1-J58-WH", st.waypoint, "every depot stocker deposits into the shared destination anchor")
	}
}

// A declared-but-uncrewed slot (empty ShipSymbol — sized before a hull is pinned) and an
// absent/empty registry launch NOTHING: there is no hull to fly, and the regression-safe
// default (no depots) must never launch a coordinator. One parametrized test covers the
// no-launch cases (Mandate 5).
func TestLaunchDepotCoordinators_NoLaunchCases(t *testing.T) {
	uncrewed, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: ""}}, // declared, not yet crewed
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: ""}},
		[]depot.Element{{Waypoint: "X1-J58-DLV", ShipSymbol: ""}}, // declared delivery-hull slot, not yet crewed
		nil,
	)
	require.NoError(t, err)

	cases := map[string]*depot.Registry{
		"nil registry":      nil,
		"empty registry":    depot.NewRegistry(nil),
		"uncrewed elements": depot.NewRegistry([]*depot.ContractDepot{uncrewed}),
	}
	for name, reg := range cases {
		reg := reg
		t.Run(name, func(t *testing.T) {
			sink := &spyDepotSink{}
			launchDepotCoordinators(context.Background(), reg, 7, sink)
			require.Empty(t, sink.warehouses, "no crewed warehouse element -> no warehouse launch")
			require.Empty(t, sink.stockers, "no crewed stocker element -> no stocker launch")
			require.Empty(t, sink.deliveries, "no crewed delivery hull -> no delivery launch")
		})
	}
}

// Gap 2: the depot warehouse's cargo targets are the RECEIPT-demand knapsack — keyed on what
// the DESTINATION receives and on the residual HAUL-LEG the buffer relocates onto the stocker
// (dist(destination-warehouse, source)). Among received goods of equal demand, a tight buffer
// holds the FAR-sourced good (big haul saved) and drops the NEAR-sourced one (little haul
// saved). This proves the caps come from PlanReceiptCaps anchored on the destination warehouse,
// not an empty/blind fill; and that receipt demand is mined for the destination's own system.
func TestDepotWarehouseTargetUnits_GatesOnReceiptDemandPreferringFarSource(t *testing.T) {
	const warehouseWaypoint = "X1-J58-WH"
	coords := func(w string) (float64, float64, bool) {
		switch w {
		case warehouseWaypoint:
			return 0, 0, true
		case "X1-SRC-NEAR":
			return 10, 0, true // ~10u from the destination warehouse: little haul saved
		case "X1-SRC-FAR":
			return 500, 0, true // ~500u from the destination warehouse: big haul saved
		}
		return 0, 0, false
	}
	// Two received goods identical in receipt demand; they differ ONLY in how far their source
	// sits from the destination warehouse (the haul-leg the buffer would move onto a stocker).
	miner := &fakeReceiptMiner{rows: []persistence.DemandCandidate{
		{Good: "NEAR_GOOD", ContractCount: 5, ForeignAsk: 100, ForeignMarket: "X1-SRC-NEAR", ForeignSystem: "X1", MaxContractUnits: 40},
		{Good: "FAR_GOOD", ContractCount: 5, ForeignAsk: 100, ForeignMarket: "X1-SRC-FAR", ForeignSystem: "X1", MaxContractUnits: 40},
	}}

	// Capacity 40 fits exactly ONE 40-unit good, forcing the receipt knapsack to choose.
	targets := depotWarehouseTargetUnits(context.Background(), miner, 40, "X1", warehouseWaypoint, coords, 7, nil)

	require.Contains(t, targets, "FAR_GOOD",
		"receipt-demand buffers the far-sourced received good (the long haul-leg the stocker absorbs)")
	require.NotContains(t, targets, "NEAR_GOOD",
		"under tight capacity the near-sourced good is dropped (little haul saved)")
	require.Equal(t, "X1", miner.queriedSystem,
		"depot-warehouse caps are keyed on what the DESTINATION system RECEIVES")
}

// sp-64se root cause #1: the depot buffer must rank received goods by CONTRACT REWARD (what
// the destination's contracts PAY for the good) — NOT by market ask (what it RESELLS for). A
// good with a high market ask but a low contract reward (the live EQUIPMENT case) must LOSE to
// a good with a low ask but a high contract reward (the live CLOTHING/ASSAULT_RIFLES case).
// Here the two goods are identical in every knapsack input (recurrence, size, source distance)
// EXCEPT that their market ask and contract reward are INVERTED; under a capacity that fits one,
// the reward-ranked buffer keeps the high-reward/low-ask good and drops the high-ask/low-reward
// one. This fails while the payment signal is the market ask (HomeAsk).
func TestDepotWarehouseTargetUnits_RanksByContractRewardNotMarketAsk(t *testing.T) {
	const warehouseWaypoint = "X1-J58-WH"
	coords := func(w string) (float64, float64, bool) {
		switch w {
		case warehouseWaypoint:
			return 0, 0, true
		case "X1-SRC": // one shared source: both goods sit at the SAME haul distance
			return 100, 0, true
		}
		return 0, 0, false
	}
	miner := &fakeReceiptMiner{rows: []persistence.DemandCandidate{
		// High market ask, LOW contract reward — the EQUIPMENT trap (resells dear, contracts pay little).
		{Good: "HIGH_ASK_LOW_REWARD", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X1-SRC", ForeignSystem: "X1", ForeignAsk: 8000,
			HomeAsk: 8000, HomeAskKnown: true, ContractRewardPerUnit: 1000},
		// Low market ask, HIGH contract reward — the CLOTHING/ASSAULT_RIFLES case the buffer should serve.
		{Good: "LOW_ASK_HIGH_REWARD", ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X1-SRC", ForeignSystem: "X1", ForeignAsk: 500,
			HomeAsk: 500, HomeAskKnown: true, ContractRewardPerUnit: 5000},
	}}

	// Capacity 40 fits exactly ONE 40-unit good, forcing the value ranking to decide.
	targets := depotWarehouseTargetUnits(context.Background(), miner, 40, "X1", warehouseWaypoint, coords, 7, nil)

	require.Contains(t, targets, "LOW_ASK_HIGH_REWARD",
		"the buffer ranks by CONTRACT REWARD, so the high-reward (low-ask) good is bought")
	require.NotContains(t, targets, "HIGH_ASK_LOW_REWARD",
		"a high market ask must NOT win a buffer slot when the good's contract reward is low")
}

// sp-64se root cause #2: a co-located warehouse GROUP solves the receipt knapsack over its
// AGGREGATE capacity (Σ hull capacity), so the shared buffer COVERS THE WHITELIST BREADTH
// instead of each hull deep-filling the same top goods over a single 80. Four equally-valued
// ~40-unit goods: a one-hull group (80) fits only two; the co-located pair (160) fits all four.
// This fails while the group's capacity is a single hull's rather than the summed aggregate.
func TestDepotColocatedWarehouseTargets_AggregateCapacityCoversWhitelistBreadth(t *testing.T) {
	const warehouseWaypoint = "X1-J58-WH"
	coords := func(w string) (float64, float64, bool) {
		if w == warehouseWaypoint {
			return 0, 0, true
		}
		return 0, 0, false // cross-system sources: residual short-circuits to the cross maximum
	}
	// Four received goods, identical value, each one contract wide (40 units), cross-system sourced.
	miner := &fakeReceiptMiner{rows: []persistence.DemandCandidate{
		{Good: "GOOD_A", ContractCount: 5, MaxContractUnits: 40, ForeignMarket: "X2-S1", ForeignSystem: "X2", ForeignAsk: 1000, ContractRewardPerUnit: 1000},
		{Good: "GOOD_B", ContractCount: 5, MaxContractUnits: 40, ForeignMarket: "X2-S2", ForeignSystem: "X2", ForeignAsk: 1000, ContractRewardPerUnit: 1000},
		{Good: "GOOD_C", ContractCount: 5, MaxContractUnits: 40, ForeignMarket: "X2-S3", ForeignSystem: "X2", ForeignAsk: 1000, ContractRewardPerUnit: 1000},
		{Good: "GOOD_D", ContractCount: 5, MaxContractUnits: 40, ForeignMarket: "X2-S4", ForeignSystem: "X2", ForeignAsk: 1000, ContractRewardPerUnit: 1000},
	}}
	capacityOf := func(string) int { return 80 } // every warehouse hull is a standard 80-cargo frame

	single := depotColocatedWarehouseTargets(context.Background(), miner,
		[]string{"WH-1"}, capacityOf, "X1", warehouseWaypoint, coords, 7, nil)
	require.Len(t, single, 2, "a single 80-cargo hull covers only two one-contract-wide goods")

	aggregate := depotColocatedWarehouseTargets(context.Background(), miner,
		[]string{"WH-1", "WH-2"}, capacityOf, "X1", warehouseWaypoint, coords, 7, nil)
	require.Len(t, aggregate, 4,
		"the co-located pair's AGGREGATE 160 capacity covers the full whitelist breadth (all four goods)")
}

// sp-64se root cause #2 (planning half): planDepotLaunches groups CO-LOCATED warehouses — every
// crewed warehouse hull of the same depot at the SAME waypoint — into each warehouse intent, so
// the launch can solve the receipt knapsack over their aggregate capacity. Warehouses at
// DIFFERENT waypoints are separate groups (co-location is by waypoint, not "all depot hulls").
func TestPlanDepotLaunches_GroupsCoLocatedWarehousesForAggregateCapacity(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{
			{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"},  // co-located pair at the anchor
			{Waypoint: "X1-J58-WH", ShipSymbol: "WH-2"},  //
			{Waypoint: "X1-J58-WH2", ShipSymbol: "WH-3"}, // a separate warehouse at its own waypoint
		},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		nil,
		nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})

	byShip := map[string][]string{}
	for _, intent := range planDepotLaunches(reg) {
		if intent.role == depot.RoleWarehouse {
			byShip[intent.shipSymbol] = intent.coLocatedWarehouseShips
		}
	}

	require.ElementsMatch(t, []string{"WH-1", "WH-2"}, byShip["WH-1"],
		"a co-located warehouse carries its whole same-waypoint group (for the aggregate-capacity solve)")
	require.ElementsMatch(t, []string{"WH-1", "WH-2"}, byShip["WH-2"])
	require.ElementsMatch(t, []string{"WH-3"}, byShip["WH-3"],
		"a warehouse at its own waypoint is its own group — co-location is by waypoint, not whole-depot")
}
