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
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type fulfillContractHandlerContext struct {
	// Test data
	contracts   map[string]*contract.Contract
	players     map[int]*player.Player
	playerID    shared.PlayerID

	// Response/Error tracking
	response    *commands.FulfillContractResponse
	err         error

	// REAL dependencies (NO MOCK REPOS!)
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository

	// Mock dependencies
	apiClient    *helpers.MockAPIClient
	clock        *shared.MockClock

	// Handler
	handler      *commands.FulfillContractHandler
}

func (ctx *fulfillContractHandlerContext) reset() {
	ctx.contracts = make(map[string]*contract.Contract)
	ctx.players = make(map[int]*player.Player)
	ctx.response = nil
	ctx.err = nil

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

	// Mock clock starting at fixed time
	ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	// Create handler with real repos + mock API client
	ctx.handler = commands.NewFulfillContractHandler(
		ctx.contractRepo,
		ctx.playerRepo,
		ctx.apiClient,
	)
}

// Given steps

func (ctx *fulfillContractHandlerContext) theCurrentTimeIs(timeStr string) error {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("invalid time format: %w", err)
	}
	ctx.clock.SetTime(t)
	return nil
}

func (ctx *fulfillContractHandlerContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	ctx.playerID = pid

	p := player.NewPlayer(pid, fmt.Sprintf("AGENT-%d", playerID), token)
	ctx.players[playerID] = p

	// Save to database using REAL repository
	return ctx.playerRepo.Add(context.Background(), p)
}

func (ctx *fulfillContractHandlerContext) anAcceptedContractForPlayerWithAllDeliveriesComplete(contractID string, playerID int) error {
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

	// Accept the contract
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *fulfillContractHandlerContext) anAcceptedContractForPlayerWithIncompleteDeliveries(contractID string, playerID int) error {
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

	// Accept the contract
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *fulfillContractHandlerContext) theAPIWillSuccessfullyFulfillTheContract() error {
	// Configure mock API to succeed - it succeeds by default
	return nil
}

// When steps

func (ctx *fulfillContractHandlerContext) iExecuteFulfillContractCommandFor(contractID string, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	// Get player token from test data
	p, exists := ctx.players[playerID]
	if !exists {
		return fmt.Errorf("player %d not set up in test", playerID)
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), p.Token)

	// Create command
	cmd := &commands.FulfillContractCommand{
		ContractID: contractID,
		PlayerID:   pid,
	}

	// Execute handler
	response, err := ctx.handler.Handle(cmdCtx, cmd)

	// Store response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*commands.FulfillContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

func (ctx *fulfillContractHandlerContext) iTryToExecuteFulfillContractCommandFor(contractID string, playerID int) error {
	return ctx.iExecuteFulfillContractCommandFor(contractID, playerID)
}

// Then steps

func (ctx *fulfillContractHandlerContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *fulfillContractHandlerContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
	}

	errMsg := strings.ToLower(ctx.err.Error())
	expectedLower := strings.ToLower(expectedError)

	if !strings.Contains(errMsg, expectedLower) {
		return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
	}

	return nil
}

func (ctx *fulfillContractHandlerContext) theContractShouldBeMarkedAsFulfilled() error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}
	if !ctx.response.Contract.Fulfilled() {
		return fmt.Errorf("expected contract to be fulfilled")
	}
	return nil
}

func (ctx *fulfillContractHandlerContext) theContractShouldBePersistedWithFulfilledStatus() error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}

	// Reload from database to verify persistence
	reloaded, err := ctx.contractRepo.FindByID(context.Background(), ctx.response.Contract.ContractID())
	if err != nil {
		return fmt.Errorf("failed to reload contract: %w", err)
	}

	if !reloaded.Fulfilled() {
		return fmt.Errorf("contract not persisted as fulfilled")
	}

	return nil
}

// Register steps

func InitializeFulfillContractHandlerScenario(ctx *godog.ScenarioContext) {
	handlerCtx := &fulfillContractHandlerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		handlerCtx.reset()
		return ctx, nil
	})

	// Register ONLY fulfill-specific steps (shared steps are already registered by accept handler)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with all deliveries complete$`, handlerCtx.anAcceptedContractForPlayerWithAllDeliveriesComplete)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with incomplete deliveries$`, handlerCtx.anAcceptedContractForPlayerWithIncompleteDeliveries)
	ctx.Step(`^the API will successfully fulfill the contract$`, handlerCtx.theAPIWillSuccessfullyFulfillTheContract)
	ctx.Step(`^I execute fulfill contract command for "([^"]*)" with player (\d+)$`, handlerCtx.iExecuteFulfillContractCommandFor)
	ctx.Step(`^I try to execute fulfill contract command for "([^"]*)" with player (\d+)$`, handlerCtx.iTryToExecuteFulfillContractCommandFor)
	ctx.Step(`^the contract should be marked as fulfilled$`, handlerCtx.theContractShouldBeMarkedAsFulfilled)
	ctx.Step(`^the contract should be persisted with fulfilled status$`, handlerCtx.theContractShouldBePersistedWithFulfilledStatus)
}
