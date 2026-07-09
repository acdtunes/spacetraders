package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// newTestCoordinator builds an InMemoryStorageCoordinator with a tiny grace
// period and small attempt budget so tests exercise the timeout->resync->park
// backstop (sp-pafv) without slowing down the suite. Production always goes
// through NewInMemoryStorageCoordinator's fixed defaults.
func newTestCoordinator(gracePeriod time.Duration, maxAttempts int) *InMemoryStorageCoordinator {
	return &InMemoryStorageCoordinator{
		storageShips:         make(map[string]*storage.StorageShip),
		shipsByOperation:     make(map[string][]string),
		waiters:              make(map[waiterQueueKey][]*storage.CargoWaiter),
		depositSubscribers:   make(map[string][]chan storage.CargoDepositNotification),
		cargoWaitGracePeriod: gracePeriod,
		cargoWaitMaxAttempts: maxAttempts,
	}
}

func mustStorageShip(t *testing.T, shipSymbol, operationID string, capacity int, initialCargo map[string]int) *storage.StorageShip {
	t.Helper()
	ship, err := storage.NewStorageShip(shipSymbol, "X1-TEST-STORAGE", operationID, capacity, initialCargo)
	if err != nil {
		t.Fatalf("NewStorageShip: %v", err)
	}
	return ship
}

// TestWaitForCargo_ImmediateReservation_HappyPathUnchanged pins the existing
// behavior: when cargo is already available at call time, WaitForCargo
// reserves and returns immediately without touching the queue/timeout path.
func TestWaitForCargo_ImmediateReservation_HappyPathUnchanged(t *testing.T) {
	c := newTestCoordinator(50*time.Millisecond, 2)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, map[string]int{"IRON_ORE": 50})
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	got, units, err := c.WaitForCargo(context.Background(), "OP-1", "IRON_ORE", 10)
	if err != nil {
		t.Fatalf("expected immediate reservation, got error: %v", err)
	}
	if got.ShipSymbol() != "STORAGE-1" {
		t.Fatalf("expected STORAGE-1, got %s", got.ShipSymbol())
	}
	if units != 50 {
		t.Fatalf("expected all 50 available units reserved, got %d", units)
	}
}

// TestWaitForCargo_FIFODeliveryBeforeTimeout_HappyPathUnchanged pins the
// existing queued-wait behavior: a waiter queued because no cargo is
// available yet is satisfied via the normal NotifyCargoDeposited -> FIFO
// channel path well before the grace period elapses. sp-pafv's timeout leg
// must not interfere with this path.
func TestWaitForCargo_FIFODeliveryBeforeTimeout_HappyPathUnchanged(t *testing.T) {
	c := newTestCoordinator(200*time.Millisecond, 2)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		c.NotifyCargoDeposited("STORAGE-1", "IRON_ORE", 30)
	}()

	start := time.Now()
	got, units, err := c.WaitForCargo(context.Background(), "OP-1", "IRON_ORE", 10)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected FIFO delivery to satisfy the wait, got error: %v", err)
	}
	if got.ShipSymbol() != "STORAGE-1" || units != 30 {
		t.Fatalf("expected STORAGE-1 with 30 units, got %s/%d", got.ShipSymbol(), units)
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("expected FIFO delivery well before the grace period, took %v", elapsed)
	}
}

// TestWaitForCargo_FIFONotificationLost_ResyncFindsCargo is sp-pafv's core
// case for the storage-acquire wait (subsumes sp-0e10). Cargo is deposited
// directly on the ship, bypassing NotifyCargoDeposited/processWaiterQueue
// entirely - simulating a lost/raced wake-up where the FIFO queue is never
// told. WaitForCargo must resync against the ship directly and succeed
// instead of hanging forever.
func TestWaitForCargo_FIFONotificationLost_ResyncFindsCargo(t *testing.T) {
	c := newTestCoordinator(15*time.Millisecond, 5)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	go func() {
		time.Sleep(40 * time.Millisecond) // outlasts the first couple of grace periods
		// Deposit directly on the ship, bypassing NotifyCargoDeposited/
		// processWaiterQueue - the coordinator's FIFO queue is never told
		// cargo arrived. Only a resync (tryImmediateReservation) re-reading
		// ship state directly can discover this.
		if err := ship.DepositCargo("IRON_ORE", 25); err != nil {
			panic(err) // test fixture setup failure
		}
	}()

	got, units, err := c.WaitForCargo(context.Background(), "OP-1", "IRON_ORE", 10)
	if err != nil {
		t.Fatalf("expected resync to recover from a lost notification, got: %v", err)
	}
	if got.ShipSymbol() != "STORAGE-1" || units != 25 {
		t.Fatalf("expected STORAGE-1 with 25 units, got %s/%d", got.ShipSymbol(), units)
	}
}

