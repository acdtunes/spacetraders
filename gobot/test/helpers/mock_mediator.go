package helpers

import (
	"context"
	"fmt"
	"reflect"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCommands "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipyardCommands "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
)

// MockMediator is a test double for the Mediator interface
// Required because BatchPurchaseShipsCommand internally uses:
// - PurchaseShipCommand - to purchase individual ships
// And PurchaseShipCommand internally uses:
// - NavigateRouteCommand - to move purchasing ship to shipyard
// - DockShipCommand - to dock ship before purchase
// - GetShipyardListingsQuery - to query shipyard listings
type MockMediator struct {
	sendFunc                func(ctx context.Context, request common.Request) (common.Response, error)
	callLog                 []string                                         // Track which commands were called
	shipyardListingsHandler *shipyardQueries.GetShipyardListingsHandler     // Handler for shipyard listings queries
	purchaseShipHandler     *shipyardCommands.PurchaseShipHandler           // Handler for purchase ship commands
}

// NewMockMediator creates a new MockMediator
func NewMockMediator() *MockMediator {
	return &MockMediator{
		callLog: []string{},
	}
}

// Send implements the Mediator interface
func (m *MockMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	// Use custom function if provided
	if m.sendFunc != nil {
		return m.sendFunc(ctx, request)
	}

	// Default behaviors based on request type
	switch req := request.(type) {
	case *shipCommands.NavigateRouteCommand:
		m.callLog = append(m.callLog, fmt.Sprintf("NavigateRoute:%s->%s", req.ShipSymbol, req.Destination))
		return &shipCommands.NavigateRouteResponse{
			Status:          "completed",
			CurrentLocation: req.Destination,
			FuelRemaining:   100,
		}, nil

	case *shipCommands.DockShipCommand:
		m.callLog = append(m.callLog, fmt.Sprintf("DockShip:%s", req.ShipSymbol))
		return &shipCommands.DockShipResponse{
			Status: "docked",
		}, nil

	case *shipyardQueries.GetShipyardListingsQuery:
		// If handler is registered, use it
		if m.shipyardListingsHandler != nil {
			m.callLog = append(m.callLog, fmt.Sprintf("GetShipyardListings:%s", req.WaypointSymbol))
			return m.shipyardListingsHandler.Handle(ctx, req)
		}
		return nil, fmt.Errorf("GetShipyardListingsHandler not registered in mock mediator")

	case *shipyardCommands.PurchaseShipCommand:
		// If handler is registered, use it
		if m.purchaseShipHandler != nil {
			m.callLog = append(m.callLog, fmt.Sprintf("PurchaseShip:%s@%s", req.ShipType, req.ShipyardWaypoint))
			return m.purchaseShipHandler.Handle(ctx, req)
		}
		return nil, fmt.Errorf("PurchaseShipHandler not registered in mock mediator")

	default:
		return nil, fmt.Errorf("unsupported request type: %T", request)
	}
}

// SetSendFunc sets a custom function for Send calls
func (m *MockMediator) SetSendFunc(fn func(ctx context.Context, request common.Request) (common.Response, error)) {
	m.sendFunc = fn
}

// SetShipyardListingsHandler registers a handler for GetShipyardListingsQuery
func (m *MockMediator) SetShipyardListingsHandler(handler *shipyardQueries.GetShipyardListingsHandler) {
	m.shipyardListingsHandler = handler
}

// SetPurchaseShipHandler registers a handler for PurchaseShipCommand
func (m *MockMediator) SetPurchaseShipHandler(handler *shipyardCommands.PurchaseShipHandler) {
	m.purchaseShipHandler = handler
}

// GetCallLog returns the list of commands that were called
func (m *MockMediator) GetCallLog() []string {
	return append([]string{}, m.callLog...)
}

// ClearCallLog clears the call log
func (m *MockMediator) ClearCallLog() {
	m.callLog = []string{}
}

// HasNavigateCall checks if a navigate command was called with specific route
func (m *MockMediator) HasNavigateCall(from, to string) bool {
	// Check for the destination in the call log
	for _, call := range m.callLog {
		if call == fmt.Sprintf("NavigateRoute:%s->%s", from, to) {
			return true
		}
	}
	return false
}

// HasDockCall checks if a dock command was called for a specific ship
func (m *MockMediator) HasDockCall(shipSymbol string) bool {
	for _, call := range m.callLog {
		if call == fmt.Sprintf("DockShip:%s", shipSymbol) {
			return true
		}
	}
	return false
}

// Register implements the Mediator interface (no-op for tests)
func (m *MockMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil // No-op for tests
}

// RegisterMiddleware implements the Mediator interface (no-op for tests)
func (m *MockMediator) RegisterMiddleware(middleware common.Middleware) {
	// No-op for tests
}

// Ensure MockMediator implements the common.Mediator interface
var _ common.Mediator = (*MockMediator)(nil)
