package storage

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
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
}

// NewInMemoryStorageCoordinator creates a new storage coordinator
func NewInMemoryStorageCoordinator() *InMemoryStorageCoordinator {
	return &InMemoryStorageCoordinator{
		storageShips:       make(map[string]*storage.StorageShip),
		shipsByOperation:   make(map[string][]string),
		waiters:            make(map[waiterQueueKey][]*storage.CargoWaiter),
		depositSubscribers: make(map[string][]chan storage.CargoDepositNotification),
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

	// Wait for notification or cancellation
	select {
	case <-ctx.Done():
		// Remove from queue
		c.removeWaiter(key, waiter)
		return nil, 0, &storage.ErrWaitCancelled{
			OperationID: operationID,
			GoodSymbol:  goodSymbol,
		}

	case result := <-waiter.ResultChan:
		if result.Error != nil {
			return nil, 0, result.Error
		}
		return result.StorageShip, result.UnitsReserved, nil
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

	// Notify deposit subscribers for this ship (e.g., storage ship worker for HYDROCARBON jettison)
	notification := storage.CargoDepositNotification{
		GoodSymbol: goodSymbol,
		Units:      units,
	}
	for _, ch := range c.depositSubscribers[storageShipSymbol] {
		// Non-blocking send - if subscriber's buffer is full, skip
		select {
		case ch <- notification:
		default:
		}
	}

	// Wake waiters for this operation+good
	operationID := ship.OperationID()
	key := waiterQueueKey{operationID: operationID, goodSymbol: goodSymbol}

	c.processWaiterQueue(key)
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
func (c *InMemoryStorageCoordinator) ConfirmDeposit(shipSymbol, goodSymbol string, units int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ship, exists := c.storageShips[shipSymbol]
	if !exists {
		return
	}

	// Convert reservation to actual cargo
	if err := ship.ConfirmDeposit(goodSymbol, units); err != nil {
		return
	}

	// Notify deposit subscribers for this ship (e.g., storage ship worker for HYDROCARBON jettison)
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

	// Wake waiters for this operation+good
	operationID := ship.OperationID()
	key := waiterQueueKey{operationID: operationID, goodSymbol: goodSymbol}
	c.processWaiterQueue(key)
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
	ch := make(chan storage.CargoDepositNotification, 10)
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
