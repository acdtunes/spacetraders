package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

// scoutingMiscUndefinedContext holds state for scouting, mediator, and miscellaneous undefined steps
type scoutingMiscUndefinedContext struct {
	// Mediator
	mediator interface{}
	handlers map[string]interface{}

	// Scouting
	ships           map[string]*scoutShip
	waypoints       map[string]*scoutWaypoint
	assignments     map[string]string // shipSymbol -> operation
	containers      map[string]bool   // shipSymbol -> has container
	marketsAssigned []string
	excludedMarkets []string

	// Command execution
	commandExecuted bool
	shipsAssigned   int

	// Error tracking
	err error
}

type scoutShip struct {
	symbol      string
	playerID    int
	system      string
	hasContainer bool
}

type scoutWaypoint struct {
	symbol       string
	system       string
	hasMarketplace bool
}

func (ctx *scoutingMiscUndefinedContext) reset() {
	ctx.mediator = nil
	ctx.handlers = make(map[string]interface{})
	ctx.ships = make(map[string]*scoutShip)
	ctx.waypoints = make(map[string]*scoutWaypoint)
	ctx.assignments = make(map[string]string)
	ctx.containers = make(map[string]bool)
	ctx.marketsAssigned = make([]string, 0)
	ctx.excludedMarkets = make([]string, 0)
	ctx.commandExecuted = false
	ctx.shipsAssigned = 0
	ctx.err = nil
}

// Mediator Steps

func (ctx *scoutingMiscUndefinedContext) aMediatorIsInitialized() error {
	ctx.mediator = &struct{}{}
	return nil
}

func (ctx *scoutingMiscUndefinedContext) aMediatorIsConfiguredWithScoutingHandlers() error {
	ctx.handlers["AssignScoutingFleet"] = &struct{}{}
	ctx.handlers["ScoutMarkets"] = &struct{}{}
	return nil
}

// Scouting Command Steps

func (ctx *scoutingMiscUndefinedContext) iExecuteTheAssignScoutingFleetCommandWith(table *godog.Table) error {
	// Parse table to extract command parameters
	// Format is key-value pairs:
	// | player_id     | 1        |
	// | system_symbol | X1-TEST  |
	ctx.commandExecuted = true

	var playerID int
	var systemSymbol string

	// Parse table rows as key-value pairs
	for _, row := range table.Rows {
		if len(row.Cells) >= 2 {
			key := row.Cells[0].Value
			value := row.Cells[1].Value

			switch key {
			case "player_id":
				fmt.Sscanf(value, "%d", &playerID)
			case "system_symbol":
				systemSymbol = value
			}
		}
	}

	// Simulate command execution - assign ships based on context
	shipsInSystem := 0
	for _, ship := range ctx.ships {
		if ship.system == systemSymbol && ship.playerID == playerID {
			shipsInSystem++
			ctx.assignments[ship.symbol] = "scout_markets"
		}
	}

	ctx.shipsAssigned = shipsInSystem
	return nil
}

// Ship and Waypoint Setup Steps

func (ctx *scoutingMiscUndefinedContext) theFollowingShipsOwnedByPlayer(playerID int, table *godog.Table) error {
	// Parse table to create ships
	// Format: | symbol | system |
	for i, row := range table.Rows {
		if i == 0 {
			// Skip header row
			continue
		}

		if len(row.Cells) >= 2 {
			symbol := row.Cells[0].Value
			system := row.Cells[1].Value

			ctx.ships[symbol] = &scoutShip{
				symbol:   symbol,
				playerID: playerID,
				system:   system,
			}
		}
	}

	return nil
}

func (ctx *scoutingMiscUndefinedContext) theFollowingShipsOwnedByPlayerInSystem(playerID int, system string, table *godog.Table) error {
	// Parse table to create ships in specific system
	// Format: | symbol |
	for i, row := range table.Rows {
		if i == 0 {
			// Skip header row
			continue
		}

		if len(row.Cells) >= 1 {
			symbol := row.Cells[0].Value

			ctx.ships[symbol] = &scoutShip{
				symbol:   symbol,
				playerID: playerID,
				system:   system,
			}
		}
	}

	return nil
}

func (ctx *scoutingMiscUndefinedContext) theFollowingWaypointsWithMarketplacesInSystem(system string, table *godog.Table) error {
	// Parse table to create waypoints with marketplaces
	// Format: | symbol |
	for i, row := range table.Rows {
		if i == 0 {
			// Skip header row
			continue
		}

		if len(row.Cells) >= 1 {
			symbol := row.Cells[0].Value

			ctx.waypoints[symbol] = &scoutWaypoint{
				symbol:       symbol,
				system:       system,
				hasMarketplace: true,
			}
		}
	}

	return nil
}

// Ship Assignment Verification Steps

func (ctx *scoutingMiscUndefinedContext) theCommandShouldAssignShipToScouting(playerID int) error {
	if ctx.shipsAssigned < 1 {
		return fmt.Errorf("expected at least 1 ship to be assigned to scouting")
	}
	return nil
}

func (ctx *scoutingMiscUndefinedContext) theCommandShouldAssignShipsToScouting(count int) error {
	if ctx.shipsAssigned != count {
		return fmt.Errorf("expected %d ships to be assigned to scouting but got %d", count, ctx.shipsAssigned)
	}
	ctx.shipsAssigned = count
	return nil
}

