package helpers

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
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
	marketData map[string]*domainPorts.MarketData // waypoint -> market data

	// Ship storage for GetShip
	ships map[string]*navigation.Ship // shipSymbol -> ship

	// Player storage for authorization
	players       map[int]string         // playerID -> token (for authorization)
	playersByToken map[string]*player.Player // token -> player (for GetAgent)

	// Waypoint storage for navigation
	waypoints map[string]*shared.Waypoint // waypointSymbol -> waypoint

	// Shipyard storage
	shipyards map[string]*domainPorts.ShipyardData // waypointSymbol -> shipyard data

	// Call tracking
	getMarketCalls []string // Track which waypoints were queried

	// Error injection
	shouldError bool
	errorMsg    string

	// Custom function handlers
	purchaseCargoFunc   func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*PurchaseCargoResult, error)
	sellCargoFunc       func(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*SellCargoResult, error)
	acceptContractFunc  func(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error)
	deliverContractFunc func(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*domainPorts.ContractData, error)
	fulfillContractFunc func(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error)
	getShipyardFunc     func(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ShipyardData, error)
	purchaseShipFunc    func(ctx context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error)
}

// NewMockAPIClient creates a new mock API client
func NewMockAPIClient() *MockAPIClient {
	return &MockAPIClient{
		marketData:     make(map[string]*domainPorts.MarketData),
		ships:          make(map[string]*navigation.Ship),
		players:        make(map[int]string),
		playersByToken: make(map[string]*player.Player),
		waypoints:      make(map[string]*shared.Waypoint),
		shipyards:      make(map[string]*domainPorts.ShipyardData),
		getMarketCalls: []string{},
	}
}

// AddWaypoint adds a waypoint to the mock for navigation lookups
func (m *MockAPIClient) AddWaypoint(waypoint *shared.Waypoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waypoints[waypoint.Symbol] = waypoint
}

// SetMarketData configures the mock to return specific market data for a waypoint
func (m *MockAPIClient) SetMarketData(waypoint string, goods []domainPorts.TradeGoodData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketData[waypoint] = &domainPorts.MarketData{
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

// AddShip adds a ship to the mock's storage for GetShip to return
func (m *MockAPIClient) AddShip(ship *navigation.Ship) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ships[ship.ShipSymbol()] = ship
}

// GetShipFromMock retrieves a ship from the mock's internal storage
func (m *MockAPIClient) GetShipFromMock(shipSymbol string) (*navigation.Ship, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ship, exists := m.ships[shipSymbol]
	return ship, exists
}

// AddPlayer registers a player and their token for authorization validation
func (m *MockAPIClient) AddPlayer(p *player.Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players[p.ID.Value()] = p.Token
	m.playersByToken[p.Token] = p
}

// UpdatePlayer updates an existing player's data in the mock
func (m *MockAPIClient) UpdatePlayer(p *player.Player) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.playersByToken[p.Token] = p
}

