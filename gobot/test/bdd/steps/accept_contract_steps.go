package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/test/helpers"
)

type acceptContractContext struct {
	contracts      map[string]*contract.Contract
	playerID       int
	response       *appContract.AcceptContractResponse
	err            error
	acceptContract bool

	// Database and repositories
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository
	apiClient    *helpers.MockAPIClient
	handler      *appContract.AcceptContractHandler
}

func (ctx *acceptContractContext) reset() {
	ctx.contracts = make(map[string]*contract.Contract)
	ctx.playerID = 0
	ctx.response = nil
	ctx.err = nil
	ctx.acceptContract = false

	// Create in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic(fmt.Errorf("failed to open test database: %w", err))
	}

	// Run migrations
	err = db.AutoMigrate(
		&persistence.ContractModel{},
		&persistence.PlayerModel{},
	)
	if err != nil {
		panic(fmt.Errorf("failed to migrate database: %w", err))
	}

	ctx.db = db

	// Create real repositories
	ctx.contractRepo = persistence.NewGormContractRepository(db)
	ctx.playerRepo = persistence.NewGormPlayerRepository(db)

	// Keep mock API client
	ctx.apiClient = helpers.NewMockAPIClient()

	ctx.handler = appContract.NewAcceptContractHandler(ctx.contractRepo, ctx.playerRepo, ctx.apiClient)
}

// Given steps

func (ctx *acceptContractContext) anUnacceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
	// Create default payment
	payment := contract.Payment{
		OnAccepted:  10000,
		OnFulfilled: 50000,
	}

	// Create default delivery
	deliveries := []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    0,
		},
	}

	// Create contract terms
	terms := contract.ContractTerms{
		Payment:          payment,
		Deliveries:       deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	// Create unaccepted contract
	c, err := contract.NewContract(contractID, playerID, "COSMIC", "PROCUREMENT", terms)
	if err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database using real repository
	if err := ctx.contractRepo.Save(context.Background(), c); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}

	return nil
}

func (ctx *acceptContractContext) anAcceptedContractForPlayerInTheDatabase(contractID string, playerID int) error {
	// Create an unaccepted contract first
	if err := ctx.anUnacceptedContractForPlayerInTheDatabase(contractID, playerID); err != nil {
		return err
	}

	// Accept it
	c := ctx.contracts[contractID]
	if err := c.Accept(); err != nil {
		return err
	}

	// Update in repository
	if err := ctx.contractRepo.Save(context.Background(), c); err != nil {
		return fmt.Errorf("failed to save accepted contract: %w", err)
	}

	return nil
}

func (ctx *acceptContractContext) aPlayerWithIDExistsInTheDatabase(playerID int) error {
	ctx.playerID = playerID

	// Add player to database using real repository
	p := player.NewPlayer(playerID, "TEST-AGENT", "test-token-123")
	if err := ctx.playerRepo.Save(context.Background(), p); err != nil {
		return fmt.Errorf("failed to create player: %w", err)
	}

	return nil
}

func (ctx *acceptContractContext) theAPIWillSuccessfullyAcceptTheContract() error {
	ctx.acceptContract = true
	return nil
}

// When steps

func (ctx *acceptContractContext) iExecuteAcceptContractCommandForWithPlayer(contractID string, playerID int) error {
	return ctx.executeAcceptContract(contractID, playerID)
}

func (ctx *acceptContractContext) iTryToExecuteAcceptContractCommandForWithPlayer(contractID string, playerID int) error {
	return ctx.executeAcceptContract(contractID, playerID)
}

func (ctx *acceptContractContext) executeAcceptContract(contractID string, playerID int) error {
	// Ensure player exists
	if ctx.playerID == 0 {
		ctx.playerID = playerID
		p := player.NewPlayer(playerID, "TEST-AGENT", "test-token-123")
		if err := ctx.playerRepo.Save(context.Background(), p); err != nil {
			return fmt.Errorf("failed to create player: %w", err)
		}
	}

	// Configure mock API client to succeed if requested
	if ctx.acceptContract {
		// Mock API returns contract data (not used in current implementation)
	}

	// Create command
	cmd := &appContract.AcceptContractCommand{
		ContractID: contractID,
		PlayerID:   playerID,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appContract.AcceptContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *acceptContractContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *acceptContractContext) theContractShouldBeMarkedAsAccepted() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if !ctx.response.Contract.Accepted() {
		return fmt.Errorf("expected contract to be accepted but it is not")
	}

	return nil
}

func (ctx *acceptContractContext) theContractShouldStillNotBeFulfilled() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if ctx.response.Contract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled but it is")
	}

	return nil
}

func (ctx *acceptContractContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
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

func (ctx *acceptContractContext) theContractShouldBePersistedWithAcceptedStatus() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// Verify contract was saved in repository
	savedContract, err := ctx.contractRepo.FindByID(context.Background(), ctx.response.Contract.ContractID(), ctx.response.Contract.PlayerID())
	if err != nil {
		return fmt.Errorf("expected contract to be saved in repository but got error: %v", err)
	}

	if !savedContract.Accepted() {
		return fmt.Errorf("expected saved contract to be accepted but it is not")
	}

	return nil
}

// Register steps

func InitializeAcceptContractScenario(ctx *godog.ScenarioContext) {
	acceptCtx := &acceptContractContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		acceptCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^an unaccepted contract "([^"]*)" for player (\d+) in the database$`, acceptCtx.anUnacceptedContractForPlayerInTheDatabase)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) in the database$`, acceptCtx.anAcceptedContractForPlayerInTheDatabase)
	ctx.Step(`^a player with ID (\d+) exists in the database$`, acceptCtx.aPlayerWithIDExistsInTheDatabase)
	ctx.Step(`^the API will successfully accept the contract$`, acceptCtx.theAPIWillSuccessfullyAcceptTheContract)
	ctx.Step(`^I execute accept contract command for "([^"]*)" with player (\d+)$`, acceptCtx.iExecuteAcceptContractCommandForWithPlayer)
	ctx.Step(`^I try to execute accept contract command for "([^"]*)" with player (\d+)$`, acceptCtx.iTryToExecuteAcceptContractCommandForWithPlayer)
	ctx.Step(`^the command should succeed$`, acceptCtx.theCommandShouldSucceed)
	ctx.Step(`^the contract should be marked as accepted$`, acceptCtx.theContractShouldBeMarkedAsAccepted)
	ctx.Step(`^the contract should still not be fulfilled$`, acceptCtx.theContractShouldStillNotBeFulfilled)
	ctx.Step(`^the command should return an error containing "([^"]*)"$`, acceptCtx.theCommandShouldReturnAnErrorContaining)
	ctx.Step(`^the contract should be persisted with accepted status$`, acceptCtx.theContractShouldBePersistedWithAcceptedStatus)
}
