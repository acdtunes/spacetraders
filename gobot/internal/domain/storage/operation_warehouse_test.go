package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A warehouse is the extractor-free sibling of a storage operation:
// construction with ZERO extractor ships must succeed, where the
// extractor-fed NewStorageOperation rejects it. This is the load-bearing seam —
// everything downstream reuses StorageOperation unchanged.
func TestNewWarehouseOperation_ZeroExtractorConstructionValid(t *testing.T) {
	op, err := NewWarehouseOperation(
		"warehouse-X1-HOME-A1",
		42,
		"X1-HOME-A1",
		[]string{"HULL-STORE-1"},
		[]string{"IRON_ORE", "ALUMINUM"},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, OperationTypeWarehouse, op.OperationType())
	require.Empty(t, op.ExtractorShips(), "a warehouse is hauler-fed and holds no extractors")
	require.Equal(t, []string{"HULL-STORE-1"}, op.StorageShips())
	require.True(t, op.SupportsGood("IRON_ORE"))
	require.True(t, op.SupportsGood("ALUMINUM"))
	require.False(t, op.SupportsGood("PLATINUM"))

	// The same lifecycle as any storage op: PENDING -> RUNNING.
	require.True(t, op.IsPending())
	require.NoError(t, op.Start())
	require.True(t, op.IsRunning())
	require.Equal(t, OperationStatusRunning, op.Status())
}

// The zero-extractor relaxation is the ONLY invariant a warehouse drops. It
// still requires an id, a positive player, a waypoint, >=1 storage ship, and a
// non-empty goods whitelist — a warehouse with no hull or no goods is
// meaningless and must fail closed.
func TestNewWarehouseOperation_ValidatesRemainingInvariants(t *testing.T) {
	cases := []struct {
		name           string
		id             string
		playerID       int
		waypoint       string
		storageShips   []string
		supportedGoods []string
	}{
		{"empty id", "", 1, "X1-HOME-A1", []string{"HULL-1"}, []string{"IRON_ORE"}},
		{"non-positive player", "wh-1", 0, "X1-HOME-A1", []string{"HULL-1"}, []string{"IRON_ORE"}},
		{"empty waypoint", "wh-1", 1, "", []string{"HULL-1"}, []string{"IRON_ORE"}},
		{"no storage ship", "wh-1", 1, "X1-HOME-A1", []string{}, []string{"IRON_ORE"}},
		{"no supported goods", "wh-1", 1, "X1-HOME-A1", []string{"HULL-1"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := NewWarehouseOperation(tc.id, tc.playerID, tc.waypoint, tc.storageShips, tc.supportedGoods, nil)
			require.Error(t, err)
			require.Nil(t, op)
		})
	}
}

// A warehouse must survive the persistence DTO round-trip with its WAREHOUSE
// type and its zero-extractor shape intact — this is what lets the recovery
// service reconstruct it (via StorageOperationFromData) after a daemon restart.
// StorageOperationFromData applies NO extractor invariant, so a zero-extractor
// row rebuilds cleanly where NewStorageOperation never could.
func TestWarehouseOperation_PersistenceDTORoundTrip(t *testing.T) {
	orig, err := NewWarehouseOperation(
		"warehouse-X1-HOME-A1",
		7,
		"X1-HOME-A1",
		[]string{"HULL-STORE-1"},
		[]string{"IRON_ORE"},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, orig.Start())

	data := orig.ToData()
	require.Equal(t, string(OperationTypeWarehouse), data.OperationType)
	require.Empty(t, data.ExtractorShips)
	require.Equal(t, string(OperationStatusRunning), data.Status)

	rebuilt := StorageOperationFromData(data, nil)
	require.Equal(t, OperationTypeWarehouse, rebuilt.OperationType())
	require.Empty(t, rebuilt.ExtractorShips())
	require.Equal(t, []string{"HULL-STORE-1"}, rebuilt.StorageShips())
	require.True(t, rebuilt.IsRunning())
	require.True(t, rebuilt.SupportsGood("IRON_ORE"))
}

// The design's core claim: the StorageShip deposit and withdrawal protocols
// work UNCHANGED for a warehouse's storage ship. A deposit (ReserveSpace ->
// ConfirmDeposit) grows cargoInventory; a withdrawal (TryReserveCargo ->
// ConfirmTransfer) drains it — the exact two protocols the gas siphon worker
// and the manufacturing STORAGE_ACQUIRE_DELIVER executor use, proving the
// warehouse buffers arbitrary contract goods with the shared primitives.
func TestWarehouseStorageShip_DepositThenWithdrawRoundTrip(t *testing.T) {
	op, err := NewWarehouseOperation("wh-1", 1, "X1-HOME-A1", []string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil)
	require.NoError(t, err)

	ship, err := NewStorageShip("HULL-STORE-1", op.WaypointSymbol(), op.ID(), 100, nil)
	require.NoError(t, err)

	// Deposit path (hauler drops 40 IRON_ORE): reserve space then confirm.
	require.NoError(t, ship.ReserveSpace(40))
	require.NoError(t, ship.ConfirmDeposit("IRON_ORE", 40))
	require.Equal(t, 40, ship.GetInventory()["IRON_ORE"])
	require.Equal(t, 40, ship.GetAvailableCargo("IRON_ORE"))

	// Withdrawal path (contract worker pulls the stock): reserve then confirm.
	reserved, err := ship.TryReserveCargo("IRON_ORE", 10)
	require.NoError(t, err)
	require.Equal(t, 40, reserved, "TryReserveCargo grabs ALL available to maximize a transfer")
	require.Equal(t, 0, ship.GetAvailableCargo("IRON_ORE"), "reserved cargo is no longer available to others")

	require.NoError(t, ship.ConfirmTransfer("IRON_ORE", 40))
	require.Empty(t, ship.GetInventory(), "the warehouse is drained after withdrawal")
}