// GetMarket implements the APIClient interface
func (m *MockAPIClient) GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error) {
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
	return &domainPorts.MarketData{
		Symbol:     waypointSymbol,
		TradeGoods: []domainPorts.TradeGoodData{},
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

// SetAcceptContractFunc sets a custom function for AcceptContract calls
func (m *MockAPIClient) SetAcceptContractFunc(f func(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acceptContractFunc = f
}

func (m *MockAPIClient) SetDeliverContractFunc(f func(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*domainPorts.ContractData, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliverContractFunc = f
}

// SetFulfillContractFunc sets a custom function for FulfillContract calls
func (m *MockAPIClient) SetFulfillContractFunc(f func(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fulfillContractFunc = f
}

// SetShipyardData configures the mock to return specific shipyard data for a waypoint
func (m *MockAPIClient) SetShipyardData(waypointSymbol string, data *domainPorts.ShipyardData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shipyards[waypointSymbol] = data
}

// SetGetShipyardFunc sets a custom function for GetShipyard calls
func (m *MockAPIClient) SetGetShipyardFunc(f func(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ShipyardData, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getShipyardFunc = f
}

// SetPurchaseShipFunc sets a custom function for PurchaseShip calls
func (m *MockAPIClient) SetPurchaseShipFunc(f func(ctx context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purchaseShipFunc = f
}

// ResetShipyardMocks clears shipyard state for test isolation
func (m *MockAPIClient) ResetShipyardMocks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shipyards = make(map[string]*domainPorts.ShipyardData)
	m.getShipyardFunc = nil
	m.purchaseShipFunc = nil
}

// Implement other APIClient interface methods as no-ops

func (m *MockAPIClient) GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ship, ok := m.ships[symbol]
	if !ok {
		return nil, fmt.Errorf("ship not found")
	}

	// Validate authorization: token must match ship's owner
	expectedToken, hasPlayer := m.players[ship.PlayerID().Value()]
	if hasPlayer && expectedToken != token {
		// Token doesn't match - unauthorized access
		return nil, fmt.Errorf("ship not found")
	}

	// Convert domain ship to ShipData DTO
	return m.shipToData(ship), nil
}

// shipToData converts a domain Ship to ShipData DTO
func (m *MockAPIClient) shipToData(ship *navigation.Ship) *navigation.ShipData {
	// Convert cargo inventory
	var inventoryData []shared.CargoItem
	for _, item := range ship.Cargo().Inventory {
		inventoryData = append(inventoryData, shared.CargoItem{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		})
	}

	cargoData := &navigation.CargoData{
		Capacity:  ship.CargoCapacity(),
		Units:     ship.Cargo().Units,
		Inventory: inventoryData,
	}

	// Convert domain ship to DTO format
	return &navigation.ShipData{
		Symbol:        ship.ShipSymbol(),
		Location:      ship.CurrentLocation().Symbol,
		NavStatus:     string(ship.NavStatus()),
		FuelCurrent:   ship.Fuel().Current,
		FuelCapacity:  ship.FuelCapacity(),
		CargoCapacity: ship.CargoCapacity(),
		CargoUnits:    ship.Cargo().Units,
		EngineSpeed:   ship.EngineSpeed(),
		FrameSymbol:   ship.FrameSymbol(),
		Role:          ship.Role(),
		Cargo:         cargoData,
	}
}

func (m *MockAPIClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.shouldError {
		return nil, fmt.Errorf("%s", m.errorMsg)
	}

	// Find player by token to get their ID
	player, exists := m.playersByToken[token]
	if !exists {
		// No player found for this token - return empty list
		return []*navigation.ShipData{}, nil
	}

	// Convert ships belonging to this player to ShipData DTOs
	var shipsData []*navigation.ShipData
	for _, ship := range m.ships {
		// Only include ships that belong to this player
		if ship.PlayerID().Value() == player.ID.Value() {
			shipsData = append(shipsData, m.shipToData(ship))
		}
	}

	return shipsData, nil
}

func (m *MockAPIClient) NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	shouldError := m.shouldError
	errorMsg := m.errorMsg
	ship := m.ships[symbol]

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	if ship == nil {
		return nil, fmt.Errorf("ship not found: %s", symbol)
	}

	// Look up destination waypoint from registered waypoints
	destWaypoint, exists := m.waypoints[destination]
	if !exists {
		// Fallback: create a basic waypoint if not registered
		var err error
		destWaypoint, err = shared.NewWaypoint(destination, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to create destination waypoint: %w", err)
		}
	}

	// Calculate fuel required based on distance (simplified: 2.5 fuel per distance unit in CRUISE mode)
	currentLoc := ship.CurrentLocation()
	distance := currentLoc.DistanceTo(destWaypoint)
	fuelConsumed := int(distance * 2.5) // Simplified fuel calculation (matches CRUISE mode)
	if fuelConsumed == 0 && distance > 0 {
		fuelConsumed = 1 // Minimum 1 fuel for any movement
	}

	// Check if ship has enough fuel BEFORE starting transit
	if ship.Fuel().Current < fuelConsumed {
		return nil, fmt.Errorf("insufficient fuel: need %d but only have %d", fuelConsumed, ship.Fuel().Current)
	}

	// Start transit to destination
	if err := ship.StartTransit(destWaypoint); err != nil {
		return nil, fmt.Errorf("failed to start transit: %w", err)
	}

	// Consume fuel
	if err := ship.ConsumeFuel(fuelConsumed); err != nil {
		return nil, fmt.Errorf("failed to consume fuel: %w", err)
	}

	// Immediately arrive (mock instant travel)
	if err := ship.Arrive(); err != nil {
		return nil, fmt.Errorf("failed to arrive: %w", err)
	}

	// Mock navigation result with instant arrival
	return &navigation.Result{
		Destination:    destination,
		ArrivalTime:    0, // Instant arrival in mock
		ArrivalTimeStr: "",
		FuelConsumed:   fuelConsumed,
	}, nil
}

func (m *MockAPIClient) OrbitShip(ctx context.Context, symbol, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	shouldError := m.shouldError
	errorMsg := m.errorMsg

	if shouldError {
		return fmt.Errorf("%s", errorMsg)
	}

	// Update ship status to IN_ORBIT in the mock
	ship := m.ships[symbol]
	if ship != nil {
		// Ensure ship is in orbit
		ship.EnsureInOrbit()
	}

	return nil
}

func (m *MockAPIClient) DockShip(ctx context.Context, symbol, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	shouldError := m.shouldError
	errorMsg := m.errorMsg

	if shouldError {
		return fmt.Errorf("%s", errorMsg)
	}

	// Update ship status to DOCKED in the mock
	ship := m.ships[symbol]
	if ship != nil {
		ship.EnsureDocked()
	}

	return nil
}

func (m *MockAPIClient) RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	shouldError := m.shouldError
	errorMsg := m.errorMsg
	ship := m.ships[symbol]

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	if ship == nil {
		return nil, fmt.Errorf("ship not found: %s", symbol)
	}

	// Calculate fuel to add
	var fuelToAdd int
	var err error

	if units == nil {
		// Full refuel
		fuelToAdd, err = ship.RefuelToFull()
		if err != nil {
			return nil, fmt.Errorf("failed to refuel ship: %w", err)
		}
	} else {
		// Partial refuel
		fuelBefore := ship.Fuel().Current
		if err := ship.Refuel(*units); err != nil {
			return nil, fmt.Errorf("failed to refuel ship: %w", err)
		}
		fuelToAdd = ship.Fuel().Current - fuelBefore
	}

	return &navigation.RefuelResult{
		FuelAdded:   fuelToAdd,
		CreditsCost: fuelToAdd * 100, // Mock cost: 100 credits per fuel unit
	}, nil
}

func (m *MockAPIClient) SetFlightMode(ctx context.Context, symbol, flightMode, token string) error {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return fmt.Errorf("%s", errorMsg)
	}

	return nil
}

func (m *MockAPIClient) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Find player by token
	p, exists := m.playersByToken[token]
	if !exists {
		return nil, fmt.Errorf("player not found for token")
	}

	// Return agent data (use defaults for fields not in Player entity)
	return &player.AgentData{
		AccountID:       fmt.Sprintf("account-%d", p.ID.Value()),
		Symbol:          p.AgentSymbol,
		Headquarters:    "X1-SYSTEM-HQ",
		Credits:         p.Credits,
		StartingFaction: p.StartingFaction,
	}, nil
}

func (m *MockAPIClient) ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.shouldError {
		return nil, fmt.Errorf("%s", m.errorMsg)
	}

	// Filter waypoints by system symbol and convert to API data format
	var waypointData []system.WaypointAPIData
	for _, wp := range m.waypoints {
		wpSystemSymbol := shared.ExtractSystemSymbol(wp.Symbol)
		if wpSystemSymbol == systemSymbol {
			waypointData = append(waypointData, system.WaypointAPIData{
				Symbol:   wp.Symbol,
				Type:     "ASTEROID", // Default type for tests
				X:        wp.X,
				Y:        wp.Y,
				Traits:   []map[string]interface{}{}, // Empty traits for tests
				Orbitals: []map[string]string{},      // Empty orbitals for tests
			})
		}
	}

	// Simple pagination - just return all waypoints for now (tests don't need multi-page)
	return &system.WaypointsListResponse{
		Data: waypointData,
		Meta: system.PaginationMeta{
			Total: len(waypointData),
			Page:  page,
			Limit: limit,
		},
	}, nil
}

