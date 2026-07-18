package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-udgc — depot-launch re-strander (ii), sibling to sp-2jrz's capacity_reconciler re-strander (i).
//
// CONFIRMED LIVE: the boot reload (reloadDepotRegistryAtBoot -> launchDepotCoordinators) re-derives
// the registry from persisted contract_depots rows and, per crewed element, launches a
// StartStocker/StartWarehouse coordinator AND re-dedicates its crewing hull to the stocker/warehouse
// fleet (via positionDepotElementHull -> AssignFleet). Keyed off the STALE rows of a DECOMMISSIONED
// contract op, this re-strands light freighters off trade EVERY restart. The demand miner cannot
// detect decommission (it ranks contract HISTORY, which a fulfilled domain still shows); the LIVE
// signal is FindActiveContracts (accepted && !fulfilled). The fix is a demand-driven launch guard:
// only (re)launch a depot whose destination SYSTEM still has live contract demand.
//
// These tests drive the launch decision through the spy sink (spyDepotSink, defined in
// container_ops_depot_launch_test.go). A spy warehouse/stocker/delivery/source-hub launch record
// stands in for the real spawn + hull re-dedication (positionDepotElementHull) the production sink
// performs — so "no launch recorded" is exactly "no container spawned, no hull re-dedicated".

// RED->GREEN (the re-strander fix): a depot whose destination system has NO live contract demand
// (decommissioned/stale contract_depots rows) must NOT launch its warehouse/stocker coordinators or
// re-dedicate its hulls on the restart-safe launch path. Before the guard, launchLiveDepotCoordinators
// launched unconditionally (the re-strander); after it, the depot is skipped entirely.
func TestLaunchLiveDepotCoordinators_SkipsDepotWithNoLiveContractDemand(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		[]depot.Element{{Waypoint: "X1-J58-HUB", ShipSymbol: "DLV-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "SRC-1"}},
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	sink := &spyDepotSink{}

	// No active contract delivers to X1-J58 — the domain is decommissioned.
	skipped := launchLiveDepotCoordinators(context.Background(), reg, 3, sink, map[string]bool{})

	require.Empty(t, sink.warehouses, "a decommissioned depot must NOT launch a warehouse (no re-dedicate)")
	require.Empty(t, sink.stockers, "a decommissioned depot must NOT launch a stocker (no re-dedicate)")
	require.Empty(t, sink.deliveries, "a decommissioned depot must NOT position a delivery hull")
	require.Empty(t, sink.sourceHubs, "a decommissioned depot must NOT position a source hub")
	require.Empty(t, sink.dedicated, "a decommissioned depot must NOT dedicate any hull to the contract reserve")
	require.Equal(t, []string{"j58"}, skipped, "the skipped depot id is reported for the boot log")
}

// Byte-identical for a LIVE depot: a depot whose destination system HAS a live contract must launch
// its warehouse + stocker exactly as launchDepotCoordinators does today (the sp-cftm behavior).
func TestLaunchLiveDepotCoordinators_LaunchesDepotWithLiveContractDemand(t *testing.T) {
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

	// An active contract delivers into X1-J58 — the depot is live.
	skipped := launchLiveDepotCoordinators(context.Background(), reg, 7, sink, map[string]bool{"X1-J58": true})

	require.Empty(t, skipped, "a live depot is not skipped")
	require.Len(t, sink.warehouses, 1, "a live depot's warehouse must still launch (byte-identical to launchDepotCoordinators)")
	require.Equal(t, "WH-1", sink.warehouses[0].ship)
	require.Equal(t, "X1-J58-WH", sink.warehouses[0].waypoint)
	require.Equal(t, 7, sink.warehouses[0].playerID)
	require.Len(t, sink.stockers, 1, "a live depot's stocker must still launch")
	require.Equal(t, "ST-1", sink.stockers[0].ship)
	require.Equal(t, "X1-J58-WH", sink.stockers[0].waypoint, "the stocker still deposits into the anchor")
}

// A mixed registry launches ONLY the live depots: the stale one is withheld, the live one is
// byte-identical. This is the real-world boot state — a decommissioned depot's rows persist
// alongside a live depot's.
func TestLaunchLiveDepotCoordinators_LaunchesOnlyLiveDepotsInMixedRegistry(t *testing.T) {
	live, err := depot.NewContractDepot(
		"live",
		[]depot.Element{{Waypoint: "X1-LIVE-WH", ShipSymbol: "WH-L"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-L"}},
		nil, nil,
	)
	require.NoError(t, err)
	stale, err := depot.NewContractDepot(
		"stale",
		[]depot.Element{{Waypoint: "X1-STALE-WH", ShipSymbol: "WH-S"}},
		[]depot.Element{{Waypoint: "X1-SRC-2", ShipSymbol: "ST-S"}},
		nil, nil,
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{live, stale})
	sink := &spyDepotSink{}

	skipped := launchLiveDepotCoordinators(context.Background(), reg, 3, sink, map[string]bool{"X1-LIVE": true})

	require.Equal(t, []string{"stale"}, skipped)
	require.Len(t, sink.warehouses, 1, "only the live depot's warehouse launches")
	require.Equal(t, "WH-L", sink.warehouses[0].ship)
	require.Len(t, sink.stockers, 1, "only the live depot's stocker launches")
	require.Equal(t, "ST-L", sink.stockers[0].ship)
}

// Pure partition helper: depots are split by whether their anchor-warehouse destination SYSTEM is in
// the live set. Order is preserved (byte-identical launch order for the live subset).
func TestDepotsWithLiveDemand_PartitionsByDestinationSystem(t *testing.T) {
	a, err := depot.NewContractDepot("a", []depot.Element{{Waypoint: "X1-AA-WH", ShipSymbol: "WA"}}, nil, nil, nil)
	require.NoError(t, err)
	b, err := depot.NewContractDepot("b", []depot.Element{{Waypoint: "X1-BB-WH", ShipSymbol: "WB"}}, nil, nil, nil)
	require.NoError(t, err)
	c, err := depot.NewContractDepot("c", []depot.Element{{Waypoint: "X1-AA-OTHER", ShipSymbol: "WC"}}, nil, nil, nil)
	require.NoError(t, err)

	liveDepots, skipped := depotsWithLiveDemand([]*depot.ContractDepot{a, b, c}, map[string]bool{"X1-AA": true})

	require.Len(t, liveDepots, 2, "a and c share the live X1-AA system")
	require.Equal(t, "a", liveDepots[0].ID())
	require.Equal(t, "c", liveDepots[1].ID(), "order preserved")
	require.Len(t, skipped, 1)
	require.Equal(t, "b", skipped[0].ID(), "b's X1-BB system has no live contract")
}

// contractDestinationSystems reduces a set of live contracts to the set of destination SYSTEMS they
// deliver to — the granularity a depot's domain is matched at. A multi-delivery contract contributes
// every delivery's system.
func TestContractDestinationSystems_ExtractsDeliverySystems(t *testing.T) {
	pid := shared.MustNewPlayerID(3)
	c1, err := contract.NewContract("c1", pid, "COSMIC", "PROCUREMENT", contract.Terms{
		Deliveries: []contract.Delivery{
			{TradeSymbol: "IRON", DestinationSymbol: "X1-J58-A1", UnitsRequired: 100},
			{TradeSymbol: "COPPER", DestinationSymbol: "X1-J58-B2", UnitsRequired: 50},
		},
	}, nil)
	require.NoError(t, err)
	c2, err := contract.NewContract("c2", pid, "COSMIC", "PROCUREMENT", contract.Terms{
		Deliveries: []contract.Delivery{{TradeSymbol: "GOLD", DestinationSymbol: "X1-KK-C3", UnitsRequired: 10}},
	}, nil)
	require.NoError(t, err)

	systems := contractDestinationSystems([]*contract.Contract{c1, c2})

	require.Equal(t, map[string]bool{"X1-J58": true, "X1-KK": true}, systems,
		"both J58 deliveries collapse to one system; KK is its own")
}
