package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

// contractApplicationContext is a unified context for ALL contract application layer handlers
// This eliminates step definition conflicts by sharing state across all handlers
type contractApplicationContext struct {
	// Shared infrastructure
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository

	// All handlers
	acceptHandler  *commands.AcceptContractHandler
	deliverHandler *commands.DeliverContractHandler
	fulfillHandler *commands.FulfillContractHandler

	// Generic state tracking (works for all handlers)
	lastError    error
	lastContract *contract.Contract
	lastResponse interface{} // Can be any handler's response type

	// Mocks
	apiClient *helpers.MockAPIClient
	clock     *shared.MockClock
}

func (ctx *contractApplicationContext) reset() {
	ctx.lastError = nil
	ctx.lastContract = nil
	ctx.lastResponse = nil

	// Truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		panic(fmt.Errorf("failed to truncate tables: %w", err))
	}

	// Use shared test DB with REAL GORM repositories
	ctx.db = helpers.SharedTestDB
	ctx.contractRepo = persistence.NewGormContractRepository(helpers.SharedTestDB)
	ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)

	// Mock API client
	ctx.apiClient = helpers.NewMockAPIClient()

	// Set up default mock behaviors that return success
	ctx.setupDefaultMockBehaviors()

	// Mock clock starting at fixed time
	ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	// Create all handlers with shared infrastructure
	ctx.acceptHandler = commands.NewAcceptContractHandler(
		ctx.contractRepo,
		ctx.playerRepo,
		ctx.apiClient,
	)

	ctx.deliverHandler = commands.NewDeliverContractHandler(
		ctx.contractRepo,
		ctx.apiClient,
		ctx.playerRepo,
	)

	ctx.fulfillHandler = commands.NewFulfillContractHandler(
		ctx.contractRepo,
		ctx.playerRepo,
		ctx.apiClient,
	)
}

// setupDefaultMockBehaviors configures the mock API to return successful responses
func (ctx *contractApplicationContext) setupDefaultMockBehaviors() {
	// DeliverContract mock - returns the updated contract data with accumulated totals
	ctx.apiClient.SetDeliverContractFunc(func(ctxAPI context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*infraPorts.ContractData, error) {
		// Load the current contract state from repository to get existing deliveries
		existingContract, err := ctx.contractRepo.FindByID(ctxAPI, contractID)
		if err != nil {
			return nil, fmt.Errorf("contract not found: %w", err)
		}

		// Calculate accumulated units by finding the matching delivery and adding new units
		terms := existingContract.Terms()
		var accumulatedUnits int
		for _, delivery := range terms.Deliveries {
			if delivery.TradeSymbol == tradeSymbol {
				accumulatedUnits = delivery.UnitsFulfilled + units
				break
			}
		}

		// Return the accumulated total, simulating what the real API would return
		return &infraPorts.ContractData{
			ID: contractID,
			Terms: infraPorts.ContractTermsData{
				Deliveries: []infraPorts.DeliveryData{
					{
						TradeSymbol:    tradeSymbol,
						UnitsFulfilled: accumulatedUnits,
					},
				},
			},
		}, nil
	})

	// FulfillContract mock - returns the updated contract data
	ctx.apiClient.SetFulfillContractFunc(func(ctxAPI context.Context, contractID, token string) (*infraPorts.ContractData, error) {
		return &infraPorts.ContractData{
			ID:       contractID,
			Fulfilled: true,
		}, nil
	})

	// AcceptContract mock - returns the updated contract data
	ctx.apiClient.SetAcceptContractFunc(func(ctxAPI context.Context, contractID, token string) (*infraPorts.ContractData, error) {
		return &infraPorts.ContractData{
			ID:       contractID,
			Accepted: true,
		}, nil
	})
}

// ==================== GIVEN STEPS ====================

func (ctx *contractApplicationContext) theCurrentTimeIs(timeStr string) error {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("invalid time format: %w", err)
	}
	ctx.clock.SetTime(t)
	return nil
}

func (ctx *contractApplicationContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	p := player.NewPlayer(pid, fmt.Sprintf("AGENT-%d", playerID), token)

	// Save to database using REAL repository
	return ctx.playerRepo.Add(context.Background(), p)
}