func (m *MockAPIClient) NegotiateContract(ctx context.Context, shipSymbol, token string) (*domainPorts.ContractNegotiationResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) GetContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) AcceptContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	m.mu.RLock()
	fn := m.acceptContractFunc
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	if fn != nil {
		return fn(ctx, contractID, token)
	}

	// Default successful response (basic mock)
	return &domainPorts.ContractData{
		ID:       contractID,
		Accepted: true,
	}, nil
}

func (m *MockAPIClient) DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*domainPorts.ContractData, error) {
	m.mu.RLock()
	fn := m.deliverContractFunc
	m.mu.RUnlock()

	if fn != nil {
		return fn(ctx, contractID, shipSymbol, tradeSymbol, units, token)
	}

	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) FulfillContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	m.mu.RLock()
	fn := m.fulfillContractFunc
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	if fn != nil {
		return fn(ctx, contractID, token)
	}

	// Default successful response (basic mock)
	return &domainPorts.ContractData{
		ID:        contractID,
		Fulfilled: true,
	}, nil
}

func (m *MockAPIClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.PurchaseResult, error) {
	m.mu.RLock()
	fn := m.purchaseCargoFunc
	m.mu.RUnlock()

	if fn != nil {
		result, err := fn(ctx, shipSymbol, goodSymbol, units, token)
		if err != nil {
			return nil, err
		}
		return &domainPorts.PurchaseResult{
			TotalCost:  result.TotalCost,
			UnitsAdded: result.UnitsAdded,
		}, nil
	}

	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.SellResult, error) {
	m.mu.RLock()
	fn := m.sellCargoFunc
	m.mu.RUnlock()

	if fn != nil {
		result, err := fn(ctx, shipSymbol, goodSymbol, units, token)
		if err != nil {
			return nil, err
		}
		return &domainPorts.SellResult{
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

func (m *MockAPIClient) GetShipyard(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ShipyardData, error) {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	fn := m.getShipyardFunc
	shipyard := m.shipyards[waypointSymbol]
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Use custom function if provided
	if fn != nil {
		return fn(ctx, systemSymbol, waypointSymbol, token)
	}

	// Return stored shipyard data if available
	if shipyard != nil {
		return shipyard, nil
	}

	// Return empty shipyard data
	return &domainPorts.ShipyardData{
		Symbol:    waypointSymbol,
		ShipTypes: []domainPorts.ShipTypeInfo{},
		Ships:     []domainPorts.ShipListingData{},
	}, nil
}

func (m *MockAPIClient) PurchaseShip(ctx context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error) {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	fn := m.purchaseShipFunc
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Use custom function if provided
	if fn != nil {
		return fn(ctx, shipType, waypointSymbol, token)
	}

	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockAPIClient) ExtractResources(ctx context.Context, shipSymbol string, token string) (*domainPorts.ExtractionResult, error) {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Return a mock extraction result
	return &domainPorts.ExtractionResult{
		ShipSymbol:      shipSymbol,
		YieldSymbol:     "IRON_ORE",
		YieldUnits:      10,
		CooldownSeconds: 60,
		CooldownExpires: "",
		Cargo:           nil,
	}, nil
}

func (m *MockAPIClient) TransferCargo(ctx context.Context, fromShipSymbol, toShipSymbol, goodSymbol string, units int, token string) (*domainPorts.TransferResult, error) {
	m.mu.RLock()
	shouldError := m.shouldError
	errorMsg := m.errorMsg
	m.mu.RUnlock()

	if shouldError {
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Return a mock transfer result
	return &domainPorts.TransferResult{
		FromShip:         fromShipSymbol,
		ToShip:           toShipSymbol,
		GoodSymbol:       goodSymbol,
		UnitsTransferred: units,
		RemainingCargo:   nil,
	}, nil
}
