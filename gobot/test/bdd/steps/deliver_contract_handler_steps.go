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

type deliverContractHandlerContext struct {
	// Test data
	contracts   map[string]*contract.Contract
	players     map[int]*player.Player
	playerID    shared.PlayerID

	// Response/Error tracking
	response    *commands.DeliverContractResponse
	err         error

	// REAL dependencies (NO MOCK REPOS!)
	db           *gorm.DB
	contractRepo *persistence.GormContractRepository
	playerRepo   *persistence.GormPlayerRepository

	// Mock dependencies
	apiClient    *helpers.MockAPIClient
	clock        *shared.MockClock

	// Handler
	handler      *commands.DeliverContractHandler
}

func (ctx *deliverContractHandlerContext) reset() {
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
	ctx.handler = commands.NewDeliverContractHandler(
		ctx.contractRepo,
		ctx.apiClient,
		ctx.playerRepo,
	)
}

// Given steps

func (ctx *deliverContractHandlerContext) theCurrentTimeIs(timeStr string) error {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return fmt.Errorf("invalid time format: %w", err)
	}
	ctx.clock.SetTime(t)
	return nil
}

func (ctx *deliverContractHandlerContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
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

func (ctx *deliverContractHandlerContext) anAcceptedContractForPlayerWithDeliveryOf(contractID string, playerID int, units int, tradeSymbol string, waypoint string) error {
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

	// Accept the contract
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	return ctx.contractRepo.Add(context.Background(), c)
}

func (ctx *deliverContractHandlerContext) anAcceptedContractForPlayerWithAlreadyDelivered(contractID string, playerID int, unitsFulfilled int, unitsRequired int, tradeSymbol string, waypoint string) error {
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

	// Accept the contract
	if err := c.Accept(); err != nil {
		return err
	}

	ctx.contracts[contractID] = c

	// Save to database
	return ctx.contractRepo.Add(context.Background(), c)
}

// When steps

func (ctx *deliverContractHandlerContext) iExecuteDeliverContractCommandFor(contractID string, units int, tradeSymbol string, shipSymbol string) error {
	// Get contract from database to find player ID and token
	c, err := ctx.contractRepo.FindByID(context.Background(), contractID)
	if err != nil {
		return fmt.Errorf("failed to find contract: %w", err)
	}

	playerID := c.PlayerID()

	// Get player from database to get token
	p, err := ctx.playerRepo.FindByID(context.Background(), playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Create context with token
	cmdCtx := common.WithPlayerToken(context.Background(), p.Token)

	// Create command
	cmd := &commands.DeliverContractCommand{
		ContractID:  contractID,
		ShipSymbol:  shipSymbol,
		TradeSymbol: tradeSymbol,
		Units:       units,
		PlayerID:    playerID,
	}

	// Execute handler
	response, err := ctx.handler.Handle(cmdCtx, cmd)

	// Store response and error
	ctx.err = err
	if err == nil {
		ctx.response = response.(*commands.DeliverContractResponse)
	} else {
		ctx.response = nil
	}

	return nil
}

func (ctx *deliverContractHandlerContext) iTryToExecuteDeliverContractCommandFor(contractID string, units int, tradeSymbol string, shipSymbol string) error {
	// Same as execute, we want to capture the error
	return ctx.iExecuteDeliverContractCommandFor(contractID, units, tradeSymbol, shipSymbol)
}

// Then steps

func (ctx *deliverContractHandlerContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	return nil
}

func (ctx *deliverContractHandlerContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
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

func (ctx *deliverContractHandlerContext) theDeliveryForShouldShowUnitsFulfilled(tradeSymbol string, units int) error {
	if ctx.response == nil {
		return fmt.Errorf("no response available")
	}

	for _, delivery := range ctx.response.Contract.Terms().Deliveries {
		if delivery.TradeSymbol == tradeSymbol {
			if delivery.UnitsFulfilled != units {
				return fmt.Errorf("expected %d units fulfilled for %s, got %d", units, tradeSymbol, delivery.UnitsFulfilled)
			}
			return nil
		}
	}

	return fmt.Errorf("trade symbol %s not found in deliveries", tradeSymbol)
}

// Register steps

func InitializeDeliverContractHandlerScenario(ctx *godog.ScenarioContext) {
	handlerCtx := &deliverContractHandlerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		handlerCtx.reset()
		return ctx, nil
	})

	// Register ONLY deliver-specific steps (shared steps are already registered by accept handler)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with delivery of (\d+) "([^"]*)" to waypoint "([^"]*)"$`, handlerCtx.anAcceptedContractForPlayerWithDeliveryOf)
	ctx.Step(`^an accepted contract "([^"]*)" for player (\d+) with (\d+) of (\d+) "([^"]*)" already delivered to waypoint "([^"]*)"$`, handlerCtx.anAcceptedContractForPlayerWithAlreadyDelivered)
	ctx.Step(`^I execute deliver contract command for "([^"]*)" with (\d+) units of "([^"]*)" from ship "([^"]*)"$`, handlerCtx.iExecuteDeliverContractCommandFor)
	ctx.Step(`^I try to execute deliver contract command for "([^"]*)" with (\d+) units of "([^"]*)" from ship "([^"]*)"$`, handlerCtx.iTryToExecuteDeliverContractCommandFor)
}
