package storage

import (
	"fmt"
	"sync"
)

// StorageShip represents a ship dedicated to buffering cargo at a storage operation.
// These ships stay IN_ORBIT at the operation waypoint and receive cargo from extractors.
// Cargo is held until haulers collect via STORAGE_ACQUIRE_DELIVER tasks.
//
// Thread-Safety:
// All cargo operations are protected by a mutex to handle concurrent access from:
// - Extractor workers depositing cargo
// - Haulers reserving and transferring cargo
// - Coordinator querying available cargo
//
// Invariants:
// - Reserved cargo never exceeds available cargo
// - Total cargo (inventory) never exceeds capacity
// - Ship remains at operationWaypoint (never navigates)
type StorageShip struct {
	mu sync.RWMutex

	shipSymbol        string
	waypointSymbol    string
	operationID       string
	cargoCapacity     int
	cargoInventory    map[string]int // goodSymbol -> units held
	reservedCargo     map[string]int // goodSymbol -> units reserved for haulers
	reservedSpace     int            // space reserved for incoming deposits (not yet transferred)
}

// NewStorageShip creates a new storage ship entity.
// Initial cargo can be provided for recovery from API state.
func NewStorageShip(
	shipSymbol string,
	waypointSymbol string,
	operationID string,
	cargoCapacity int,
	initialCargo map[string]int,
) (*StorageShip, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol cannot be empty")
	}
	if waypointSymbol == "" {
		return nil, fmt.Errorf("waypoint symbol cannot be empty")
	}
	if operationID == "" {
		return nil, fmt.Errorf("operation ID cannot be empty")
	}
	if cargoCapacity < 0 {
		return nil, fmt.Errorf("cargo capacity cannot be negative")
	}

	inventory := make(map[string]int)
	totalUnits := 0
	for good, units := range initialCargo {
		if units < 0 {
			return nil, fmt.Errorf("initial cargo for %s cannot be negative", good)
		}
		inventory[good] = units
		totalUnits += units
	}

	if totalUnits > cargoCapacity {
		return nil, fmt.Errorf("initial cargo (%d) exceeds capacity (%d)", totalUnits, cargoCapacity)
	}

	return &StorageShip{
		shipSymbol:     shipSymbol,
		waypointSymbol: waypointSymbol,
		operationID:    operationID,
		cargoCapacity:  cargoCapacity,
		cargoInventory: inventory,
		reservedCargo:  make(map[string]int),
	}, nil
}

// Getters

func (s *StorageShip) ShipSymbol() string     { return s.shipSymbol }
func (s *StorageShip) WaypointSymbol() string { return s.waypointSymbol }
func (s *StorageShip) OperationID() string    { return s.operationID }
func (s *StorageShip) CargoCapacity() int     { return s.cargoCapacity }

// TotalCargoUnits returns total cargo units held (including reserved).
// Thread-safe.
func (s *StorageShip) TotalCargoUnits() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, units := range s.cargoInventory {
		total += units
	}
	return total
}

// AvailableSpace returns cargo space available for new deposits.
// Thread-safe.
func (s *StorageShip) AvailableSpace() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.availableSpaceUnsafe()
}

func (s *StorageShip) availableSpaceUnsafe() int {
	total := 0
	for _, units := range s.cargoInventory {
		total += units
	}
	// Subtract both actual cargo AND reserved space (pending deposits)
	return s.cargoCapacity - total - s.reservedSpace
}

// GetInventory returns a copy of the current cargo inventory.
// Thread-safe.
func (s *StorageShip) GetInventory() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]int)
	for good, units := range s.cargoInventory {
		result[good] = units
	}
	return result
}

// GetCargoUnits returns total units of a specific good.
// Thread-safe.
func (s *StorageShip) GetCargoUnits(goodSymbol string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.cargoInventory[goodSymbol]
}

// GetAvailableCargo returns unreserved cargo for a specific good.
// Formula: inventory[good] - reserved[good]
// Thread-safe.
func (s *StorageShip) GetAvailableCargo(goodSymbol string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getAvailableCargoUnsafe(goodSymbol)
}

func (s *StorageShip) getAvailableCargoUnsafe(goodSymbol string) int {
	inventory := s.cargoInventory[goodSymbol]
	reserved := s.reservedCargo[goodSymbol]
	available := inventory - reserved
	if available < 0 {
		return 0
	}
	return available
}

// GetReservedCargo returns reserved cargo for a specific good.
// Thread-safe.
func (s *StorageShip) GetReservedCargo(goodSymbol string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.reservedCargo[goodSymbol]
}

// Cargo Operations

// DepositCargo adds cargo from an extractor ship.
// Called after successful API transfer from extractor to storage ship.
// Thread-safe.
func (s *StorageShip) DepositCargo(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("deposit units must be positive")
	}
	if goodSymbol == "" {
		return fmt.Errorf("good symbol cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.availableSpaceUnsafe() < units {
		return fmt.Errorf("insufficient space: need %d, have %d", units, s.availableSpaceUnsafe())
	}

	s.cargoInventory[goodSymbol] += units
	return nil
}

