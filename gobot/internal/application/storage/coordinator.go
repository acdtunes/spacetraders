package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

const depositSubscriberBufferSize = 10

// Default grace period and attempt budget for WaitForCargo's
// timeout->resync->park backstop (sp-pafv, subsumes sp-0e10). A waiter is
// woken via processWaiterQueue, called from NotifyCargoDeposited/
// ConfirmDeposit/RegisterStorageShip. If a deposit lands and
// processWaiterQueue runs in the gap between tryImmediateReservation
// returning "nothing available" and the waiter actually being enqueued
// below, that wake-up is permanently missed - a lost-wakeup race. Without a
// bound, a lost/raced wake-up stalls the waiting worker permanently. These
// defaults only govern the timeout leg - the happy path (FIFO delivery) is
// unaffected and still returns as soon as the notification shows up.
const (
	DefaultCargoWaitGracePeriod = 30 * time.Second
	DefaultCargoWaitMaxAttempts = 3
)

// waiterQueueKey creates a unique key for operation+good combinations
type waiterQueueKey struct {
	operationID string
	goodSymbol  string
}

// InMemoryStorageCoordinator implements StorageCoordinator with in-memory state.
// It manages all storage ships across all operations with FIFO waiter queues.
//
// Thread-Safety:
// Uses a two-level locking strategy:
// 1. Coordinator mutex (coarse): protects storageShips map, waiters map
// 2. StorageShip mutex (fine): protects individual cargo operations
//
// FIFO Queue Guarantees:
// - Waiters are served in arrival order (fair scheduling)
// - Prevents starvation of long-waiting haulers
// - Each (operationID, goodSymbol) has a separate queue
type InMemoryStorageCoordinator struct {
	mu sync.RWMutex

	// storageShips maps shipSymbol -> StorageShip
	storageShips map[string]*storage.StorageShip

	// shipsByOperation maps operationID -> []shipSymbol
	shipsByOperation map[string][]string

	// waiters maps (operationID, goodSymbol) -> FIFO queue of waiters
	waiters map[waiterQueueKey][]*storage.CargoWaiter

	// depositSubscribers maps shipSymbol -> list of subscriber channels
	// Used to notify storage ship workers when cargo is deposited
	depositSubscribers map[string][]chan storage.CargoDepositNotification

	// cargoWaitGracePeriod and cargoWaitMaxAttempts bound WaitForCargo's
	// timeout->resync->park backstop (sp-pafv). See the Default* constants
	// above for the rationale; production always uses those defaults, tests
	// construct the struct directly with tiny overrides.
	cargoWaitGracePeriod time.Duration
	cargoWaitMaxAttempts int

	// operationBasis records the per-(operation, good) weighted-average unit
	// cost basis of deposited stock (C1, sp-64je). Written only by
	// ConfirmDepositWithBasis (the factory-output deposit path); read by
	// GetCostBasis so the tour solver can price warehouse stock as a
	// zero-ask-at-basis source. A good absent here has an UNKNOWN basis
	// (fail-closed — not offered as a solver stock source). Lazily initialised
	// so the test constructor and NewInMemoryStorageCoordinator need no change.
	// See coordinator_basis.go.
	operationBasis map[string]map[string]int

	// basisStore optionally persists operationBasis so it survives daemon
	// restart (RULINGS #2). nil (the default) keeps basis in-memory only —
	// tests, and deployments not using planner-visible stock. See
	// coordinator_basis.go.
	basisStore CostBasisStore
}

// NewInMemoryStorageCoordinator creates a new storage coordinator
func NewInMemoryStorageCoordinator() *InMemoryStorageCoordinator {
	return &InMemoryStorageCoordinator{
		storageShips:         make(map[string]*storage.StorageShip),
		shipsByOperation:     make(map[string][]string),
		waiters:              make(map[waiterQueueKey][]*storage.CargoWaiter),
		depositSubscribers:   make(map[string][]chan storage.CargoDepositNotification),
		cargoWaitGracePeriod: DefaultCargoWaitGracePeriod,
		cargoWaitMaxAttempts: DefaultCargoWaitMaxAttempts,
	}
}

