package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraports "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type deliverContractContext struct {
	contracts     map[string]*contract.Contract
	ships         map[string]*navigation.Ship
	players       map[int]*player.Player
	playerID      int
	response      *appContract.DeliverContractResponse
	err           error
	apiDeliveries map[string]int // Track expected API delivery responses

	// Test doubles
	contractRepo     *helpers.MockContractRepository
	mockPlayerRepo   *helpers.MockPlayerRepository
	mockWaypointRepo *helpers.MockWaypointRepository
	shipRepo         navigation.ShipRepository
	apiClient        *helpers.MockAPIClient
	handler          *appContract.DeliverContractHandler
}

func (ctx *deliverContractContext) reset() {
	ctx.contracts = make(map[string]*contract.Contract)
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.players = make(map[int]*player.Player)
	ctx.playerID = 0
	ctx.response = nil
	ctx.err = nil
	ctx.apiDeliveries = make(map[string]int)

	// Initialize test doubles
	ctx.contractRepo = helpers.NewMockContractRepository()
	ctx.mockPlayerRepo = helpers.NewMockPlayerRepository()
	ctx.mockWaypointRepo = helpers.NewMockWaypointRepository()
	ctx.apiClient = helpers.NewMockAPIClient()
	ctx.shipRepo = api.NewAPIShipRepository(ctx.apiClient, ctx.mockPlayerRepo, ctx.mockWaypointRepo)

	ctx.handler = appContract.NewDeliverContractHandler(ctx.contractRepo, ctx.apiClient, ctx.mockPlayerRepo)
}

// Given steps

func (ctx *deliverContractContext) aPlayerWithIDAndToken(playerID int, token string) error {
	ctx.playerID = playerID
	p := player.NewPlayer(playerID, fmt.Sprintf("AGENT-%d", playerID), token)
	ctx.players[playerID] = p
	ctx.mockPlayerRepo.AddPlayer(p)
	return nil
}

func (ctx *deliverContractContext) anAcceptedContractForPlayerWithDeliveryOfToWaypoint(
	contractID string,
	playerID int,
	units int,
	tradeSymbol string,
	waypointSymbol string,
) error {
	// Create contract terms
	terms := contract.ContractTerms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       tradeSymbol,
				DestinationSymbol: waypointSymbol,
				UnitsRequired:     units,
				UnitsFulfilled:    0,
			},
		},
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	// Create contract
	c, err := contract.NewContract(contractID, playerID, "COSMIC", "PROCUREMENT", terms)
	if err != nil {
		return err
	}

	// Accept it
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c
	ctx.contractRepo.AddContract(c)

	return nil
}

func (ctx *deliverContractContext) anAcceptedContractForPlayerWithOfAlreadyDeliveredToWaypoint(
	contractID string,
	playerID int,
	unitsFulfilled int,
	unitsRequired int,
	tradeSymbol string,
	waypointSymbol string,
) error {
	// Create contract terms
	terms := contract.ContractTerms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       tradeSymbol,
				DestinationSymbol: waypointSymbol,
				UnitsRequired:     unitsRequired,
				UnitsFulfilled:    unitsFulfilled,
			},
		},
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	// Create contract
	c, err := contract.NewContract(contractID, playerID, "COSMIC", "PROCUREMENT", terms)
	if err != nil {
		return err
	}

	// Accept it
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c
	ctx.contractRepo.AddContract(c)

	return nil
}

func (ctx *deliverContractContext) aShipOwnedByPlayerAtWaypointWithInCargo(
	shipSymbol string,
	playerID int,
	waypointSymbol string,
	cargoUnits int,
	tradeSymbol string,
) error {
	// Parse waypoint
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		return err
	}

	// Create cargo items
	item, err := shared.NewCargoItem(tradeSymbol, tradeSymbol, tradeSymbol, cargoUnits)
	if err != nil {
		return err
	}
	items := []*shared.CargoItem{item}

	// Determine cargo capacity (ensure it's large enough for the cargo units)
	cargoCapacity := 100
	if cargoUnits > 100 {
		cargoCapacity = cargoUnits + 50 // Add buffer
	}

	// Create cargo
	cargo, err := shared.NewCargo(cargoCapacity, cargoUnits, items)
	if err != nil {
		return err
	}

	// Create fuel
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		return err
	}

	// Create ship (docked at the waypoint)
	ship, err := navigation.NewShip(
		shipSymbol,
		playerID,
		waypoint,
		fuel,
		100,           // fuel capacity
		cargoCapacity, // cargo capacity
		cargo,
		30, // engine speed
		navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.apiClient.AddShip(ship)

	return nil
}

func (ctx *deliverContractContext) theAPIWillReturnSuccessfulDeliveryWithUnitsDelivered(units int) error {
	// Store the expected units for the API response
	ctx.apiDeliveries["expected"] = units
	return nil
}

