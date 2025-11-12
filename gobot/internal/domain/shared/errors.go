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