// RegisterStorageShip adds a storage ship to the coordinator
func (c *InMemoryStorageCoordinator) RegisterStorageShip(ship *storage.StorageShip) error {
	if ship == nil {
		return fmt.Errorf("storage ship cannot be nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.storageShips[ship.ShipSymbol()]; exists {
		return &storage.ErrStorageShipAlreadyRegistered{ShipSymbol: ship.ShipSymbol()}
	}

	c.storageShips[ship.ShipSymbol()] = ship

	// Add to operation index
	opID := ship.OperationID()
	c.shipsByOperation[opID] = append(c.shipsByOperation[opID], ship.ShipSymbol())

	// Wake any waiters for goods that this ship has in initial cargo.
	// This is critical for recovery scenarios where haulers start waiting
	// BEFORE storage ships register (race condition after daemon restart).
	inventory := ship.GetInventory()
	for goodSymbol := range inventory {
		key := waiterQueueKey{operationID: opID, goodSymbol: goodSymbol}
		c.processWaiterQueue(key)
	}

	return nil
}

// UnregisterStorageShip removes a storage ship from the coordinator
func (c *InMemoryStorageCoordinator) UnregisterStorageShip(shipSymbol string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ship, exists := c.storageShips[shipSymbol]
	if !exists {
		return
	}

	opID := ship.OperationID()

	// Remove from ships map
	delete(c.storageShips, shipSymbol)

	// Remove from operation index
	ships := c.shipsByOperation[opID]
	for i, s := range ships {
		if s == shipSymbol {
			c.shipsByOperation[opID] = append(ships[:i], ships[i+1:]...)
			break
		}
	}

	// Notify all waiters for this operation that a ship is gone
	// They will need to check if cargo is still available
	for key, waiterQueue := range c.waiters {
		if key.operationID == opID {
			for _, waiter := range waiterQueue {
				waiter.ResultChan <- &storage.CargoWaiterResult{
					Error: &storage.ErrStorageShipNotFound{ShipSymbol: shipSymbol},
				}
			}
			delete(c.waiters, key)
		}
	}
}

// WaitForCargo blocks until cargo is available and reserved.
func (c *InMemoryStorageCoordinator) WaitForCargo(
	ctx context.Context,
	operationID, goodSymbol string,
	minUnits int,
) (*storage.StorageShip, int, error) {
	if minUnits <= 0 {
		return nil, 0, fmt.Errorf("minUnits must be positive")
	}

	// Try immediate reservation first
	ship, units, err := c.tryImmediateReservation(operationID, goodSymbol, minUnits)
	if err != nil {
		return nil, 0, err
	}
	if ship != nil {
		return ship, units, nil
	}

	// No immediate cargo available, wait in queue
	waiter := &storage.CargoWaiter{
		OperationID: operationID,
		GoodSymbol:  goodSymbol,
		MinUnits:    minUnits,
		ResultChan:  make(chan *storage.CargoWaiterResult, 1),
	}

	// Add to queue
	c.mu.Lock()
	key := waiterQueueKey{operationID: operationID, goodSymbol: goodSymbol}
	c.waiters[key] = append(c.waiters[key], waiter)
	c.mu.Unlock()

	return c.awaitCargo(ctx, key, waiter)
}

// awaitCargo waits for a queued CargoWaiter to be satisfied via the FIFO
// notification path (NotifyCargoDeposited/ConfirmDeposit/RegisterStorageShip
// call processWaiterQueue), with a timeout->resync->park backstop
// (sp-pafv, subsumes sp-0e10). If the notification that would have woken
// this waiter is lost - e.g. a deposit lands and processWaiterQueue runs
// BEFORE this waiter's enqueue above completes, a genuine lost-wakeup race -
// the wait would otherwise block forever. Instead, each grace period this
// resyncs by re-checking cargo directly against the operation's storage
// ships (tryImmediateReservation), bypassing the FIFO channel entirely. If
// cargo still never shows up after cargoWaitMaxAttempts, the wait gives up
// with a typed error instead of hanging, and removes the abandoned waiter
// from the queue so it cannot permanently block waiters behind it
// (processWaiterQueue stops at the first unsatisfied waiter in FIFO order).
func (c *InMemoryStorageCoordinator) awaitCargo(
	ctx context.Context,
	key waiterQueueKey,
	waiter *storage.CargoWaiter,
) (*storage.StorageShip, int, error) {
	for attempt := 1; attempt <= c.cargoWaitMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			c.removeWaiter(key, waiter)
			return nil, 0, &storage.ErrWaitCancelled{
				OperationID: key.operationID,
				GoodSymbol:  key.goodSymbol,
			}

		case result := <-waiter.ResultChan:
			if result.Error != nil {
				return nil, 0, result.Error
			}
			return result.StorageShip, result.UnitsReserved, nil

		case <-time.After(c.cargoWaitGracePeriod):
			// No FIFO notification within the grace period. Resync directly
			// against ship state instead of assuming the notification is
			// merely slow - it may have been lost to the enqueue race
			// described above. removeWaiter is required on success too: if
			// processWaiterQueue is concurrently satisfying this SAME
			// waiter via the FIFO path right as this resync also succeeds,
			// leaving the waiter in the queue would either strand a
			// duplicate reservation on waiter.ResultChan forever, or leave
			// a phantom "already served" waiter clogging the FIFO queue.
			ship, units, err := c.tryImmediateReservation(key.operationID, key.goodSymbol, waiter.MinUnits)
			if err == nil && ship != nil {
				c.removeWaiter(key, waiter)
				return ship, units, nil
			}
			// Still nothing available (or a transient lookup error): loop
			// back for another grace period. A concurrently-delivered FIFO
			// result, if any, is picked up by this same select on the next
			// iteration.
		}
	}

	// Exhausted every attempt. Do one final non-blocking drain: a FIFO
	// delivery may have landed in the same instant the last grace period
	// elapsed (the same benign race described above) - prefer a real
	// reservation over declaring failure so it is never silently leaked.
	select {
	case result := <-waiter.ResultChan:
		if result.Error != nil {
			return nil, 0, result.Error
		}
		return result.StorageShip, result.UnitsReserved, nil
	default:
	}

	// Give up: remove the waiter so it cannot permanently block whoever is
	// queued behind it, then park by returning a typed error instead of
	// blocking forever.
	c.removeWaiter(key, waiter)
	return nil, 0, &storage.ErrWaitTimeout{
		OperationID: key.operationID,
		GoodSymbol:  key.goodSymbol,
	}
}