// When steps

func (ctx *deliverContractContext) iSendDeliverContractCommandWithContractShipTradeUnitsPlayer(
	contractID string,
	shipSymbol string,
	tradeSymbol string,
	units int,
	playerID int,
) error {
	// Configure mock API client to return successful delivery if requested
	if expectedUnits, ok := ctx.apiDeliveries["expected"]; ok {
		// Find the contract to get updated delivery info
		c, exists := ctx.contracts[contractID]
		if exists {
			// Prepare the API response with updated delivery
			apiDeliveries := []infraports.DeliveryData{}
			for _, d := range c.Terms().Deliveries {
				apiDelivery := infraports.DeliveryData{
					TradeSymbol:       d.TradeSymbol,
					DestinationSymbol: d.DestinationSymbol,
					UnitsRequired:     d.UnitsRequired,
					UnitsFulfilled:    d.UnitsFulfilled,
				}
				if d.TradeSymbol == tradeSymbol {
					apiDelivery.UnitsFulfilled = d.UnitsFulfilled + expectedUnits
				}
				apiDeliveries = append(apiDeliveries, apiDelivery)
			}

			apiContractData := &infraports.ContractData{
				Terms: infraports.ContractTermsData{
					Deliveries: apiDeliveries,
				},
			}

			ctx.apiClient.SetDeliverContractFunc(func(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*infraports.ContractData, error) {
				return apiContractData, nil
			})
		}
	}

	// Create command
	cmd := &appContract.DeliverContractCommand{
		ContractID:  contractID,
		ShipSymbol:  shipSymbol,
		TradeSymbol: tradeSymbol,
		Units:       units,
		PlayerID:    playerID,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appContract.DeliverContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *deliverContractContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *deliverContractContext) unitsShouldBeDelivered(units int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if ctx.response.UnitsDelivered != units {
		return fmt.Errorf("expected %d units delivered but got %d", units, ctx.response.UnitsDelivered)
	}

	return nil
}

func (ctx *deliverContractContext) theContractShouldShowUnitsFulfilledFor(expectedUnits int, tradeSymbol string) error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// Find the delivery for this trade symbol
	var found bool
	var actualUnits int
	for _, delivery := range ctx.response.Contract.Terms().Deliveries {
		if delivery.TradeSymbol == tradeSymbol {
			found = true
			actualUnits = delivery.UnitsFulfilled
			break
		}
	}

	if !found {
		return fmt.Errorf("delivery for trade symbol %s not found", tradeSymbol)
	}

	if actualUnits != expectedUnits {
		return fmt.Errorf("expected %d units fulfilled for %s but got %d", expectedUnits, tradeSymbol, actualUnits)
	}

	return nil
}

func (ctx *deliverContractContext) theCommandShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
	}

	// Check if error message contains expected text (case-insensitive substring match)
	errMsg := strings.ToLower(ctx.err.Error())
	expectedLower := strings.ToLower(expectedError)

	if !strings.Contains(errMsg, expectedLower) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
	}

	return nil
}

// Register steps

func InitializeDeliverContractScenario(ctx *godog.ScenarioContext) {
	deliverCtx := &deliverContractContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		deliverCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^a player with ID (\d+) and token "([^"]*)"$`, deliverCtx.aPlayerWithIDAndToken)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with delivery of (\d+) "([^"]*)" to "([^"]*)"$`, deliverCtx.anAcceptedContractForPlayerWithDeliveryOfToWaypoint)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with (\d+) of (\d+) "([^"]*)" already delivered to "([^"]*)"$`, deliverCtx.anAcceptedContractForPlayerWithOfAlreadyDeliveredToWaypoint)
	ctx.Step(`^a ship "([^"]*)" owned by player (\d+) at waypoint "([^"]*)" with (\d+) "([^"]*)" in cargo$`, deliverCtx.aShipOwnedByPlayerAtWaypointWithInCargo)
	ctx.Step(`^the API will return successful delivery with (\d+) units delivered$`, deliverCtx.theAPIWillReturnSuccessfulDeliveryWithUnitsDelivered)
	ctx.Step(`^I send DeliverContractCommand with contract "([^"]*)", ship "([^"]*)", trade "([^"]*)", (\d+) units, player (\d+)$`, deliverCtx.iSendDeliverContractCommandWithContractShipTradeUnitsPlayer)
	ctx.Step(`^the deliver command should succeed$`, deliverCtx.theCommandShouldSucceed)
	ctx.Step(`^(\d+) units should be delivered$`, deliverCtx.unitsShouldBeDelivered)
	ctx.Step(`^the contract should show (\d+) units fulfilled for "([^"]*)"$`, deliverCtx.theContractShouldShowUnitsFulfilledFor)
	ctx.Step(`^the deliver command should fail with error "([^"]*)"$`, deliverCtx.theCommandShouldFailWithError)
}
