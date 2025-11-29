package storage

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// RecoveryResult contains the outcome of storage recovery
type RecoveryResult struct {
	OperationsRecovered int
	ShipsRegistered     int
	Errors              []string
}

// StorageRecoveryService handles recovery of storage ship state on daemon restart.
// It loads running StorageOperations from the database and queries the Ship API
// to reconstruct the current cargo state of each storage ship.
type StorageRecoveryService struct {
	operationRepo storage.StorageOperationRepository
	apiClient     ports.APIClient
	coordinator   storage.StorageCoordinator
}

// NewStorageRecoveryService creates a new storage recovery service
func NewStorageRecoveryService(
	operationRepo storage.StorageOperationRepository,
	apiClient ports.APIClient,
	coordinator storage.StorageCoordinator,
) *StorageRecoveryService {
	return &StorageRecoveryService{
		operationRepo: operationRepo,
		apiClient:     apiClient,
		coordinator:   coordinator,
	}
}

// RecoverStorageOperations loads running storage operations for a player and
// reconstructs storage ship state from the API.
//
// This should be called when starting manufacturing for a player to ensure
// storage ships are registered with their current cargo state.
//
// Parameters:
// - ctx: Context for cancellation
// - playerID: The player whose operations to recover
// - token: The player's API token for fetching ship data
//
// Returns a RecoveryResult with counts and any errors encountered.
// Partial failures are logged but don't stop recovery of other ships.
func (s *StorageRecoveryService) RecoverStorageOperations(
	ctx context.Context,
	playerID int,
	token string,
) (*RecoveryResult, error) {
	result := &RecoveryResult{
		Errors: make([]string, 0),
	}

	// Load running storage operations from database
	operations, err := s.operationRepo.FindRunning(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load running storage operations: %w", err)
	}

	if len(operations) == 0 {
		log.Printf("[storage-recovery] No running storage operations found for player %d", playerID)
		return result, nil
	}

	log.Printf("[storage-recovery] Found %d running storage operations for player %d", len(operations), playerID)

	// Process each operation
	for _, op := range operations {
		shipsRecovered := s.recoverOperationShips(ctx, op, token, result)
		if shipsRecovered > 0 {
			result.OperationsRecovered++
		}
	}

	log.Printf("[storage-recovery] Recovery complete: %d operations, %d ships registered, %d errors",
		result.OperationsRecovered, result.ShipsRegistered, len(result.Errors))

	return result, nil
}

// recoverOperationShips recovers storage ships for a single operation
func (s *StorageRecoveryService) recoverOperationShips(
	ctx context.Context,
	op *storage.StorageOperation,
	token string,
	result *RecoveryResult,
) int {
	shipsRecovered := 0

	for _, shipSymbol := range op.StorageShips() {
		// Check if already registered (idempotent)
		if _, exists := s.coordinator.GetStorageShipBySymbol(shipSymbol); exists {
			log.Printf("[storage-recovery] Ship %s already registered, skipping", shipSymbol)
			shipsRecovered++
			continue
		}

		// Fetch current ship state from API
		shipData, err := s.apiClient.GetShip(ctx, shipSymbol, token)
		if err != nil {
			errMsg := fmt.Sprintf("failed to fetch ship %s: %v", shipSymbol, err)
			log.Printf("[storage-recovery] ERROR: %s", errMsg)
			result.Errors = append(result.Errors, errMsg)
			continue
		}

		// Convert cargo inventory to map[string]int
		initialCargo := make(map[string]int)
		for _, item := range shipData.Cargo.Inventory {
			initialCargo[item.Symbol] = item.Units
		}

		// Create StorageShip entity
		storageShip, err := storage.NewStorageShip(
			shipSymbol,
			op.WaypointSymbol(),
			op.ID(),
			shipData.Cargo.Capacity,
			initialCargo,
		)
		if err != nil {
			errMsg := fmt.Sprintf("failed to create storage ship %s: %v", shipSymbol, err)
			log.Printf("[storage-recovery] ERROR: %s", errMsg)
			result.Errors = append(result.Errors, errMsg)
			continue
		}

		// Register with coordinator
		if err := s.coordinator.RegisterStorageShip(storageShip); err != nil {
			errMsg := fmt.Sprintf("failed to register storage ship %s: %v", shipSymbol, err)
			log.Printf("[storage-recovery] ERROR: %s", errMsg)
			result.Errors = append(result.Errors, errMsg)
			continue
		}

		log.Printf("[storage-recovery] Registered ship %s (op=%s, cargo=%d/%d)",
			shipSymbol, op.ID(), shipData.Cargo.Units, shipData.Cargo.Capacity)

		result.ShipsRegistered++
		shipsRecovered++
	}

	return shipsRecovered
}

// RecoverSingleOperation recovers storage ships for a specific operation.
// Useful when starting a new operation or restarting a stopped one.
func (s *StorageRecoveryService) RecoverSingleOperation(
	ctx context.Context,
	operationID string,
	token string,
) (*RecoveryResult, error) {
	result := &RecoveryResult{
		Errors: make([]string, 0),
	}

	// Load the operation
	op, err := s.operationRepo.FindByID(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("failed to load storage operation %s: %w", operationID, err)
	}
	if op == nil {
		return nil, fmt.Errorf("storage operation %s not found", operationID)
	}

	log.Printf("[storage-recovery] Recovering operation %s", operationID)

	shipsRecovered := s.recoverOperationShips(ctx, op, token, result)
	if shipsRecovered > 0 {
		result.OperationsRecovered = 1
	}

	return result, nil
}