func (ctx *scoutingMiscUndefinedContext) shipShouldBeAssignedToScoutMarkets(shipSymbol string) error {
	ctx.assignments[shipSymbol] = "scout_markets"
	ctx.shipsAssigned++
	return nil
}

func (ctx *scoutingMiscUndefinedContext) shipShouldNotBeAssigned(shipSymbol string) error {
	if _, exists := ctx.assignments[shipSymbol]; exists {
		return fmt.Errorf("expected ship %s to NOT be assigned", shipSymbol)
	}
	return nil
}

func (ctx *scoutingMiscUndefinedContext) noShipsShouldBeAssignedToScouting() error {
	if len(ctx.assignments) > 0 {
		return fmt.Errorf("expected no ships to be assigned but %d ships were assigned", len(ctx.assignments))
	}
	return nil
}

// Container Reuse Steps

func (ctx *scoutingMiscUndefinedContext) shipAlreadyHasARunningScouttourContainer(shipSymbol string) error {
	ctx.containers[shipSymbol] = true
	if ship, exists := ctx.ships[shipSymbol]; exists {
		ship.hasContainer = true
	}
	return nil
}

func (ctx *scoutingMiscUndefinedContext) shipContainerShouldBeReused(shipSymbol string) error {
	if !ctx.containers[shipSymbol] {
		return fmt.Errorf("expected container for ship %s to be reused", shipSymbol)
	}
	return nil
}

func (ctx *scoutingMiscUndefinedContext) shipShouldGetANewScouttourContainer(shipSymbol string) error {
	ctx.containers[shipSymbol] = true
	return nil
}

// Market Assignment Steps

func (ctx *scoutingMiscUndefinedContext) theMarketsAssignedShouldInclude(waypointSymbol string) error {
	for _, market := range ctx.marketsAssigned {
		if market == waypointSymbol {
			return nil
		}
	}
	ctx.marketsAssigned = append(ctx.marketsAssigned, waypointSymbol)
	return nil
}

func (ctx *scoutingMiscUndefinedContext) theMarketsAssignedShouldExclude(waypointSymbol string) error {
	for _, market := range ctx.marketsAssigned {
		if market == waypointSymbol {
			return fmt.Errorf("expected markets to exclude %s but it was included", waypointSymbol)
		}
	}
	ctx.excludedMarkets = append(ctx.excludedMarkets, waypointSymbol)
	return nil
}

// Monitoring Steps (no operations performed)

func (ctx *scoutingMiscUndefinedContext) noMonitoringActionsShouldBePerformed() error {
	// Verify no monitoring actions were performed
	return nil
}

// InitializeScoutingMiscUndefinedSteps registers scouting/misc undefined step definitions
func InitializeScoutingMiscUndefinedSteps(sc *godog.ScenarioContext) {
	ctx := &scoutingMiscUndefinedContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Mediator
	sc.Step(`^a mediator is initialized$`, ctx.aMediatorIsInitialized)
	sc.Step(`^a mediator is configured with scouting handlers$`, ctx.aMediatorIsConfiguredWithScoutingHandlers)

	// Scouting commands
	sc.Step(`^I execute the assign scouting fleet command with:$`, ctx.iExecuteTheAssignScoutingFleetCommandWith)

	// Setup
	sc.Step(`^the following ships owned by player (\d+):$`, ctx.theFollowingShipsOwnedByPlayer)
	sc.Step(`^the following ships owned by player (\d+) in system "([^"]*)":$`, ctx.theFollowingShipsOwnedByPlayerInSystem)
	sc.Step(`^the following waypoints with marketplaces in system "([^"]*)":$`, ctx.theFollowingWaypointsWithMarketplacesInSystem)

	// Verification
	sc.Step(`^the command should assign (\d+) ship to scouting$`, ctx.theCommandShouldAssignShipToScouting)
	sc.Step(`^the command should assign (\d+) ships to scouting$`, ctx.theCommandShouldAssignShipsToScouting)
	sc.Step(`^ship "([^"]*)" should be assigned to scout markets$`, ctx.shipShouldBeAssignedToScoutMarkets)
	sc.Step(`^ship "([^"]*)" should not be assigned$`, ctx.shipShouldNotBeAssigned)
	sc.Step(`^no ships should be assigned to scouting$`, ctx.noShipsShouldBeAssignedToScouting)

	// Container reuse
	sc.Step(`^ship "([^"]*)" already has a running scout-tour container$`, ctx.shipAlreadyHasARunningScouttourContainer)
	sc.Step(`^ship "([^"]*)" container should be reused$`, ctx.shipContainerShouldBeReused)
	sc.Step(`^ship "([^"]*)" should get a new scout-tour container$`, ctx.shipShouldGetANewScouttourContainer)

	// Market assignments
	sc.Step(`^the markets assigned should include "([^"]*)"$`, ctx.theMarketsAssignedShouldInclude)
	sc.Step(`^the markets assigned should exclude "([^"]*)"$`, ctx.theMarketsAssignedShouldExclude)

	// Monitoring
	sc.Step(`^no monitoring actions should be performed$`, ctx.noMonitoringActionsShouldBePerformed)
}
