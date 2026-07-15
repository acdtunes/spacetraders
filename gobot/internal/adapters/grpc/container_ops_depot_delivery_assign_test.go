package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// depotDeliveryFakeWaypointProvider is a no-lookup IWaypointProvider: it always errors so
// modelToDomain falls back to the ship row's DENORMALIZED location (LocationSymbol/X/Y). That
// keeps the real api.ShipRepository usable in these depot-delivery tests (which exercise the
// claim-release + reposition decision against real persistence) without a live system graph.
type depotDeliveryFakeWaypointProvider struct{}

func (depotDeliveryFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("depot-delivery test: no waypoint graph needed")
}

type depotNavCall struct {
	ship     string
	dest     string
	playerID int
}

// newDepotDeliveryTestServer wires a DaemonServer against the REAL api.ShipRepository sharing an
// in-memory DB, so launchDepotDelivery's atomic claim-release + re-dedication is verified against
// the real AssignFleet / ReleaseContainerClaim persistence (an ADAPTER, integration-tested — no
// mocked infrastructure). The one seam faked is the hub reposition (depotDeliveryNavigateOverride):
// the real NavigateShip spawns a navigate container goroutine that would race the assertions, so a
// spy records the reposition intent instead.
func newDepotDeliveryTestServer(t *testing.T) (*DaemonServer, *gorm.DB, int, *[]depotNavCall) {
	t.Helper()
	s, db, playerID := newRecoveryTestServer(t)
	s.shipRepo = api.NewShipRepository(nil, nil, nil, depotDeliveryFakeWaypointProvider{}, db, shared.NewRealClock())
	navCalls := &[]depotNavCall{}
	s.depotDeliveryNavigateOverride = func(_ context.Context, ship, dest string, pid int) (string, error) {
		*navCalls = append(*navCalls, depotNavCall{ship: ship, dest: dest, playerID: pid})
		return "nav-" + ship, nil
	}
	return s, db, playerID, navCalls
}

// insertDepotDeliveryHull inserts a haul-capable hull with the given standing dedication and
// location. active=true gives it a live coordinator work-claim (the "mid-task at assign time"
// shape); active=false is an idle hull.
func insertDepotDeliveryHull(t *testing.T, db *gorm.DB, symbol string, playerID int, dedicatedFleet, location string, active bool) {
	t.Helper()
	model := &persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		Role:             "HAULER",
		CargoCapacity:    80,
		FuelCapacity:     100,
		FuelCurrent:      100,
		EngineSpeed:      30, // ReconstructShip requires a positive engine speed
		FrameSymbol:      "FRAME_LIGHT_FREIGHTER",
		NavStatus:        "DOCKED",
		LocationSymbol:   location,
		SystemSymbol:     shared.ExtractSystemSymbol(location),
		DedicatedFleet:   dedicatedFleet,
		AssignmentStatus: "idle",
		AssignmentOwner:  string(navigation.AssignmentOwnerContainer),
	}
	if active {
		// A live work-claim needs a real parent container row (fk_ships_container is enforced).
		containerID := "worker-" + symbol
		insertRunningContainer(t, db, containerID, "run_contract", "contract_worker", "{}", playerID, nil)
		model.ContainerID = &containerID
		model.AssignmentStatus = "active"
	}
	require.NoError(t, db.Create(model).Error)
}

// sp-3l64 behaviors 1+2, ROUND 2 (contract): assigning an idle CONTRACT-tagged hull as a depot
// delivery hull must re-dedicate it to the distinct depot-delivery fleet, so the contract
// coordinator's OWN dedicated pool (FindIdleShipsByFleet("contract")) can no longer re-grab it and
// pull it off its hub. This is the exact re-grab vector observed live: hulls added as delivery
// hulls stayed 'contract'-tagged and were yanked back onto general contracts.
func TestLaunchDepotDelivery_ReDedicatesIdleContractHullSoCoordinatorCannotReGrabIt(t *testing.T) {
	s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
	const hub = "X1-VB74-J58"
	insertDepotDeliveryHull(t, db, "DLV-J58", playerID, "contract", hub, false) // idle, contract-tagged, AT its hub
	pid := shared.MustNewPlayerID(playerID)

	// The re-grab vector: while it carries the contract tag, the coordinator's dedicated pool sees it.
	_, before, err := appContract.FindIdleShipsByFleet(context.Background(), pid, s.shipRepo, "contract")
	require.NoError(t, err)
	require.Contains(t, before, "DLV-J58",
		"precondition: a contract-tagged idle hull is grabbable by the contract coordinator (the recurring bug)")

	require.NoError(t, s.launchDepotDelivery(context.Background(), "DLV-J58", hub, playerID))

	var got persistence.ShipModel
	require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", "DLV-J58", playerID).Error)
	require.Equal(t, depot.DeliveryHullFleet, got.DedicatedFleet,
		"assignment re-dedicates the hull to the distinct depot-delivery fleet")

	_, contractPool, err := appContract.FindIdleShipsByFleet(context.Background(), pid, s.shipRepo, "contract")
	require.NoError(t, err)
	require.NotContains(t, contractPool, "DLV-J58",
		"a depot delivery hull is no longer in the contract coordinator's dedicated pool — it cannot be re-grabbed")

	_, generalPool, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.NotContains(t, generalPool, "DLV-J58",
		"a depot delivery hull is excluded from the general idle-hauler pool as well")

	require.Empty(t, *navCalls, "a hull already parked at its hub is not repositioned")
}

