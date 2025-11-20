package mining

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TransferStatus represents the state of a cargo transfer request
type TransferStatus string

const (
	TransferStatusPending    TransferStatus = "PENDING"
	TransferStatusInProgress TransferStatus = "IN_PROGRESS"
	TransferStatusCompleted  TransferStatus = "COMPLETED"
)

// CargoTransferRequest represents an immutable transfer request between ships
// This is a value object that tracks the transfer of cargo from a miner to a transport.
type CargoTransferRequest struct {
	id                string
	miningOperationID string
	minerShip         string
	transportShip     string                // May be empty if pending assignment
	cargoManifest     []shared.CargoItem    // Items to transfer
	status            TransferStatus
	createdAt         time.Time
	completedAt       *time.Time
}

// NewCargoTransferRequest creates a new cargo transfer request
func NewCargoTransferRequest(
	id string,
	miningOperationID string,
	minerShip string,
	cargoManifest []shared.CargoItem,
) *CargoTransferRequest {
	// Copy cargo manifest to ensure immutability
	manifest := make([]shared.CargoItem, len(cargoManifest))
	copy(manifest, cargoManifest)

	return &CargoTransferRequest{
		id:                id,
		miningOperationID: miningOperationID,
		minerShip:         minerShip,
		cargoManifest:     manifest,
		status:            TransferStatusPending,
		createdAt:         time.Now(),
	}
}

// Getters

func (r *CargoTransferRequest) ID() string                        { return r.id }
func (r *CargoTransferRequest) MiningOperationID() string         { return r.miningOperationID }
func (r *CargoTransferRequest) MinerShip() string                 { return r.minerShip }
func (r *CargoTransferRequest) TransportShip() string             { return r.transportShip }
func (r *CargoTransferRequest) CargoManifest() []shared.CargoItem { return r.cargoManifest }
func (r *CargoTransferRequest) Status() TransferStatus            { return r.status }
func (r *CargoTransferRequest) CreatedAt() time.Time              { return r.createdAt }
func (r *CargoTransferRequest) CompletedAt() *time.Time           { return r.completedAt }

// Value object operations - return new instances

// WithTransportShip returns a new CargoTransferRequest with the transport ship assigned
func (r *CargoTransferRequest) WithTransportShip(transportShip string) *CargoTransferRequest {
	manifest := make([]shared.CargoItem, len(r.cargoManifest))
	copy(manifest, r.cargoManifest)

	return &CargoTransferRequest{
		id:                r.id,
		miningOperationID: r.miningOperationID,
		minerShip:         r.minerShip,
		transportShip:     transportShip,
		cargoManifest:     manifest,
		status:            TransferStatusInProgress,
		createdAt:         r.createdAt,
		completedAt:       r.completedAt,
	}
}

// WithCompleted returns a new CargoTransferRequest marked as completed
func (r *CargoTransferRequest) WithCompleted(completedAt time.Time) *CargoTransferRequest {
	manifest := make([]shared.CargoItem, len(r.cargoManifest))
	copy(manifest, r.cargoManifest)

	return &CargoTransferRequest{
		id:                r.id,
		miningOperationID: r.miningOperationID,
		minerShip:         r.minerShip,
		transportShip:     r.transportShip,
		cargoManifest:     manifest,
		status:            TransferStatusCompleted,
		createdAt:         r.createdAt,
		completedAt:       &completedAt,
	}
}

// State queries

// IsPending returns true if the transfer is waiting for assignment
func (r *CargoTransferRequest) IsPending() bool {
	return r.status == TransferStatusPending
}

// IsInProgress returns true if the transfer is being executed
func (r *CargoTransferRequest) IsInProgress() bool {
	return r.status == TransferStatusInProgress
}

// IsCompleted returns true if the transfer has been completed
func (r *CargoTransferRequest) IsCompleted() bool {
	return r.status == TransferStatusCompleted
}

// TotalUnits calculates the total cargo units in the manifest
func (r *CargoTransferRequest) TotalUnits() int {
	total := 0
	for _, item := range r.cargoManifest {
		total += item.Units
	}
	return total
}

// CargoTransferRequestData is the DTO for persisting cargo transfer requests
type CargoTransferRequestData struct {
	ID                string
	MiningOperationID string
	MinerShip         string
	TransportShip     string
	CargoManifest     []shared.CargoItem
	Status            string
	CreatedAt         time.Time
	CompletedAt       *time.Time
}

// ToData converts the value object to a DTO for persistence
func (r *CargoTransferRequest) ToData() *CargoTransferRequestData {
	return &CargoTransferRequestData{
		ID:                r.id,
		MiningOperationID: r.miningOperationID,
		MinerShip:         r.minerShip,
		TransportShip:     r.transportShip,
		CargoManifest:     r.cargoManifest,
		Status:            string(r.status),
		CreatedAt:         r.createdAt,
		CompletedAt:       r.completedAt,
	}
}

// CargoTransferRequestFromData creates a CargoTransferRequest from a DTO
func CargoTransferRequestFromData(data *CargoTransferRequestData) *CargoTransferRequest {
	manifest := make([]shared.CargoItem, len(data.CargoManifest))
	copy(manifest, data.CargoManifest)

	return &CargoTransferRequest{
		id:                data.ID,
		miningOperationID: data.MiningOperationID,
		minerShip:         data.MinerShip,
		transportShip:     data.TransportShip,
		cargoManifest:     manifest,
		status:            TransferStatus(data.Status),
		createdAt:         data.CreatedAt,
		completedAt:       data.CompletedAt,
	}
}