func (ctx *contractApplicationContext) anUnacceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-MARKET",
				UnitsRequired:     100,
				UnitsFulfilled:    0,
			},
		},
		DeadlineToAccept: "2099-12-31T23:59:59Z",
		Deadline:         "2100-01-31T23:59:59Z",
	}

	c, err := contract.NewContract(contractID, pid, "COMMERCE_REPUBLIC", "PROCUREMENT", terms, ctx.clock)
	if err != nil {
		return err
	}

	// Save to database
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) anAcceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
	// First create unaccepted contract
	if err := ctx.anUnacceptedContractForPlayerInTheDatabase(contractID, playerID); err != nil {
		return err
	}

	// Load, accept, and save
	c, err := ctx.contractRepo.FindByID(context.Background(), contractID)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) anAcceptedContractForPlayerWithDeliveryOf(contractID string, playerID int, units int, tradeSymbol string, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       tradeSymbol,
				DestinationSymbol: waypoint,
				UnitsRequired:     units,
				UnitsFulfilled:    0,
			},
		},
		DeadlineToAccept: "2099-12-31T23:59:59Z",
		Deadline:         "2100-01-31T23:59:59Z",
	}

	c, err := contract.NewContract(contractID, pid, "COMMERCE_REPUBLIC", "PROCUREMENT", terms, ctx.clock)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) anAcceptedContractForPlayerWithAlreadyDelivered(contractID string, playerID int, unitsFulfilled int, unitsRequired int, tradeSymbol string, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       tradeSymbol,
				DestinationSymbol: waypoint,
				UnitsRequired:     unitsRequired,
				UnitsFulfilled:    unitsFulfilled,
			},
		},
		DeadlineToAccept: "2099-12-31T23:59:59Z",
		Deadline:         "2100-01-31T23:59:59Z",
	}

	c, err := contract.NewContract(contractID, pid, "COMMERCE_REPUBLIC", "PROCUREMENT", terms, ctx.clock)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) anAcceptedContractForPlayerWithAllDeliveriesComplete(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-MARKET",
				UnitsRequired:     100,
				UnitsFulfilled:    100, // All delivered
			},
		},
		DeadlineToAccept: "2099-12-31T23:59:59Z",
		Deadline:         "2100-01-31T23:59:59Z",
	}

	c, err := contract.NewContract(contractID, pid, "COMMERCE_REPUBLIC", "PROCUREMENT", terms, ctx.clock)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) anAcceptedContractForPlayerWithIncompleteDeliveries(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-MARKET",
				UnitsRequired:     100,
				UnitsFulfilled:    50, // Only half delivered
			},
		},
		DeadlineToAccept: "2099-12-31T23:59:59Z",
		Deadline:         "2100-01-31T23:59:59Z",
	}

	c, err := contract.NewContract(contractID, pid, "COMMERCE_REPUBLIC", "PROCUREMENT", terms, ctx.clock)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *contractApplicationContext) theAPIWillSuccessfullyAcceptTheContract() error {
	// Mock API succeeds by default
	return nil
}

func (ctx *contractApplicationContext) theAPIWillSuccessfullyFulfillTheContract() error {
	// Mock API succeeds by default
	return nil
}

// ==================== WHEN STEPS ====================

func (ctx *contractApplicationContext) iExecuteAcceptContractCommandFor(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	// Get player token from database
	p, err := ctx.playerRepo.FindByID(context.Background(), pid)
	if err != nil {
		return fmt.Errorf("player %d not found in database", playerID)
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), p.Token)

	// Create command
	cmd := &commands.AcceptContractCommand{
		ContractID: contractID,
		PlayerID:   pid,
	}

	// Execute handler
	response, err := ctx.acceptHandler.Handle(cmdCtx, cmd)

	// Store results
	ctx.lastError = err
	if err == nil {
		ctx.lastResponse = response
		ctx.lastContract = response.(*commands.AcceptContractResponse).Contract
	} else {
		ctx.lastResponse = nil
		ctx.lastContract = nil
	}

	return nil
}

func (ctx *contractApplicationContext) iTryToExecuteAcceptContractCommandFor(contractID string, playerID int) error {
	return ctx.iExecuteAcceptContractCommandFor(contractID, playerID)
}