// tryImmediateReservation attempts to reserve cargo without waiting
func (c *InMemoryStorageCoordinator) tryImmediateReservation(
	operationID, goodSymbol string,
	minUnits int,
) (*storage.StorageShip, int, error) {
	c.mu.RLock()
	shipSymbols := c.shipsByOperation[operationID]
	c.mu.RUnlock()

	if len(shipSymbols) == 0 {
		return nil, 0, &storage.ErrOperationNotFound{OperationID: operationID}
	}

	// Try each storage ship
	for _, symbol := range shipSymbols {
		c.mu.RLock()
		ship := c.storageShips[symbol]
		c.mu.RUnlock()

		if ship == nil {
			continue
		}

		// Try to reserve from this ship
		units, err := ship.TryReserveCargo(goodSymbol, minUnits)
		if err != nil {
			continue
		}
		if units > 0 {
			return ship, units, nil
		}
	}

	// No cargo available for immediate reservation
	return nil, 0, nil
}

// removeWaiter removes a waiter from the queue (used on cancellation)
func (c *InMemoryStorageCoordinator) removeWaiter(key waiterQueueKey, waiter *storage.CargoWaiter) {
	c.mu.Lock()
	defer c.mu.Unlock()

	queue := c.waiters[key]
	for i, w := range queue {
		if w == waiter {
			c.waiters[key] = append(queue[:i], queue[i+1:]...)
			break
		}
	}

	// Clean up empty queues
	if len(c.waiters[key]) == 0 {
		delete(c.waiters, key)
	}
}

