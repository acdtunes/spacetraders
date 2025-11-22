package helpers

import (
	"context"
	"fmt"
	"sync"
)

// MockMarketScanner is a test double for the MarketScanner service
type MockMarketScanner struct {
	mu              sync.RWMutex
	scanAndSaveFunc func(ctx context.Context, playerID uint, waypointSymbol string) error
	scanCalls       []ScanCall // Track all scan calls for verification
}

// ScanCall represents a single call to ScanAndSaveMarket
type ScanCall struct {
	PlayerID       uint
	WaypointSymbol string
}

// NewMockMarketScanner creates a new mock market scanner
func NewMockMarketScanner() *MockMarketScanner {
	return &MockMarketScanner{
		scanCalls: make([]ScanCall, 0),
	}
}

// ScanAndSaveMarket executes the configured mock function
func (m *MockMarketScanner) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	m.mu.Lock()
	m.scanCalls = append(m.scanCalls, ScanCall{
		PlayerID:       playerID,
		WaypointSymbol: waypointSymbol,
	})
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.scanAndSaveFunc != nil {
		return m.scanAndSaveFunc(ctx, playerID, waypointSymbol)
	}

	// Default: success
	return nil
}

// SetScanAndSaveFunc sets the function to call when ScanAndSaveMarket is invoked
func (m *MockMarketScanner) SetScanAndSaveFunc(f func(ctx context.Context, playerID uint, waypointSymbol string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanAndSaveFunc = f
}

// GetScanCalls returns all recorded scan calls
func (m *MockMarketScanner) GetScanCalls() []ScanCall {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scanCalls
}

// GetScanCount returns the number of times ScanAndSaveMarket was called
func (m *MockMarketScanner) GetScanCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.scanCalls)
}

// GetScansForWaypoint returns all scan calls for a specific waypoint
func (m *MockMarketScanner) GetScansForWaypoint(waypointSymbol string) []ScanCall {
	m.mu.RLock()
	defer m.mu.RUnlock()

	matches := make([]ScanCall, 0)
	for _, call := range m.scanCalls {
		if call.WaypointSymbol == waypointSymbol {
			matches = append(matches, call)
		}
	}
	return matches
}

// Reset clears all recorded scan calls
func (m *MockMarketScanner) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanCalls = make([]ScanCall, 0)
}

// VerifyScanCount checks that ScanAndSaveMarket was called exactly n times
func (m *MockMarketScanner) VerifyScanCount(expected int) error {
	actual := m.GetScanCount()
	if actual != expected {
		return fmt.Errorf("expected %d scan calls, got %d", expected, actual)
	}
	return nil
}

// VerifyWaypointScanned checks that a specific waypoint was scanned
func (m *MockMarketScanner) VerifyWaypointScanned(waypointSymbol string) error {
	scans := m.GetScansForWaypoint(waypointSymbol)
	if len(scans) == 0 {
		return fmt.Errorf("waypoint %s was not scanned", waypointSymbol)
	}
	return nil
}