// JettisonCargo removes cargo from inventory without reservation.
// Used for jettisoning worthless cargo like HYDROCARBON.
// Thread-safe.
func (s *StorageShip) JettisonCargo(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("jettison units must be positive")
	}
	if goodSymbol == "" {
		return fmt.Errorf("good symbol cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	inventory := s.cargoInventory[goodSymbol]
	if inventory < units {
		return fmt.Errorf("insufficient cargo: want to remove %d %s, have %d", units, goodSymbol, inventory)
	}

	s.cargoInventory[goodSymbol] -= units

	// Clean up zero entry
	if s.cargoInventory[goodSymbol] == 0 {
		delete(s.cargoInventory, goodSymbol)
	}

	return nil
}

// ReserveSpace reserves space for an incoming deposit.
// Must be called BEFORE the API transfer to prevent race conditions.
// Thread-safe.
func (s *StorageShip) ReserveSpace(units int) error {
	if units <= 0 {
		return fmt.Errorf("reserve units must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.availableSpaceUnsafe() < units {
		return fmt.Errorf("insufficient space: need %d, have %d", units, s.availableSpaceUnsafe())
	}

	s.reservedSpace += units
	return nil
}

// ReleaseReservedSpace releases a space reservation when a transfer fails.
// Thread-safe.
func (s *StorageShip) ReleaseReservedSpace(units int) {
	if units <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.reservedSpace -= units
	if s.reservedSpace < 0 {
		s.reservedSpace = 0
	}
}

// ConfirmDeposit converts a space reservation into actual cargo after successful API transfer.
// This atomically releases the reservation and adds the cargo.
// Thread-safe.
func (s *StorageShip) ConfirmDeposit(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("deposit units must be positive")
	}
	if goodSymbol == "" {
		return fmt.Errorf("good symbol cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Release the reservation
	s.reservedSpace -= units
	if s.reservedSpace < 0 {
		s.reservedSpace = 0
	}

	// Add to inventory
	s.cargoInventory[goodSymbol] += units
	return nil
}

// ReserveCargo reserves cargo for a hauler.
// The reservation is held until ConfirmTransfer or CancelReservation.
// Thread-safe.
//
// Invariant: reserved[good] <= inventory[good]
func (s *StorageShip) ReserveCargo(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("reserve units must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.getAvailableCargoUnsafe(goodSymbol)
	if available < units {
		return fmt.Errorf("insufficient cargo: need %d %s, have %d available", units, goodSymbol, available)
	}

	s.reservedCargo[goodSymbol] += units
	return nil
}

// TryReserveCargo attempts to reserve cargo atomically.
// Returns (units actually reserved, error).
// If minUnits is not available, returns 0 with no error (caller decides what to do).
// Reserves ALL available cargo (not just minUnits) to maximize transfer efficiency.
// Thread-safe.
func (s *StorageShip) TryReserveCargo(goodSymbol string, minUnits int) (int, error) {
	if minUnits <= 0 {
		return 0, fmt.Errorf("min units must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.getAvailableCargoUnsafe(goodSymbol)
	if available < minUnits {
		return 0, nil // Not enough cargo, but not an error
	}

	// Reserve ALL available cargo (not just minUnits) to maximize transfer efficiency.
	// This is crucial for gas operations where mixed cargo means we can't wait for
	// a full ship's worth of a single good type.
	s.reservedCargo[goodSymbol] += available
	return available, nil
}

// ConfirmTransfer completes a cargo transfer after successful API call.
// Removes both inventory and reservation.
// Called after hauler successfully transfers cargo from storage ship.
// Thread-safe.
func (s *StorageShip) ConfirmTransfer(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("transfer units must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	reserved := s.reservedCargo[goodSymbol]
	if reserved < units {
		return fmt.Errorf("cannot confirm transfer of %d %s: only %d reserved", units, goodSymbol, reserved)
	}

	inventory := s.cargoInventory[goodSymbol]
	if inventory < units {
		return fmt.Errorf("cannot confirm transfer of %d %s: only %d in inventory", units, goodSymbol, inventory)
	}

	s.reservedCargo[goodSymbol] -= units
	s.cargoInventory[goodSymbol] -= units

	// Clean up zero entries
	if s.reservedCargo[goodSymbol] == 0 {
		delete(s.reservedCargo, goodSymbol)
	}
	if s.cargoInventory[goodSymbol] == 0 {
		delete(s.cargoInventory, goodSymbol)
	}

	return nil
}

// CancelReservation releases a reservation when transfer fails or is cancelled.
// Thread-safe.
func (s *StorageShip) CancelReservation(goodSymbol string, units int) error {
	if units <= 0 {
		return fmt.Errorf("cancel units must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	reserved := s.reservedCargo[goodSymbol]
	if reserved < units {
		return fmt.Errorf("cannot cancel %d %s: only %d reserved", units, goodSymbol, reserved)
	}

	s.reservedCargo[goodSymbol] -= units

	if s.reservedCargo[goodSymbol] == 0 {
		delete(s.reservedCargo, goodSymbol)
	}

	return nil
}

// HasAvailableCargo checks if there's unreserved cargo of the specified good.
// Thread-safe.
func (s *StorageShip) HasAvailableCargo(goodSymbol string, minUnits int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getAvailableCargoUnsafe(goodSymbol) >= minUnits
}

// GetSupportedGoods returns all goods currently in inventory.
// Thread-safe.
func (s *StorageShip) GetSupportedGoods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	goods := make([]string, 0, len(s.cargoInventory))
	for good := range s.cargoInventory {
		goods = append(goods, good)
	}
	return goods
}

// String provides human-readable representation
func (s *StorageShip) String() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, units := range s.cargoInventory {
		total += units
	}
	totalReserved := 0
	for _, units := range s.reservedCargo {
		totalReserved += units
	}

	return fmt.Sprintf("StorageShip[%s, op=%s, cargo=%d/%d, reserved=%d]",
		s.shipSymbol, s.operationID, total, s.cargoCapacity, totalReserved)
}