// sp-3l64 behaviors 1+3, ROUND 1 (manufacturing): a hull added as a delivery hull while it is
// MID-TASK (busy, manufacturing-tagged, docked away from its hub) must be claim-released from its
// prior fleet (sp-w3yd), re-dedicated, and — now that it is free — navigated home to its hub. This
// is the exact stall observed live: hulls stayed DOCKED at I55, still manufacturing-tagged, never
// navigated, until a manual free-from-fleet unblocked them.
func TestLaunchDepotDelivery_ReleasesMidTaskManufacturingHullAndNavigatesItToHub(t *testing.T) {
	s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
	const hub = "X1-VB74-C39"
	insertDepotDeliveryHull(t, db, "DLV-C39", playerID, "manufacturing", "X1-VB74-I55", true) // busy, manufacturing, OFF hub

	require.NoError(t, s.launchDepotDelivery(context.Background(), "DLV-C39", hub, playerID))

	var got persistence.ShipModel
	require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", "DLV-C39", playerID).Error)
	require.Equal(t, depot.DeliveryHullFleet, got.DedicatedFleet,
		"re-dedicated to depot-delivery, clearing the stale manufacturing tag")
	require.Equal(t, "idle", got.AssignmentStatus,
		"its prior manufacturing work-claim is released so the hull is free (sp-w3yd)")
	require.Nil(t, got.ContainerID, "the released hull no longer belongs to the manufacturing container")

	require.Len(t, *navCalls, 1, "a freed, off-hub delivery hull is navigated home to its hub")
	require.Equal(t, "DLV-C39", (*navCalls)[0].ship)
	require.Equal(t, hub, (*navCalls)[0].dest)
	require.Equal(t, playerID, (*navCalls)[0].playerID)
}

// sp-3l64 idempotency guard: a hull ALREADY dedicated as a depot delivery hull is left undisturbed
// on a reload/re-apply — a busy one mid-delivery keeps its live claim (never yanked), and an idle
// one already parked at its hub is a no-op. This guards against the claim-release / reposition
// firing every pass, which would strand a hull mid-depot-delivery.
func TestLaunchDepotDelivery_LeavesAlreadyDedicatedDeliveryHullUndisturbed(t *testing.T) {
	const hub = "X1-VB74-K83"

	t.Run("busy hull mid-delivery keeps its live claim and is not repositioned", func(t *testing.T) {
		s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
		insertDepotDeliveryHull(t, db, "DLV-1", playerID, depot.DeliveryHullFleet, "X1-VB74-E44", true) // dedicated, busy, off-hub

		require.NoError(t, s.launchDepotDelivery(context.Background(), "DLV-1", hub, playerID))

		var got persistence.ShipModel
		require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", "DLV-1", playerID).Error)
		require.Equal(t, "active", got.AssignmentStatus, "a hull mid-depot-delivery keeps its live claim — never yanked on reload")
		require.NotNil(t, got.ContainerID)
		require.Equal(t, "worker-DLV-1", *got.ContainerID, "the SAME worker still holds the hull")
		require.Equal(t, depot.DeliveryHullFleet, got.DedicatedFleet, "its dedication is unchanged")
		require.Empty(t, *navCalls, "a busy hull is not repositioned")
	})

	t.Run("idle hull already parked at its hub is a no-op", func(t *testing.T) {
		s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
		insertDepotDeliveryHull(t, db, "DLV-2", playerID, depot.DeliveryHullFleet, hub, false) // dedicated, idle, AT hub

		require.NoError(t, s.launchDepotDelivery(context.Background(), "DLV-2", hub, playerID))

		require.Empty(t, *navCalls, "a hull already at its hub is not repositioned")
	})
}
