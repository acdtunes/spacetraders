package api

import (
	"errors"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	// CircuitClosed allows all requests
	CircuitClosed CircuitState = iota
	// CircuitOpen blocks all requests
	CircuitOpen
	// CircuitHalfOpen allows limited requests to test recovery
	CircuitHalfOpen
)

var (
	// ErrCircuitOpen is returned when the circuit breaker is open
	ErrCircuitOpen = errors.New("circuit breaker open")
)

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	maxFailures     int
	timeout         time.Duration
	state           CircuitState
	failureCount    int
	lastFailureTime time.Time
	mu              sync.RWMutex
	clock           shared.Clock
}

// NewCircuitBreaker creates a new circuit breaker with optional clock injection
// If clock is nil, uses RealClock
func NewCircuitBreaker(maxFailures int, timeout time.Duration, clock shared.Clock) *CircuitBreaker {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &CircuitBreaker{
		maxFailures: maxFailures,
		timeout:     timeout,
		state:       CircuitClosed,
		clock:       clock,
	}
}

// Call executes a function with circuit breaker protection
func (cb *CircuitBreaker) Call(fn func() error) error {
	// Check circuit state and transition if needed
	cb.mu.Lock()
	if cb.state == CircuitOpen {
		elapsed := cb.clock.Now().Sub(cb.lastFailureTime)
		if elapsed >= cb.timeout {
			cb.state = CircuitHalfOpen
		} else {
			cb.mu.Unlock()
			return ErrCircuitOpen
		}
	}
	cb.mu.Unlock()

	// Execute the function WITHOUT holding the lock
	// This allows long-running operations (retries, sleeps) without blocking other requests
	err := fn()

	// Update circuit breaker state based on result
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if err != nil {
		cb.onFailure()
		return err
	}

	cb.onSuccess()
	return nil
}

// onFailure records a failure and updates circuit state
func (cb *CircuitBreaker) onFailure() {
	cb.failureCount++
	cb.lastFailureTime = cb.clock.Now()

	if cb.state == CircuitHalfOpen {
		// Failed in half-open state, reopen circuit
		cb.state = CircuitOpen
		return
	}

	if cb.failureCount >= cb.maxFailures {
		cb.state = CircuitOpen
	}
}

// onSuccess records a success and updates circuit state
func (cb *CircuitBreaker) onSuccess() {
	cb.failureCount = 0

	if cb.state == CircuitHalfOpen {
		// Success in half-open state, close circuit
		cb.state = CircuitClosed
	}
}

// GetState returns the current circuit breaker state
func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetFailureCount returns the current consecutive failure count
func (cb *CircuitBreaker) GetFailureCount() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failureCount
}

// SetState allows setting the circuit breaker state (for testing)
func (cb *CircuitBreaker) SetState(state CircuitState, failures int, lastFailure time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = state
	cb.failureCount = failures
	cb.lastFailureTime = lastFailure
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CircuitClosed
	cb.failureCount = 0
}
