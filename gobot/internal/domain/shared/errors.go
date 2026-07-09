package shared

import "fmt"

// DomainError is the base error type for all domain errors
type DomainError struct {
	Message string
}

func (e *DomainError) Error() string {
	return e.Message
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

// ShipReservedByCaptainError indicates an attempt to assign a ship to a
// container was rejected because the captain has reserved it for direct,
// manual use (sp-i1ku). Reason is the free-text reason given at reserve time
// (may be empty).
type ShipReservedByCaptainError struct {
	*ShipAssignmentError
	Reason string
}

func NewShipReservedByCaptainError(shipSymbol, reason string) *ShipReservedByCaptainError {
	msg := fmt.Sprintf("ship %s is reserved by the captain", shipSymbol)
	if reason != "" {
		msg = fmt.Sprintf("%s: %s", msg, reason)
	}
	return &ShipReservedByCaptainError{
		ShipAssignmentError: NewShipAssignmentError(msg, shipSymbol, ""),
		Reason:              reason,
	}
}

// ShipNotReservedError indicates `ship release` was called on a hull that is
// not currently reserved by the captain (sp-i1ku).
type ShipNotReservedError struct {
	*ShipAssignmentError
}

func NewShipNotReservedError(shipSymbol string) *ShipNotReservedError {
	return &ShipNotReservedError{
		ShipAssignmentError: NewShipAssignmentError(
			fmt.Sprintf("ship %s is not reserved by the captain", shipSymbol),
			shipSymbol,
			"",
		),
	}
}

// ShipDedicatedToOtherFleetError indicates a claim was rejected because the
// ship is dedicated to a different operation's exclusive fleet (sp-l7h2).
// Fleet is the ship's persisted DedicatedFleet tag; Operation is the fleet
// name the claiming coordinator identified itself as. This is the atomic
// layer-2 enforcement inside ClaimShip's row-locked transaction — the
// discovery-time exclusion in FindIdleLightHaulers is only a best-effort
// pre-filter and can race with a concurrent `fleet assign`.
type ShipDedicatedToOtherFleetError struct {
	*ShipAssignmentError
	Fleet     string
	Operation string
}

func NewShipDedicatedToOtherFleetError(shipSymbol, fleet, operation string) *ShipDedicatedToOtherFleetError {
	claimant := operation
	if claimant == "" {
		claimant = "an undeclared operation"
	}
	return &ShipDedicatedToOtherFleetError{
		ShipAssignmentError: NewShipAssignmentError(
			fmt.Sprintf("ship %s is dedicated to fleet %q and cannot be claimed by %s", shipSymbol, fleet, claimant),
			shipSymbol,
			"",
		),
		Fleet:     fleet,
		Operation: operation,
	}
}
