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

type fulfillContractContext struct {
	contracts      map[string]*contract.Contract
	playerID       int
	response       *appContract.FulfillContractResponse
	err            error
	fulfillSuccess bool

	// Database and repositories
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository
	apiClient    *helpers.MockAPIClient
	handler      *appContract.FulfillContractHandler
}

func (ctx *fulfillContractContext) reset() {
	ctx.contracts = make(map[string]*contract.Contract)
	ctx.playerID = 0
	ctx.response = nil
	ctx.err = nil
	ctx.fulfillSuccess = false

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

	// Create mock API client
	ctx.apiClient = helpers.NewMockAPIClient()

	ctx.handler = appContract.NewFulfillContractHandler(ctx.contractRepo, ctx.playerRepo, ctx.apiClient)
}

// Given steps

func (ctx *fulfillContractContext) aPlayerWithIDAndToken(playerID int, token string) error {
	ctx.playerID = playerID

	// Add player to database using real repository
	p := player.NewPlayer(playerID, "TEST-AGENT", token)
	if err := ctx.playerRepo.Save(context.Background(), p); err != nil {
		return fmt.Errorf("failed to create player: %w", err)
	}

	return nil
}

func (ctx *fulfillContractContext) anAcceptedContractForPlayerWithAllDeliveriesFulfilled(contractID string, playerID int) error {
	// Create payment
	payment := contract.Payment{
		OnAccepted:  10000,
		OnFulfilled: 50000,
	}

	// Create delivery with all units fulfilled
	deliveries := []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    100, // All units delivered
		},
	}

	// Create contract terms
	terms := contract.ContractTerms{
		Payment:          payment,
		Deliveries:       deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	// Create and accept contract
	c, err := contract.NewContract(contractID, playerID, "COSMIC", "PROCUREMENT", terms)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	if err := ctx.contractRepo.Save(context.Background(), c); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}

	return nil
}

func (ctx *fulfillContractContext) anAcceptedContractForPlayerWithIncompleteDeliveries(contractID string, playerID int) error {
	// Create payment
	payment := contract.Payment{
		OnAccepted:  10000,
		OnFulfilled: 50000,
	}

	// Create delivery with incomplete units
	deliveries := []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    50, // Only half delivered
		},
	}

	// Create contract terms
	terms := contract.ContractTerms{
		Payment:          payment,
		Deliveries:       deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	// Create and accept contract
	c, err := contract.NewContract(contractID, playerID, "COSMIC", "PROCUREMENT", terms)
	if err != nil {
		return err
	}

	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	if err := ctx.contractRepo.Save(context.Background(), c); err != nil {
		return fmt.Errorf("failed to save contract: %w", err)
	}

	return nil
}

func (ctx *fulfillContractContext) theAPIWillSuccessfullyFulfillTheContract() error {
	ctx.fulfillSuccess = true
	return nil
}

// When steps

func (ctx *fulfillContractContext) iSendFulfillContractCommandForWithPlayer(contractID string, playerID int) error {
	return ctx.executeFulfillContract(contractID, playerID)
}

func (ctx *fulfillContractContext) executeFulfillContract(contractID string, playerID int) error {
	// Ensure player exists if not already created
	if ctx.playerID == 0 {
		ctx.playerID = playerID
		p := player.NewPlayer(playerID, "TEST-AGENT", "test-token-123")
		if err := ctx.playerRepo.Save(context.Background(), p); err != nil {
			return fmt.Errorf("failed to create player: %w", err)
		}
	}

	// Configure mock API client to succeed if requested
	if ctx.fulfillSuccess {
		// Mock API returns contract data (not used in current implementation)
	}

	// Create command
	cmd := &appContract.FulfillContractCommand{
		ContractID: contractID,
		PlayerID:   playerID,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), cmd)

	// Store both response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*appContract.FulfillContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *fulfillContractContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *fulfillContractContext) theContractShouldBeMarkedAsFulfilled() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	if !ctx.response.Contract.Fulfilled() {
		return fmt.Errorf("expected contract to be fulfilled but it is not")
	}

	return nil
}

func (ctx *fulfillContractContext) theCommandShouldFailWithError(expectedError string) error {
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

func (ctx *fulfillContractContext) theContractShouldBePersistedWithFulfilledStatus() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}

	// Verify contract was saved in repository
	savedContract, err := ctx.contractRepo.FindByID(context.Background(), ctx.response.Contract.ContractID(), ctx.response.Contract.PlayerID())
	if err != nil {
		return fmt.Errorf("expected contract to be saved in repository but got error: %v", err)
	}

	if !savedContract.Fulfilled() {
		return fmt.Errorf("expected saved contract to be fulfilled but it is not")
	}

	return nil
}

// Register steps

func InitializeFulfillContractScenario(ctx *godog.ScenarioContext) {
	fulfillCtx := &fulfillContractContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		fulfillCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^a player with ID (\d+) and token "([^"]*)"$`, fulfillCtx.aPlayerWithIDAndToken)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with all deliveries fulfilled$`, fulfillCtx.anAcceptedContractForPlayerWithAllDeliveriesFulfilled)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with incomplete deliveries$`, fulfillCtx.anAcceptedContractForPlayerWithIncompleteDeliveries)
	ctx.Step(`^the API will successfully fulfill the contract$`, fulfillCtx.theAPIWillSuccessfullyFulfillTheContract)
	ctx.Step(`^I send FulfillContractCommand for "([^"]*)" with player (\d+)$`, fulfillCtx.iSendFulfillContractCommandForWithPlayer)
	ctx.Step(`^the command should succeed$`, fulfillCtx.theCommandShouldSucceed)
	ctx.Step(`^the contract should be marked as fulfilled$`, fulfillCtx.theContractShouldBeMarkedAsFulfilled)
	ctx.Step(`^the command should fail with error "([^"]*)"$`, fulfillCtx.theCommandShouldFailWithError)
	ctx.Step(`^the contract should be persisted with fulfilled status$`, fulfillCtx.theContractShouldBePersistedWithFulfilledStatus)
}