// NotifyCargoDeposited is called by extractors after depositing cargo.
func (c *InMemoryStorageCoordinator) NotifyCargoDeposited(storageShipSymbol, goodSymbol string, units int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ship, exists := c.storageShips[storageShipSymbol]
	if !exists {
		return
	}

	// Deposit the cargo to the ship
	if err := ship.DepositCargo(goodSymbol, units); err != nil {
		// Log error but continue - cargo was already transferred via API
		return
	}

	c.notifyDepositSubscribers(storageShipSymbol, goodSymbol, units)

	// Wake waiters for this operation+good
	operationID := ship.OperationID()
	key := waiterQueueKey{operationID: operationID, goodSymbol: goodSymbol}

	c.processWaiterQueue(key)
}

func (c *InMemoryStorageCoordinator) notifyDepositSubscribers(shipSymbol, goodSymbol string, units int) {
	notification := storage.CargoDepositNotification{
		GoodSymbol: goodSymbol,
		Units:      units,
	}
	for _, ch := range c.depositSubscribers[shipSymbol] {
		select {
		case ch <- notification:
		default:
		}
	}
}

// NotifyCargoJettisoned is called after cargo is jettisoned from a storage ship.
// This updates the coordinator's internal cargo tracking so AvailableSpace() is accurate.
func (c *InMemoryStorageCoordinator) NotifyCargoJettisoned(storageShipSymbol, goodSymbol string, units int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ship, exists := c.storageShips[storageShipSymbol]
	if !exists {
		return
	}

	// Remove the cargo from the ship's inventory
	if err := ship.JettisonCargo(goodSymbol, units); err != nil {
		// Log error but continue - cargo was already jettisoned via API
		return
	}
}

// processWaiterQueue attempts to satisfy waiters in FIFO order
func (c *InMemoryStorageCoordinator) processWaiterQueue(key waiterQueueKey) {
	queue := c.waiters[key]
	if len(queue) == 0 {
		return
	}

	// Get all ships for this operation
	shipSymbols := c.shipsByOperation[key.operationID]

	// Process waiters in FIFO order
	var remaining []*storage.CargoWaiter

	for i, waiter := range queue {
		satisfied := false

		// Try to reserve from any ship
		for _, symbol := range shipSymbols {
			ship := c.storageShips[symbol]
			if ship == nil {
				continue
			}

			units, err := ship.TryReserveCargo(key.goodSymbol, waiter.MinUnits)
			if err == nil && units > 0 {
				// Reservation successful, notify waiter
				waiter.ResultChan <- &storage.CargoWaiterResult{
					StorageShip:   ship,
					UnitsReserved: units,
				}
				satisfied = true
				break
			}
		}

		if !satisfied {
			// Not enough cargo for this waiter, keep in queue
			// Stop processing - FIFO order means we can't skip ahead
			// Capture current waiter and all remaining waiters
			remaining = queue[i:]
			break
		}
	}

	if len(remaining) == 0 {
		delete(c.waiters, key)
	} else {
		c.waiters[key] = remaining
	}
}

// GetTotalCargoAvailable returns total unreserved cargo for a good
func (c *InMemoryStorageCoordinator) GetTotalCargoAvailable(operationID, goodSymbol string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := 0
	for _, symbol := range c.shipsByOperation[operationID] {
		if ship := c.storageShips[symbol]; ship != nil {
			total += ship.GetAvailableCargo(goodSymbol)
		}
	}
	return total
}

// FindStorageShipWithSpace finds a storage ship with available space
func (c *InMemoryStorageCoordinator) FindStorageShipWithSpace(operationID string, minSpace int) (*storage.StorageShip, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, symbol := range c.shipsByOperation[operationID] {
		if ship := c.storageShips[symbol]; ship != nil {
			if ship.AvailableSpace() >= minSpace {
				return ship, true
			}
		}
	}
	return nil, false
}

