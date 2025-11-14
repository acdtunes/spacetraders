package shared

import "fmt"

// DomainError is the base error type for all domain errors
type DomainError struct {
	Message string
}

func (e *DomainError) Error() string {
	return e.Message
}

func NewDomainError(message string) *DomainError {
	return &DomainError{Message: message}
}

// Ship-related errors

type ShipError struct {
	*DomainError
}

func NewShipError(message string) *ShipError {
	return &ShipError{DomainError: &DomainError{Message: message}}
}

type InvalidNavStatusError struct {
	*ShipError
}

func NewInvalidNavStatusError(message string) *InvalidNavStatusError {
	return &InvalidNavStatusError{ShipError: NewShipError(message)}
}

type InsufficientFuelError struct {
	*ShipError
	Required  int
	Available int
}

func NewInsufficientFuelError(required, available int) *InsufficientFuelError {
	return &InsufficientFuelError{
		ShipError: NewShipError(fmt.Sprintf("insufficient fuel: need %d, have %d", required, available)),
		Required:  required,
		Available: available,
	}
}

type InvalidShipDataError struct {
	*ShipError
}

func NewInvalidShipDataError(message string) *InvalidShipDataError {
	return &InvalidShipDataError{ShipError: NewShipError(message)}
}

// Validation error

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func NewValidationError(field, message string) *ValidationError {
	return &ValidationError{Field: field, Message: message}
}

// Ship Assignment errors

type ShipAssignmentError struct {
	*DomainError
	ShipSymbol  string
	ContainerID string
}

func NewShipAssignmentError(message, shipSymbol, containerID string) *ShipAssignmentError {
	return &ShipAssignmentError{
		DomainError: &DomainError{Message: message},
		ShipSymbol:  shipSymbol,
		ContainerID: containerID,
	}
}

type ShipAlreadyAssignedError struct {
	*ShipAssignmentError
}

func NewShipAlreadyAssignedError(shipSymbol, currentContainerID string) *ShipAlreadyAssignedError {
	return &ShipAlreadyAssignedError{
		ShipAssignmentError: NewShipAssignmentError(
			fmt.Sprintf("ship %s is already assigned to container %s", shipSymbol, currentContainerID),
			shipSymbol,
			currentContainerID,
		),
	}
}

type ShipLockedError struct {
	*ShipAssignmentError
}

func NewShipLockedError(shipSymbol, containerID string) *ShipLockedError {
	return &ShipLockedError{
		ShipAssignmentError: NewShipAssignmentError(
			fmt.Sprintf("ship %s is locked by container %s", shipSymbol, containerID),
			shipSymbol,
			containerID,
		),
	}
}

type ShipPlayerMismatchError struct {
	*ShipAssignmentError
	ExpectedPlayerID int
	ActualPlayerID   int
}

func NewShipPlayerMismatchError(shipSymbol string, expectedPlayerID, actualPlayerID int) *ShipPlayerMismatchError {
	return &ShipPlayerMismatchError{
		ShipAssignmentError: NewShipAssignmentError(
			fmt.Sprintf("ship %s player_id mismatch: expected %d, got %d", shipSymbol, expectedPlayerID, actualPlayerID),
			shipSymbol,
			"",
		),
		ExpectedPlayerID: expectedPlayerID,
		ActualPlayerID:   actualPlayerID,
	}
}