// TestWaitForCargo_NeverSatisfied_ExhaustsToTypedErrorAndUnblocksQueue proves
// the wait does NOT hang forever: if cargo never arrives, WaitForCargo gives
// up after cargoWaitMaxAttempts grace periods with a typed error - AND
// correctly removes the abandoned waiter from the FIFO queue so it does not
// permanently block a later waiter for the same operation+good
// (processWaiterQueue stops at the first unsatisfied waiter).
func TestWaitForCargo_NeverSatisfied_ExhaustsToTypedErrorAndUnblocksQueue(t *testing.T) {
	c := newTestCoordinator(10*time.Millisecond, 3)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	_, _, err := c.WaitForCargo(context.Background(), "OP-1", "IRON_ORE", 10)
	if err == nil {
		t.Fatalf("expected timeout error, got nil (wait must not silently succeed with no cargo)")
	}
	var timeoutErr *storage.ErrWaitTimeout
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *storage.ErrWaitTimeout, got %T: %v", err, err)
	}
	if timeoutErr.OperationID != "OP-1" || timeoutErr.GoodSymbol != "IRON_ORE" {
		t.Fatalf("unexpected error fields: %+v", timeoutErr)
	}

	// The abandoned waiter must not still be queued - otherwise it would
	// permanently block a subsequent waiter behind it (processWaiterQueue
	// stops at the first unsatisfied waiter in the FIFO queue).
	c.mu.RLock()
	remaining := len(c.waiters[waiterQueueKey{operationID: "OP-1", goodSymbol: "IRON_ORE"}])
	c.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("expected the abandoned waiter to be removed from the queue, found %d still queued", remaining)
	}

	// The coordinator must still be usable for the same operation+good after
	// a timeout: a fresh wait succeeds via the immediate-reservation fast
	// path. (The FIFO-specific dequeue claim is proven above by the direct
	// queue-length check, not by this call - DepositCargo bypasses the FIFO
	// notification path entirely.)
	if err := ship.DepositCargo("IRON_ORE", 15); err != nil {
		t.Fatalf("DepositCargo: %v", err)
	}
	got, units, err := c.WaitForCargo(context.Background(), "OP-1", "IRON_ORE", 10)
	if err != nil {
		t.Fatalf("expected a fresh wait to succeed after the abandoned waiter cleared, got: %v", err)
	}
	if got.ShipSymbol() != "STORAGE-1" || units != 15 {
		t.Fatalf("expected STORAGE-1 with 15 units, got %s/%d", got.ShipSymbol(), units)
	}
}

// TestWaitForCargo_ContextCancelled_ReturnsErrWaitCancelledImmediately pins
// the existing cancellation behavior: an already-cancelled context returns
// immediately without waiting for the grace period or attempting a resync.
func TestWaitForCargo_ContextCancelled_ReturnsErrWaitCancelledImmediately(t *testing.T) {
	c := newTestCoordinator(50*time.Millisecond, 3)
	ship := mustStorageShip(t, "STORAGE-1", "OP-1", 100, nil)
	if err := c.RegisterStorageShip(ship); err != nil {
		t.Fatalf("RegisterStorageShip: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := c.WaitForCargo(ctx, "OP-1", "IRON_ORE", 10)
	var cancelledErr *storage.ErrWaitCancelled
	if !errors.As(err, &cancelledErr) {
		t.Fatalf("expected *storage.ErrWaitCancelled, got %T: %v", err, err)
	}
}