// ReserveSpaceForDeposit atomically finds a storage ship with space AND reserves it.
// This prevents race conditions where multiple extractors try to deposit to the same ship.
func (c *InMemoryStorageCoordinator) ReserveSpaceForDeposit(operationID string, units int) (*storage.StorageShip, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, symbol := range c.shipsByOperation[operationID] {
		ship := c.storageShips[symbol]
		if ship == nil {
			continue
		}

		available := ship.AvailableSpace()
		if available <= 0 {
			continue
		}

		// Reserve up to what's available (may be less than requested)
		toReserve := units
		if toReserve > available {
			toReserve = available
		}

		// Reserve the space atomically
		if err := ship.ReserveSpace(toReserve); err == nil {
			return ship, toReserve, true
		}
	}

	return nil, 0, false
}

// ConfirmDeposit converts a space reservation into actual cargo after successful API transfer.
// It records NO cost basis (the contract/gas deposit paths); the good's basis
// stays UNKNOWN. The factory-output path uses ConfirmDepositWithBasis instead
// (C1, sp-64je, coordinator_basis.go).
func (c *InMemoryStorageCoordinator) ConfirmDeposit(shipSymbol, goodSymbol string, units int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.confirmDepositLocked(shipSymbol, goodSymbol, units)
}

// confirmDepositLocked converts a space reservation into actual cargo and wakes
// waiters. Caller must hold c.mu. Returns false if the ship is unknown or the
// ship-level ConfirmDeposit fails (capacity invariant). Shared by ConfirmDeposit
// and ConfirmDepositWithBasis.
func (c *InMemoryStorageCoordinator) confirmDepositLocked(shipSymbol, goodSymbol string, units int) bool {
	ship, exists := c.storageShips[shipSymbol]
	if !exists {
		return false
	}

	// Convert reservation to actual cargo
	if err := ship.ConfirmDeposit(goodSymbol, units); err != nil {
		return false
	}

	c.notifyDepositSubscribers(shipSymbol, goodSymbol, units)

	// Wake waiters for this operation+good
	operationID := ship.OperationID()
	key := waiterQueueKey{operationID: operationID, goodSymbol: goodSymbol}
	c.processWaiterQueue(key)
	return true
}

// ReleaseReservedSpace releases a space reservation when a transfer fails.
func (c *InMemoryStorageCoordinator) ReleaseReservedSpace(shipSymbol string, units int) {
	c.mu.RLock()
	ship, exists := c.storageShips[shipSymbol]
	c.mu.RUnlock()

	if !exists {
		return
	}

	ship.ReleaseReservedSpace(units)
}

// GetStorageShipBySymbol retrieves a storage ship by its symbol
func (c *InMemoryStorageCoordinator) GetStorageShipBySymbol(shipSymbol string) (*storage.StorageShip, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ship, exists := c.storageShips[shipSymbol]
	return ship, exists
}

// GetStorageShipsForOperation returns all storage ships for an operation
func (c *InMemoryStorageCoordinator) GetStorageShipsForOperation(operationID string) []*storage.StorageShip {
	c.mu.RLock()
	defer c.mu.RUnlock()

	symbols := c.shipsByOperation[operationID]
	ships := make([]*storage.StorageShip, 0, len(symbols))

	for _, symbol := range symbols {
		if ship := c.storageShips[symbol]; ship != nil {
			ships = append(ships, ship)
		}
	}

	return ships
}

// SubscribeToDeposits returns a channel that receives notifications when
// cargo is deposited to a specific storage ship.
func (c *InMemoryStorageCoordinator) SubscribeToDeposits(shipSymbol string) (<-chan storage.CargoDepositNotification, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Create buffered channel to avoid blocking the coordinator
	ch := make(chan storage.CargoDepositNotification, depositSubscriberBufferSize)
	c.depositSubscribers[shipSymbol] = append(c.depositSubscribers[shipSymbol], ch)

	// Return unsubscribe function
	unsubscribe := func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		subs := c.depositSubscribers[shipSymbol]
		for i, sub := range subs {
			if sub == ch {
				c.depositSubscribers[shipSymbol] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		// Clean up empty slices
		if len(c.depositSubscribers[shipSymbol]) == 0 {
			delete(c.depositSubscribers, shipSymbol)
		}
		close(ch)
	}

	return ch, unsubscribe
}

// Verify interface implementation
var _ storage.StorageCoordinator = (*InMemoryStorageCoordinator)(nil)
