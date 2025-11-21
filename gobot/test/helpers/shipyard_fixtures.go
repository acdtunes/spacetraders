package helpers

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// CreateTestShipyardData builds a ShipyardData with configurable listings
func CreateTestShipyardData(waypointSymbol string, listings ...ports.ShipListingData) *ports.ShipyardData {
	shipTypes := make([]ports.ShipTypeInfo, len(listings))
	for i, listing := range listings {
		shipTypes[i] = ports.ShipTypeInfo{Type: listing.Type}
	}

	return &ports.ShipyardData{
		Symbol:          waypointSymbol,
		ShipTypes:       shipTypes,
		Ships:           listings,
		Transactions:    []map[string]interface{}{},
		ModificationFee: 0,
	}
}

// CreateTestShipListing builds a ShipListingData with sensible defaults
func CreateTestShipListing(shipType string, price int) ports.ShipListingData {
	return ports.ShipListingData{
		Type:          shipType,
		Name:          fmt.Sprintf("%s Ship", shipType),
		Description:   fmt.Sprintf("A %s class vessel", shipType),
		PurchasePrice: price,
		Frame:         map[string]interface{}{"symbol": "FRAME_" + shipType},
		Reactor:       map[string]interface{}{"symbol": "REACTOR_" + shipType},
		Engine:        map[string]interface{}{"symbol": "ENGINE_" + shipType},
		Modules:       []map[string]interface{}{},
		Mounts:        []map[string]interface{}{},
	}
}

// CreateTestShipPurchaseResult builds a ShipPurchaseResult
func CreateTestShipPurchaseResult(agentSymbol, shipSymbol, shipType, waypointSymbol string, price, newCredits int) *ports.ShipPurchaseResult {
	return &ports.ShipPurchaseResult{
		Agent: &player.AgentData{
			AccountID: agentSymbol,
			Symbol:    agentSymbol,
			Credits:   newCredits,
		},
		Ship: &navigation.ShipData{
			Symbol:        shipSymbol,
			Location:      waypointSymbol,
			NavStatus:     "DOCKED",
			FuelCurrent:   100,
			FuelCapacity:  100,
			CargoCapacity: 40,
			CargoUnits:    0,
			EngineSpeed:   30,
			FrameSymbol:   "FRAME_" + shipType,
			Role:          "COMMAND",
			Cargo: &navigation.CargoData{
				Capacity:  40,
				Units:     0,
				Inventory: []shared.CargoItem{},
			},
		},
		Transaction: &ports.ShipPurchaseTransaction{
			WaypointSymbol: waypointSymbol,
			ShipSymbol:     shipSymbol,
			ShipType:       shipType,
			Price:          price,
			AgentSymbol:    agentSymbol,
			Timestamp:      time.Now().Format(time.RFC3339),
		},
	}
}

// CreateTestWaypointWithShipyard creates a waypoint with SHIPYARD trait
func CreateTestWaypointWithShipyard(symbol string, x, y int) (*shared.Waypoint, error) {
	waypoint, err := shared.NewWaypoint(symbol, float64(x), float64(y))
	if err != nil {
		return nil, err
	}
	waypoint.Traits = []string{"SHIPYARD"}
	return waypoint, nil
}

// CreateTestWaypoint creates a waypoint without SHIPYARD trait
func CreateTestWaypoint(symbol string, x, y int) (*shared.Waypoint, error) {
	return shared.NewWaypoint(symbol, float64(x), float64(y))
}
