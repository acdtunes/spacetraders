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

type acceptContractHandlerContext struct {
	// Test data
	contracts   map[string]*contract.Contract
	players     map[int]*player.Player
	playerID    shared.PlayerID

	// Response/Error tracking
	response    *commands.AcceptContractResponse
	err         error

	// REAL dependencies (NO MOCK REPOS!)
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository

	// Mock dependencies
	apiClient    *helpers.MockAPIClient
	clock        *shared.MockClock

	// Handler
	handler      *commands.AcceptContractHandler
}

func (ctx *acceptContractHandlerContext) reset() {
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

	// Mock clock starting at fixed time (can be overridden in Given steps)
	ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	// Create handler with real repos + mock API client
	ctx.handler = commands.NewAcceptContractHandler(
		ctx.contractRepo,
		ctx.playerRepo,
		ctx.apiClient,
	)
}

// Given steps

func (ctx *acceptContractHandlerContext) theCurrentTimeIs(timeStr string) error {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("invalid time format: %w", err)
	}
	ctx.clock.SetTime(t)
	return nil
}

func (ctx *acceptContractHandlerContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
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

func (ctx *acceptContractHandlerContext) anUnacceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
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

	ctx.contracts[contractID] = c

	// Save to database using REAL repository
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *acceptContractHandlerContext) anAcceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
	// First create unaccepted contract
	if err := ctx.anUnacceptedContractForPlayerInTheDatabase(contractID, playerID); err != nil {
		return err
	}

	// Accept it
	c := ctx.contracts[contractID]
	if err := c.Accept(); err != nil {
		return err
	}

	// Save updated state
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *acceptContractHandlerContext) theAPIWillSuccessfullyAcceptTheContract() error {
	// Configure mock API to succeed - it succeeds by default, no configuration needed
	return nil
}

// When steps

func (ctx *acceptContractHandlerContext) iExecuteAcceptContractCommandFor(contractID string, playerID int) error {
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
	cmd := &commands.AcceptContractCommand{
		ContractID: contractID,
		PlayerID:   pid,
	}

	// Execute handler
	response, err := ctx.handler.Handle(cmdCtx, cmd)

	// Store response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*commands.AcceptContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

func (ctx *acceptContractHandlerContext) iTryToExecuteAcceptContractCommandFor(contractID string, playerID int) error {
	return ctx.iExecuteAcceptContractCommandFor(contractID, playerID)
}

// Then steps

func (ctx *acceptContractHandlerContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *acceptContractHandlerContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
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

func (ctx *acceptContractHandlerContext) theContractShouldBeMarkedAsAccepted() error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}
	if !ctx.response.Contract.Accepted() {
		return fmt.Errorf("expected contract to be accepted")
	}
	return nil
}

func (ctx *acceptContractHandlerContext) theContractShouldStillNotBeFulfilled() error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}
	if ctx.response.Contract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled")
	}
	return nil
}

func (ctx *acceptContractHandlerContext) theContractShouldBePersistedWithAcceptedStatus() error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}

	// Reload from database to verify persistence
	reloaded, err := ctx.contractRepo.FindByID(context.Background(), ctx.response.Contract.ContractID())
	if err != nil {
		return fmt.Errorf("failed to reload contract: %w", err)
	}

	if !reloaded.Accepted() {
		return fmt.Errorf("contract not persisted as accepted")
	}

	return nil
}

// Register steps

func InitializeAcceptContractHandlerScenario(ctx *godog.ScenarioContext) {
	handlerCtx := &acceptContractHandlerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		handlerCtx.reset()
		return ctx, nil
	})

	// Register steps
	ctx.Step(`^the current time is "([^"]*)"$`, handlerCtx.theCurrentTimeIs)
	ctx.Step(`^a player with ID (\d+) and token "([^"]*)" exists in the database$`, handlerCtx.aPlayerWithIDAndTokenExistsInTheDatabase)
	ctx.Step(`^an unaccepted contract "([^"]*)" for player (\d+) in the database$`, handlerCtx.anUnacceptedContractForPlayerInTheDatabase)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) in the database$`, handlerCtx.anAcceptedContractForPlayerInTheDatabase)
	ctx.Step(`^the API will successfully accept the contract$`, handlerCtx.theAPIWillSuccessfullyAcceptTheContract)
	ctx.Step(`^I execute accept contract command for "([^"]*)" with player (\d+)$`, handlerCtx.iExecuteAcceptContractCommandFor)
	ctx.Step(`^I try to execute accept contract command for "([^"]*)" with player (\d+)$`, handlerCtx.iTryToExecuteAcceptContractCommandFor)
	ctx.Step(`^the command should succeed$`, handlerCtx.theCommandShouldSucceed)
	ctx.Step(`^the command should return an error containing "([^"]*)"$`, handlerCtx.theCommandShouldReturnAnErrorContaining)
	ctx.Step(`^the contract should be marked as accepted$`, handlerCtx.theContractShouldBeMarkedAsAccepted)
	ctx.Step(`^the contract should still not be fulfilled$`, handlerCtx.theContractShouldStillNotBeFulfilled)
	ctx.Step(`^the contract should be persisted with accepted status$`, handlerCtx.theContractShouldBePersistedWithAcceptedStatus)
}
