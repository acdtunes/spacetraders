package storage

import "fmt"

// ErrStorageShipNotFound indicates a storage ship was not found
type ErrStorageShipNotFound struct {
	ShipSymbol string
}

func (e *ErrStorageShipNotFound) Error() string {
	return fmt.Sprintf("storage ship not found: %s", e.ShipSymbol)
}

// ErrOperationNotFound indicates a storage operation was not found
type ErrOperationNotFound struct {
	OperationID string
}

func (e *ErrOperationNotFound) Error() string {
	return fmt.Sprintf("storage operation not found: %s", e.OperationID)
}

// ErrNoStorageShipsAvailable indicates no storage ships have space
type ErrNoStorageShipsAvailable struct {
	OperationID string
	MinSpace    int
}

func (e *ErrNoStorageShipsAvailable) Error() string {
	return fmt.Sprintf("no storage ships with %d space available for operation %s", e.MinSpace, e.OperationID)
}

// ErrInsufficientStorageCargo indicates not enough cargo is available
type ErrInsufficientStorageCargo struct {
	OperationID string
	GoodSymbol  string
	Requested   int
	Available   int
}

func (e *ErrInsufficientStorageCargo) Error() string {
	return fmt.Sprintf("insufficient cargo for operation %s: need %d %s, have %d available",
		e.OperationID, e.Requested, e.GoodSymbol, e.Available)
}

// ErrStorageShipAlreadyRegistered indicates ship is already in coordinator
type ErrStorageShipAlreadyRegistered struct {
	ShipSymbol string
}

func (e *ErrStorageShipAlreadyRegistered) Error() string {
	return fmt.Sprintf("storage ship already registered: %s", e.ShipSymbol)
}

// ErrWaitCancelled indicates a cargo wait was cancelled
type ErrWaitCancelled struct {
	OperationID string
	GoodSymbol  string
}

func (e *ErrWaitCancelled) Error() string {
	return fmt.Sprintf("cargo wait cancelled for operation %s, good %s", e.OperationID, e.GoodSymbol)
}

// ErrInvalidOperationState indicates operation is in wrong state for action
type ErrInvalidOperationState struct {
	OperationID   string
	CurrentStatus OperationStatus
	Action        string
}

func (e *ErrInvalidOperationState) Error() string {
	return fmt.Sprintf("cannot %s operation %s in %s state", e.Action, e.OperationID, e.CurrentStatus)
}