func (ctx *contractApplicationContext) iExecuteDeliverContractCommandFor(contractID string, units int, tradeSymbol string, shipSymbol string) error {
	// Try to get contract from database to find player
	// If contract doesn't exist, use default player 1 (for error testing scenarios)
	var playerID shared.PlayerID
	var token string

	c, err := ctx.contractRepo.FindByID(context.Background(), contractID)
	if err != nil {
		// Contract not found - use default player 1 for testing error scenarios
		playerID, _ = shared.NewPlayerID(1)
		p, err := ctx.playerRepo.FindByID(context.Background(), playerID)
		if err != nil {
			// If player 1 doesn't exist either, use test token
			token = "test-token"
		} else {
			token = p.Token
		}
	} else {
		playerID = c.PlayerID()
		// Get player token from database
		p, err := ctx.playerRepo.FindByID(context.Background(), playerID)
		if err != nil {
			// Player not found - use test token and let handler deal with it
			token = "test-token-invalid"
		} else {
			token = p.Token
		}
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), token)

	// Create command
	cmd := &commands.DeliverContractCommand{
		ContractID:  contractID,
		ShipSymbol:  shipSymbol,
		TradeSymbol: tradeSymbol,
		Units:       units,
		PlayerID:    playerID,
	}

	// Execute handler
	response, err := ctx.deliverHandler.Handle(cmdCtx, cmd)

	// Store results
	ctx.lastError = err
	if err == nil {
		ctx.lastResponse = response
		ctx.lastContract = response.(*commands.DeliverContractResponse).Contract
	} else {
		ctx.lastResponse = nil
		ctx.lastContract = nil
	}

	return nil
}

func (ctx *contractApplicationContext) iTryToExecuteDeliverContractCommandFor(contractID string, units int, tradeSymbol string, shipSymbol string) error {
	return ctx.iExecuteDeliverContractCommandFor(contractID, units, tradeSymbol, shipSymbol)
}

func (ctx *contractApplicationContext) iExecuteFulfillContractCommandFor(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	// Get player token from database
	p, err := ctx.playerRepo.FindByID(context.Background(), pid)
	if err != nil {
		return fmt.Errorf("player %d not found in database", playerID)
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), p.Token)

	// Create command
	cmd := &commands.FulfillContractCommand{
		ContractID: contractID,
		PlayerID:   pid,
	}

	// Execute handler
	response, err := ctx.fulfillHandler.Handle(cmdCtx, cmd)

	// Store results
	ctx.lastError = err
	if err == nil {
		ctx.lastResponse = response
		ctx.lastContract = response.(*commands.FulfillContractResponse).Contract
	} else {
		ctx.lastResponse = nil
		ctx.lastContract = nil
	}

	return nil
}

func (ctx *contractApplicationContext) iTryToExecuteFulfillContractCommandFor(contractID string, playerID int) error {
	return ctx.iExecuteFulfillContractCommandFor(contractID, playerID)
}

// ==================== THEN STEPS ====================

func (ctx *contractApplicationContext) theCommandShouldSucceed() error {
	if ctx.lastError != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.lastError)
	}
	if ctx.lastResponse == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *contractApplicationContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
	if ctx.lastError == nil {
		return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
	}

	errMsg := strings.ToLower(ctx.lastError.Error())
	expectedLower := strings.ToLower(expectedError)

	if !strings.Contains(errMsg, expectedLower) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.lastError)
	}

	return nil
}

func (ctx *contractApplicationContext) theContractShouldBeMarkedAsAccepted() error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}
	if !ctx.lastContract.Accepted() {
		return fmt.Errorf("expected contract to be accepted")
	}
	return nil
}

func (ctx *contractApplicationContext) theContractShouldStillNotBeFulfilled() error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}
	if ctx.lastContract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled")
	}
	return nil
}

func (ctx *contractApplicationContext) theContractShouldBePersistedWithAcceptedStatus() error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}

	// Reload from database to verify persistence
	reloaded, err := ctx.contractRepo.FindByID(context.Background(), ctx.lastContract.ContractID())
	if err != nil {
		return fmt.Errorf("failed to reload contract: %w", err)
	}

	if !reloaded.Accepted() {
		return fmt.Errorf("contract not persisted as accepted")
	}

	return nil
}

func (ctx *contractApplicationContext) theDeliveryForShouldShowUnitsFulfilled(tradeSymbol string, units int) error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}

	for _, delivery := range ctx.lastContract.Terms().Deliveries {
		if delivery.TradeSymbol == tradeSymbol {
			if delivery.UnitsFulfilled != units {
				return fmt.Errorf("expected %d units fulfilled for %s, got %d", units, tradeSymbol, delivery.UnitsFulfilled)
			}
			return nil
		}
	}

	return fmt.Errorf("trade symbol %s not found in deliveries", tradeSymbol)
}

func (ctx *contractApplicationContext) theContractShouldBeMarkedAsFulfilled() error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}
	if !ctx.lastContract.Fulfilled() {
		return fmt.Errorf("expected contract to be fulfilled")
	}
	return nil
}

