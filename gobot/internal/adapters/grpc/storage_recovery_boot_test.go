package grpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- stubs ---------------------------------------------------------------

// bootPlayerRepo serves a fixed set of players. recoverStorageOperations only
// calls ListAll; the rest of the interface is the embedded nil (a panic surfaces
// any unexpected new dependency).
type bootPlayerRepo struct {
	player.PlayerRepository
	players []*player.Player
	err     error
}

func (r *bootPlayerRepo) ListAll(context.Context) ([]*player.Player, error) {
	return r.players, r.err
}

// bootOpRepo serves a fixed set of running operations for every player. Recovery
// only calls FindRunning here.
type bootOpRepo struct {
	storage.StorageOperationRepository
	running []*storage.StorageOperation
}

func (r *bootOpRepo) FindRunning(context.Context, int) ([]*storage.StorageOperation, error) {
	return r.running, nil
}

// bootAPIClient reports each ship's LIVE cargo — the physical source of truth the
// recovery reconstructs from (RULINGS #2). A ship listed in errShips fails its
// GetShip so the fail-open path (log + skip, boot continues) can be exercised.
type bootAPIClient struct {
	domainPorts.APIClient
	ships    map[string]*navigation.ShipData
	errShips map[string]bool
}

func (c *bootAPIClient) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	if c.errShips[symbol] {
		return nil, fmt.Errorf("simulated API failure for %s", symbol)
	}
	if ship, ok := c.ships[symbol]; ok {
		return ship, nil
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

func warehouseWithShips(t *testing.T, id, waypoint string, ships, goods []string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, ships, goods, nil)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

// --- tests ---------------------------------------------------------------

// TestBootStorageRecovery_ReseedsCoordinatorFromLiveShipState is the sp-o477
// regression. After a daemon restart the in-memory StorageCoordinator is EMPTY
// (it is populated only by live deposits), so a warehouse holding physical stock
// is invisible to GetTotalCargoAvailable — which is exactly why contracts began
// market-buying goods already in the warehouse. The boot recovery wiring re-seeds
// the coordinator from each storage ship's live API cargo.
//
// This test FAILS without the wiring (coordinator stays 0) and PASSES with it.
func TestBootStorageRecovery_ReseedsCoordinatorFromLiveShipState(t *testing.T) {
	op := warehouseWithShips(t, "warehouse-X1-VB74-A1", "X1-VB74-A1",
		[]string{"TORWIND-A"}, []string{"MEDICINE"})

	coordinator := storageApp.NewInMemoryStorageCoordinator()
	// Precondition: the post-restart state — nothing registered, stock invisible.
	require.Equal(t, 0, coordinator.GetTotalCargoAvailable("warehouse-X1-VB74-A1", "MEDICINE"),
		"precondition: a freshly-restarted coordinator sees zero standing stock")

	svc := storageApp.NewStorageRecoveryService(
		&bootOpRepo{running: []*storage.StorageOperation{op}},
		&bootAPIClient{ships: map[string]*navigation.ShipData{
			"TORWIND-A": {
				Symbol:   "TORWIND-A",
				Location: "X1-VB74-A1",
				Cargo: &navigation.CargoData{
					Capacity:  80,
					Units:     27,
					Inventory: []shared.CargoItem{{Symbol: "MEDICINE", Units: 27}},
				},
			},
		}},
		coordinator,
	)

	server := &DaemonServer{
		playerRepo:      &bootPlayerRepo{players: []*player.Player{player.NewPlayer(shared.MustNewPlayerID(1), "AGENT-1", "token-1")}},
		storageRecovery: svc,
	}

	server.recoverStorageOperations(context.Background())

	require.Equal(t, 27, coordinator.GetTotalCargoAvailable("warehouse-X1-VB74-A1", "MEDICINE"),
		"boot recovery must rebuild the warehouse's cargo availability from the hull's live cargo (sp-o477)")
	_, registered := coordinator.GetStorageShipBySymbol("TORWIND-A")
	require.True(t, registered, "the warehouse hull must be re-registered with the shared coordinator")
}

// TestBootStorageRecovery_NilServiceDoesNotPanic: boot must never depend on the
// recovery service being present. When it is unwired (nil), recovery is a silent
// no-op — the daemon still boots (RULINGS #1 fail-open).
func TestBootStorageRecovery_NilServiceDoesNotPanic(t *testing.T) {
	server := &DaemonServer{
		playerRepo:      &bootPlayerRepo{players: []*player.Player{player.NewPlayer(shared.MustNewPlayerID(1), "AGENT-1", "token-1")}},
		storageRecovery: nil,
	}
	require.NotPanics(t, func() {
		server.recoverStorageOperations(context.Background())
	})
}

// TestBootStorageRecovery_ShipErrorSkippedOthersRecovered: a per-ship API failure
// during recovery is logged and skipped — boot continues and every other ship is
// still recovered (RULINGS #1, never empty good state on a partial failure).
func TestBootStorageRecovery_ShipErrorSkippedOthersRecovered(t *testing.T) {
	op := warehouseWithShips(t, "warehouse-X1-VB74-A1", "X1-VB74-A1",
		[]string{"BROKEN-HULL", "TORWIND-A"}, []string{"MEDICINE"})

	coordinator := storageApp.NewInMemoryStorageCoordinator()
	svc := storageApp.NewStorageRecoveryService(
		&bootOpRepo{running: []*storage.StorageOperation{op}},
		&bootAPIClient{
			ships: map[string]*navigation.ShipData{
				"TORWIND-A": {
					Symbol:   "TORWIND-A",
					Location: "X1-VB74-A1",
					Cargo: &navigation.CargoData{
						Capacity:  80,
						Units:     27,
						Inventory: []shared.CargoItem{{Symbol: "MEDICINE", Units: 27}},
					},
				},
			},
			errShips: map[string]bool{"BROKEN-HULL": true},
		},
		coordinator,
	)

	server := &DaemonServer{
		playerRepo:      &bootPlayerRepo{players: []*player.Player{player.NewPlayer(shared.MustNewPlayerID(1), "AGENT-1", "token-1")}},
		storageRecovery: svc,
	}

	require.NotPanics(t, func() {
		server.recoverStorageOperations(context.Background())
	})

	// The healthy hull was recovered despite its sibling's API failure.
	require.Equal(t, 27, coordinator.GetTotalCargoAvailable("warehouse-X1-VB74-A1", "MEDICINE"),
		"a failed ship must not block recovery of the healthy ships in the same operation")
	_, healthy := coordinator.GetStorageShipBySymbol("TORWIND-A")
	require.True(t, healthy, "the healthy hull must still be registered after a sibling's API error")
	_, broken := coordinator.GetStorageShipBySymbol("BROKEN-HULL")
	require.False(t, broken, "the ship whose API call failed must be skipped, not registered")
}

// TestBootStorageRecovery_PlayerListErrorFailsOpen: if listing players fails, boot
// recovery is skipped without panicking (RULINGS #1 — a boot-time read error never
// crashes the daemon).
func TestBootStorageRecovery_PlayerListErrorFailsOpen(t *testing.T) {
	server := &DaemonServer{
		playerRepo:      &bootPlayerRepo{err: fmt.Errorf("db unavailable")},
		storageRecovery: storageApp.NewStorageRecoveryService(&bootOpRepo{}, &bootAPIClient{}, storageApp.NewInMemoryStorageCoordinator()),
	}
	require.NotPanics(t, func() {
		server.recoverStorageOperations(context.Background())
	})
}
