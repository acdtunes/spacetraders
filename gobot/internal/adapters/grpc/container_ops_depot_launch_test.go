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
}

type depotLaunchRecord struct {
	ship     string
	waypoint string
	playerID int
}

func (s *spyDepotSink) launchDepotWarehouse(_ context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	s.warehouses = append(s.warehouses, depotLaunchRecord{ship: shipSymbol, waypoint: warehouseWaypoint, playerID: playerID})
	return nil
}

func (s *spyDepotSink) launchDepotStocker(_ context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	s.stockers = append(s.stockers, depotLaunchRecord{ship: shipSymbol, waypoint: warehouseWaypoint, playerID: playerID})
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
		nil,
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