func (ctx *contractApplicationContext) theContractShouldBePersistedWithFulfilledStatus() error {
	if ctx.lastContract == nil {
		return fmt.Errorf("no contract available")
	}

	// Reload from database to verify persistence
	reloaded, err := ctx.contractRepo.FindByID(context.Background(), ctx.lastContract.ContractID())
	if err != nil {
		return fmt.Errorf("failed to reload contract: %w", err)
	}

	if !reloaded.Fulfilled() {
		return fmt.Errorf("contract not persisted as fulfilled")
	}

	return nil
}

// ==================== REGISTRATION ====================

func InitializeContractApplicationScenarios(sc *godog.ScenarioContext) {
	ctx := &contractApplicationContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Register all Given steps
	sc.Step(`^the current time is "([^"]*)"$`, ctx.theCurrentTimeIs)
	sc.Step(`^a player with ID (\d+) and token "([^"]*)" exists in the database$`, ctx.aPlayerWithIDAndTokenExistsInTheDatabase)
	sc.Step(`^an unaccepted contract "([^"]*)" for player (\d+) in the database$`, ctx.anUnacceptedContractForPlayerInTheDatabase)
	sc.Step(`^an accepted contract "([^"]*)" for player (\d+) in the database$`, ctx.anAcceptedContractForPlayerInTheDatabase)
	sc.Step(`^an accepted contract "([^"]*)" for player (\d+) with delivery of (\d+) "([^"]*)" to waypoint "([^"]*)"$`, ctx.anAcceptedContractForPlayerWithDeliveryOf)
	sc.Step(`^an accepted contract "([^"]*)" for player (\d+) with (\d+) of (\d+) "([^"]*)" already delivered to waypoint "([^"]*)"$`, ctx.anAcceptedContractForPlayerWithAlreadyDelivered)
	sc.Step(`^an accepted contract "([^"]*)" for player (\d+) with all deliveries complete$`, ctx.anAcceptedContractForPlayerWithAllDeliveriesComplete)
	sc.Step(`^an accepted contract "([^"]*)" for player (\d+) with incomplete deliveries$`, ctx.anAcceptedContractForPlayerWithIncompleteDeliveries)
	sc.Step(`^the API will successfully accept the contract$`, ctx.theAPIWillSuccessfullyAcceptTheContract)
	sc.Step(`^the API will successfully fulfill the contract$`, ctx.theAPIWillSuccessfullyFulfillTheContract)

	// Register all When steps
	sc.Step(`^I execute accept contract command for "([^"]*)" with player (\d+)$`, ctx.iExecuteAcceptContractCommandFor)
	sc.Step(`^I try to execute accept contract command for "([^"]*)" with player (\d+)$`, ctx.iTryToExecuteAcceptContractCommandFor)
	sc.Step(`^I execute deliver contract command for "([^"]*)" with (\d+) units of "([^"]*)" from ship "([^"]*)"$`, ctx.iExecuteDeliverContractCommandFor)
	sc.Step(`^I try to execute deliver contract command for "([^"]*)" with (\d+) units of "([^"]*)" from ship "([^"]*)"$`, ctx.iTryToExecuteDeliverContractCommandFor)
	sc.Step(`^I execute fulfill contract command for "([^"]*)" with player (\d+)$`, ctx.iExecuteFulfillContractCommandFor)
	sc.Step(`^I try to execute fulfill contract command for "([^"]*)" with player (\d+)$`, ctx.iTryToExecuteFulfillContractCommandFor)

	// Register all Then steps
	sc.Step(`^the command should succeed$`, ctx.theCommandShouldSucceed)
	sc.Step(`^the command should return an error containing "([^"]*)"$`, ctx.theCommandShouldReturnAnErrorContaining)
	sc.Step(`^the contract should be marked as accepted$`, ctx.theContractShouldBeMarkedAsAccepted)
	sc.Step(`^the contract should still not be fulfilled$`, ctx.theContractShouldStillNotBeFulfilled)
	sc.Step(`^the contract should be persisted with accepted status$`, ctx.theContractShouldBePersistedWithAcceptedStatus)
	sc.Step(`^the contract should have delivery for "([^"]*)" with (\d+) units fulfilled$`, ctx.theDeliveryForShouldShowUnitsFulfilled)
	sc.Step(`^the contract should be marked as fulfilled$`, ctx.theContractShouldBeMarkedAsFulfilled)
	sc.Step(`^the contract should be persisted with fulfilled status$`, ctx.theContractShouldBePersistedWithFulfilledStatus)
}
