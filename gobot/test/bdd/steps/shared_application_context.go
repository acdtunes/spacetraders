package steps

import (
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sharedApplicationContext provides shared state for application-layer command tests
// This allows multiple step definition contexts to work with the same ships, waypoints, and player data
type sharedApplicationContext struct {
	mu          sync.RWMutex
	ships       map[string]*navigation.Ship
	waypoints   map[string]*shared.Waypoint
	playerID    int
	agentSymbol string
	token       string
}

var (
	// Global shared context for application-layer tests
	globalAppContext = &sharedApplicationContext{
		ships:     make(map[string]*navigation.Ship),
		waypoints: make(map[string]*shared.Waypoint),
	}
)

// reset clears all shared state (called in Before hooks)
func (ctx *sharedApplicationContext) reset() {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
}

// Player management

func (ctx *sharedApplicationContext) setPlayerInfo(agentSymbol, token string, playerID int) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	ctx.playerID = playerID
}

func (ctx *sharedApplicationContext) getPlayerInfo() (agentSymbol, token string, playerID int) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	return ctx.agentSymbol, ctx.token, ctx.playerID
}

// Ship management

func (ctx *sharedApplicationContext) addShip(symbol string, ship *navigation.Ship) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.ships[symbol] = ship
}

func (ctx *sharedApplicationContext) getShip(symbol string) (*navigation.Ship, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	ship, exists := ctx.ships[symbol]
	return ship, exists
}

func (ctx *sharedApplicationContext) getAllShips() map[string]*navigation.Ship {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	// Return a copy to prevent concurrent modification
	shipsCopy := make(map[string]*navigation.Ship, len(ctx.ships))
	for k, v := range ctx.ships {
		shipsCopy[k] = v
	}
	return shipsCopy
}

func (ctx *sharedApplicationContext) updateShip(symbol string, ship *navigation.Ship) error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if _, exists := ctx.ships[symbol]; !exists {
		return fmt.Errorf("ship %s not found", symbol)
	}
	ctx.ships[symbol] = ship
	return nil
}

// Waypoint management

func (ctx *sharedApplicationContext) addWaypoint(symbol string, waypoint *shared.Waypoint) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.waypoints[symbol] = waypoint
}

func (ctx *sharedApplicationContext) getWaypoint(symbol string) (*shared.Waypoint, bool) {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	waypoint, exists := ctx.waypoints[symbol]
	return waypoint, exists
}

// Shared step definitions that multiple contexts can use

func sharedPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	globalAppContext.setPlayerInfo(agentSymbol, token, 0)
	return nil
}

func sharedPlayerHasPlayerID(playerID int) error {
	agentSymbol, token, _ := globalAppContext.getPlayerInfo()
	globalAppContext.setPlayerInfo(agentSymbol, token, playerID)
	return nil
}

func sharedShipForPlayerAtWithStatus(shipSymbol string, playerID int, location, status string) error {
	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	var navStatus navigation.NavStatus
	switch status {
	case "DOCKED":
		navStatus = navigation.NavStatusDocked
	case "IN_ORBIT":
		navStatus = navigation.NavStatusInOrbit
	case "IN_TRANSIT":
		navStatus = navigation.NavStatusInTransit
	default:
		return fmt.Errorf("unknown nav status: %s", status)
	}

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	globalAppContext.addShip(shipSymbol, ship)
	globalAppContext.addWaypoint(location, waypoint)
	return nil
}

func sharedShipForPlayerInTransitTo(shipSymbol string, playerID int, destination string) error {
	waypoint, _ := shared.NewWaypoint("X1-START", 0, 0)
	destWaypoint, _ := shared.NewWaypoint(destination, 100, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	// Transition to in-transit
	if err := ship.StartTransit(destWaypoint); err != nil {
		return err
	}

	globalAppContext.addShip(shipSymbol, ship)
	globalAppContext.addWaypoint(destination, destWaypoint)
	return nil
}

func sharedWaypointExistsAtCoordinates(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	globalAppContext.addWaypoint(waypointSymbol, waypoint)
	return nil
}
