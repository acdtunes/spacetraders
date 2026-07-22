package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// spyDepotSink records the coordinator launches the depot orchestrator dispatches,
// standing in for the real container-launch infrastructure (StartWarehouse / StartStocker)
// so the wiring is proven without spawning container goroutines or requiring idle hulls.
type spyDepotSink struct {
	warehouses []depotLaunchRecord
	stockers   []depotLaunchRecord
	deliveries []depotLaunchRecord
	sourceHubs []depotLaunchRecord
	// dedicated records the ship symbols the reserve floor fleet-assigned to the exclusive "contract"
	// fleet (sp-7zoq) — the reserved fresh hulls plus any reclaimed pinned hulls, in dispatch order.
	dedicated []string
	// reserve is the reserve-floor census the launch consults (sp-mzdk). The zero value reserves
	// nothing, so every pre-sp-mzdk test keeps its pin-everything behavior unchanged.
	reserve deliveryPinBudget
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

func (s *spyDepotSink) launchDepotDelivery(_ context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	s.deliveries = append(s.deliveries, depotLaunchRecord{ship: shipSymbol, waypoint: hubWaypoint, playerID: playerID})
	return nil
}

func (s *spyDepotSink) launchDepotSourceHub(_ context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	s.sourceHubs = append(s.sourceHubs, depotLaunchRecord{ship: shipSymbol, waypoint: hubWaypoint, playerID: playerID})
	return nil
}

func (s *spyDepotSink) homeContractWorkerReserve(_ context.Context, _ *depot.Registry, _ int) deliveryPinBudget {
	return s.reserve
}

func (s *spyDepotSink) dedicateContractReserve(_ context.Context, shipSymbol string, _ int) error {
	s.dedicated = append(s.dedicated, shipSymbol)
	return nil
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
		[]depot.Element{{Waypoint: "X1-J58-DLV", ShipSymbol: ""}},  // declared delivery-hull slot, not yet crewed
		[]depot.Element{{Waypoint: "X1-J58-SRCH", ShipSymbol: ""}}, // declared source-hub slot, not yet crewed
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
			require.Empty(t, sink.sourceHubs, "no crewed source hub -> no source-hub launch")
		})
	}
}
