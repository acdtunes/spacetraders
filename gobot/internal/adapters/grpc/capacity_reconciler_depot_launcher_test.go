package grpc

// sp-rt3b6 — the reuse-path depot launch bridge's translation: the reconciler's neutral
// DepotLaunchSpec maps onto the grpc DepotSpec by role, and the mapped spec is a valid
// domain depot (a warehouse anchor is present) — so StartDepot can actually persist +
// launch it.

import (
	"testing"

	"github.com/stretchr/testify/require"

	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
)

// Behavior: each reuse role maps to the matching DepotSpec element class (worker →
// delivery hull), the hub symbol becomes the depot id, and the result is a valid domain
// depot ready for StartDepot.
func TestReuseDepotSpec_MapsRolesAndProducesLaunchableDepot(t *testing.T) {
	spec := capacityCmd.DepotLaunchSpec{
		HubSymbol:     "X1-HUB-A",
		Warehouses:    []capacityCmd.DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "WH-1"}},
		Stockers:      []capacityCmd.DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "ST-1"}},
		DeliveryHulls: []capacityCmd.DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "DL-1"}},
	}

	got := reuseDepotSpec(spec)

	require.Equal(t, "X1-HUB-A", got.ID, "the hub symbol is the stable depot identity")
	require.Equal(t, []ElementSpec{{Waypoint: "X1-HUB-A", ShipSymbol: "WH-1"}}, got.Warehouses)
	require.Equal(t, []ElementSpec{{Waypoint: "X1-HUB-A", ShipSymbol: "ST-1"}}, got.Stockers)
	require.Equal(t, []ElementSpec{{Waypoint: "X1-HUB-A", ShipSymbol: "DL-1"}}, got.DeliveryHulls,
		"the capacity worker maps to the depot DELIVERY hull")
	require.Empty(t, got.SourceHubs, "a reuse-staffed depot carries no source hubs")

	_, err := got.toDomain()
	require.NoError(t, err, "the mapped spec must be a valid domain depot (warehouse anchor present) for StartDepot")
}

// Behavior: an empty role slice maps to nil (no element), never an empty non-nil slice —
// so a worker-only or warehouse-only spec carries exactly the roles it has.
func TestReuseDepotSpec_EmptyRolesMapToNil(t *testing.T) {
	got := reuseDepotSpec(capacityCmd.DepotLaunchSpec{
		HubSymbol:  "X1-HUB-A",
		Warehouses: []capacityCmd.DepotLaunchElement{{Waypoint: "X1-HUB-A", ShipSymbol: "WH-1"}},
	})

	require.Len(t, got.Warehouses, 1)
	require.Nil(t, got.Stockers)
	require.Nil(t, got.DeliveryHulls)
}
