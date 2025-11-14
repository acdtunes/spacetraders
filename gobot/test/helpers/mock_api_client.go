package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	infraports "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// PurchaseCargoResult represents the result of a purchase operation
type PurchaseCargoResult struct {
	TotalCost  int
	UnitsAdded int
}

// SellCargoResult represents the result of a sell operation
type SellCargoResult struct {
	TotalRevenue int
	UnitsSold    int
}

// MockAPIClient is a test double for APIClient interface
type MockAPIClient struct {
	mu sync.RWMutex

	// Market data responses
	marketData map[string]*infraports.MarketData // waypoint -> market data

	// Call tracking
	getMarketCalls []string // Track which waypoints were queried

	// Error injection
	shouldError bool
	errorMsg    string

	// Custom function handlers
	purchaseCargoFunc func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*PurchaseCargoResult, error)
	sellCargoFunc     func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*SellCargoResult, error)
}

// NewMockAPIClient creates a new mock API client
func NewMockAPIClient() *MockAPIClient {
	return &MockAPIClient{
		marketData:     make(map[string]*infraports.MarketData),
		getMarketCalls: []string{},
	}
}

// SetMarketData configures the mock to return specific market data for a waypoint
func (m *MockAPIClient) SetMarketData(waypoint string, goods []infraports.TradeGoodData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketData[waypoint] = &infraports.MarketData{
		Symbol:     waypoint,
		TradeGoods: goods,
	}
}

// SetError configures the mock to return an error
func (m *MockAPIClient) SetError(shouldError bool, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldError = shouldError
	m.errorMsg = msg
}

// GetMarket implements the APIClient interface
func (m *MockAPIClient) GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*infraports.MarketData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.getMarketCalls = append(m.getMarketCalls, waypointSymbol)

	if m.shouldError {
		return nil, fmt.Errorf("%s", m.errorMsg)
	}

	if data, ok := m.marketData[waypointSymbol]; ok {
		return data, nil
	}

	// Return empty market data if not configured
	return &infraports.MarketData{
		Symbol:     waypointSymbol,
		TradeGoods: []infraports.TradeGoodData{},
	}, nil
}

// GetMarketCalls returns the list of waypoints that were queried
func (m *MockAPIClient) GetMarketCalls() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string{}, m.getMarketCalls...)
}

// ResetCalls clears the call tracking
func (m *MockAPIClient) ResetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getMarketCalls = []string{}
}

// ClearCalls is an alias for ResetCalls
func (m *MockAPIClient) ClearCalls() {
	m.ResetCalls()
}

// SetPurchaseCargoFunc sets a custom function for PurchaseCargo calls
func (m *MockAPIClient) SetPurchaseCargoFunc(f func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*PurchaseCargoResult, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purchaseCargoFunc = f
}

// SetSellCargoFunc sets a custom function for SellCargo calls
func (m *MockAPIClient) SetSellCargoFunc(f func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*SellCargoResult, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sellCargoFunc = f
}

// Implement other APIClient interface methods as no-ops

func (m *MockAPIClient) GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.NavigationResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) OrbitShip(ctx context.Context, symbol, token string) error {
	return fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) DockShip(ctx context.Context, symbol, token string) error {
	return fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) SetFlightMode(ctx context.Context, symbol, flightMode, token string) error {
	return fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) NegotiateContract(ctx context.Context, shipSymbol, token string) (*infraports.ContractNegotiationResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) GetContract(ctx context.Context, contractID, token string) (*infraports.ContractData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) AcceptContract(ctx context.Context, contractID, token string) (*infraports.ContractData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*infraports.ContractData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) FulfillContract(ctx context.Context, contractID, token string) (*infraports.ContractData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*infraports.PurchaseResult, error) {
	m.mu.RLock()
	fn := m.purchaseCargoFunc
	m.mu.RUnlock()

	if fn != nil {
		result, err := fn(ctx, shipSymbol, goodSymbol, units, token)
		if err != nil {
			return nil, err
		}
		return &infraports.PurchaseResult{
			TotalCost:  result.TotalCost,
			UnitsAdded: result.UnitsAdded,
		}, nil
	}

	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*infraports.SellResult, error) {
	m.mu.RLock()
	fn := m.sellCargoFunc
	m.mu.RUnlock()

	if fn != nil {
		result, err := fn(ctx, shipSymbol, goodSymbol, units, token)
		if err != nil {
			return nil, err
		}
		return &infraports.SellResult{
			TotalRevenue: result.TotalRevenue,
			UnitsSold:    result.UnitsSold,
		}, nil
	}

	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) JettisonCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) error {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return fmt.Errorf("%s", errorMsg)
	}

	// Mock success - in real implementation this would call the API
	return nil
}
