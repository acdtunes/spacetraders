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
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

type evaluateContractProfitabilityContext struct {
	// Test state
	systemSymbol    string
	contract        *contract.Contract
	contractTerms   *contract.ContractTerms // Store terms while building contract
	shipSymbol      string
	playerID        int
	fuelCostPerTrip int
	response        *appContract.ProfitabilityResult
	err             error

	// SQLite database and repositories
	db         *gorm.DB
	shipRepo   *persistence.GormShipRepository
	marketRepo *persistence.MarketRepositoryGORM
	handler    *appContract.EvaluateContractProfitabilityHandler

	// Test data
	markets map[string][]market.TradeGood // waypoint -> trade goods
	ship    *navigation.Ship
}

func (ctx *evaluateContractProfitabilityContext) reset() {
	ctx.systemSymbol = ""
	ctx.contract = nil
	ctx.shipSymbol = "TEST-SHIP-1"
	ctx.playerID = 1
	ctx.fuelCostPerTrip = 0
	ctx.response = nil
	ctx.err = nil
	ctx.markets = make(map[string][]market.TradeGood)
	ctx.ship = nil

	// Initialize in-memory SQLite database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic(fmt.Sprintf("failed to open test database: %v", err))
	}

	// Auto-migrate the models
	err = db.AutoMigrate(
		&persistence.MarketData{},
		&persistence.TradeGoodData{},
	)
	if err != nil {
		panic(fmt.Sprintf("failed to migrate database: %v", err))
	}

	ctx.db = db
	ctx.marketRepo = persistence.NewMarketRepository(db)
}

// Background steps

func (ctx *evaluateContractProfitabilityContext) aSystem(systemSymbol string) error {
	ctx.systemSymbol = systemSymbol
	return nil
}

// Given steps

func (ctx *evaluateContractProfitabilityContext) aContractPayingCreditsOnAcceptanceAndOnFulfillment(onAccepted, onFulfilled int) error {
	payment := contract.Payment{
		OnAccepted:  onAccepted,
		OnFulfilled: onFulfilled,
	}

	// Store terms without deliveries yet - they'll be added by subsequent steps
	ctx.contractTerms = &contract.ContractTerms{
		Payment:          payment,
		Deliveries:       []contract.Delivery{}, // Will be populated by delivery steps
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	ctx.contract = nil // Will be created once deliveries are added
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theContractRequiresDeliveryOfTo(units int, tradeSymbol, destination string) error {
	if ctx.contractTerms == nil {
		return fmt.Errorf("contract terms not initialized")
	}

	// Add new delivery
	delivery := contract.Delivery{
		TradeSymbol:       tradeSymbol,
		DestinationSymbol: destination,
		UnitsRequired:     units,
		UnitsFulfilled:    0,
	}
	ctx.contractTerms.Deliveries = append(ctx.contractTerms.Deliveries, delivery)

	// Recreate contract with updated terms
	c, err := contract.NewContract(
		"TEST-CONTRACT-1",
		ctx.playerID,
		"COSMIC",
		"PROCUREMENT",
		*ctx.contractTerms,
	)
	if err != nil {
		return err
	}

	ctx.contract = c
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theContractRequiresDeliveryOfToWithUnitsAlreadyFulfilled(
	units int, tradeSymbol, destination string, fulfilled int,
) error {
	if ctx.contractTerms == nil {
		return fmt.Errorf("contract terms not initialized")
	}

	// Add new delivery with partial fulfillment
	delivery := contract.Delivery{
		TradeSymbol:       tradeSymbol,
		DestinationSymbol: destination,
		UnitsRequired:     units,
		UnitsFulfilled:    fulfilled,
	}
	ctx.contractTerms.Deliveries = append(ctx.contractTerms.Deliveries, delivery)

	// Recreate contract with updated terms
	c, err := contract.NewContract(
		"TEST-CONTRACT-1",
		ctx.playerID,
		"COSMIC",
		"PROCUREMENT",
		*ctx.contractTerms,
	)
	if err != nil {
		return err
	}

	ctx.contract = c
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theCheapestMarketSellsAtCreditsPerUnitInSystem(
	tradeSymbol string, sellPrice int, systemSymbol string,
) error {
	// Create a unique waypoint in the system for each trade symbol
	// Use trade symbol in waypoint name to ensure uniqueness
	waypointSymbol := fmt.Sprintf("%s-MARKET-%s", systemSymbol, tradeSymbol)

	// Create trade good
	good, err := market.NewTradeGood(tradeSymbol, strPtr("ABUNDANT"), nil, 0, sellPrice, 100)
	if err != nil {
		return fmt.Errorf("failed to create trade good: %w", err)
	}

	// Store in context for later persistence
	ctx.markets[waypointSymbol] = []market.TradeGood{*good}

	// Persist to database
	timestamp := time.Now()
	err = ctx.marketRepo.UpsertMarketData(context.Background(), uint(ctx.playerID), waypointSymbol, []market.TradeGood{*good}, timestamp)
	if err != nil {
		return fmt.Errorf("failed to persist market data: %w", err)
	}

	return nil
}

func (ctx *evaluateContractProfitabilityContext) noMarketSellsInSystem(tradeSymbol, systemSymbol string) error {
	// Don't create any market data for this trade symbol
	// This will cause FindCheapestMarketSelling to return nil
	return nil
}

func (ctx *evaluateContractProfitabilityContext) aShipWithCargoCapacity(capacity int) error {
	// Create a simple ship with the specified cargo capacity
	waypoint, _ := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(capacity, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		ctx.shipSymbol,
		ctx.playerID,
		waypoint,
		fuel,
		100,
		capacity,
		cargo,
		30,
		navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	ctx.ship = ship
	return nil
}

func (ctx *evaluateContractProfitabilityContext) fuelCostIsCreditsPerTrip(fuelCost int) error {
	ctx.fuelCostPerTrip = fuelCost
	return nil
}

// When steps

func (ctx *evaluateContractProfitabilityContext) iEvaluateTheContractProfitability() error {
	return ctx.executeEvaluation()
}

func (ctx *evaluateContractProfitabilityContext) iTryToEvaluateTheContractProfitability() error {
	return ctx.executeEvaluation()
}

func (ctx *evaluateContractProfitabilityContext) iTryToEvaluateTheContractProfitabilityWithAnInvalidShip() error {
	ctx.shipSymbol = "INVALID-SHIP"
	ctx.ship = nil // Ensure ship is not in memory
	return ctx.executeEvaluation()
}

func (ctx *evaluateContractProfitabilityContext) executeEvaluation() error {
	// Create a test ship repository that returns our test ship
	testShipRepo := newTestShipRepository(ctx.ship)

	// Create a market repository adapter that implements trading.MarketRepository
	marketRepoAdapter := newMarketRepositoryAdapter(ctx.marketRepo)

	ctx.handler = appContract.NewEvaluateContractProfitabilityHandler(
		testShipRepo,
		marketRepoAdapter,
	)

	// Create query
	query := &appContract.EvaluateContractProfitabilityQuery{
		Contract:        ctx.contract,
		ShipSymbol:      ctx.shipSymbol,
		PlayerID:        ctx.playerID,
		FuelCostPerTrip: ctx.fuelCostPerTrip,
	}

	// Execute handler
	response, err := ctx.handler.Handle(context.Background(), query)

	ctx.err = err
	if err == nil {
		ctx.response = response.(*appContract.ProfitabilityResult)
	} else {
		ctx.response = nil
	}

	return nil
}

// Then steps

func (ctx *evaluateContractProfitabilityContext) theNetProfitShouldBeCredits(expectedProfit int) error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if ctx.response.NetProfit != expectedProfit {
		return fmt.Errorf("expected net profit %d but got %d", expectedProfit, ctx.response.NetProfit)
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theContractShouldBeProfitable() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if !ctx.response.IsProfitable {
		return fmt.Errorf("expected contract to be profitable but it is not")
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theContractShouldNotBeProfitable() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if ctx.response.IsProfitable {
		return fmt.Errorf("expected contract to not be profitable but it is")
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) tripsRequiredShouldBe(expectedTrips int) error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if ctx.response.TripsRequired != expectedTrips {
		return fmt.Errorf("expected %d trips but got %d", expectedTrips, ctx.response.TripsRequired)
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) thePurchaseCostShouldBeCredits(expectedCost int) error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if ctx.response.PurchaseCost != expectedCost {
		return fmt.Errorf("expected purchase cost %d but got %d", expectedCost, ctx.response.PurchaseCost)
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) theReasonShouldContain(expectedReason string) error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("expected response but got nil")
	}
	if !strings.Contains(ctx.response.Reason, expectedReason) {
		return fmt.Errorf("expected reason to contain '%s' but got '%s'", expectedReason, ctx.response.Reason)
	}
	return nil
}

func (ctx *evaluateContractProfitabilityContext) iShouldGetAnErrorContaining(expectedError string) error {
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

// Helper functions

func strPtr(s string) *string {
	return &s
}

// testShipRepository is a simple in-memory ship repository for testing
type testShipRepository struct {
	ship *navigation.Ship
}

func newTestShipRepository(ship *navigation.Ship) *testShipRepository {
	return &testShipRepository{ship: ship}
}

func (r *testShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID int) (*navigation.Ship, error) {
	if r.ship == nil || r.ship.ShipSymbol() != symbol {
		return nil, fmt.Errorf("ship not found")
	}
	return r.ship, nil
}

func (r *testShipRepository) FindAllByPlayer(ctx context.Context, playerID int) ([]*navigation.Ship, error) {
	if r.ship != nil && r.ship.PlayerID() == playerID {
		return []*navigation.Ship{r.ship}, nil
	}
	return []*navigation.Ship{}, nil
}

func (r *testShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	return nil // No-op for tests
}

func (r *testShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID int) (*navigation.NavigationResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *testShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID int) error {
	return fmt.Errorf("not implemented")
}

func (r *testShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID int) error {
	return fmt.Errorf("not implemented")
}

func (r *testShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID int, units *int) error {
	return fmt.Errorf("not implemented")
}

func (r *testShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID int, mode string) error {
	return fmt.Errorf("not implemented")
}

func (r *testShipRepository) JettisonCargo(ctx context.Context, ship *navigation.Ship, playerID int, goodSymbol string, units int) error {
	return fmt.Errorf("not implemented")
}

func (r *testShipRepository) GetShipData(ctx context.Context, symbol string, playerID int) (*navigation.ShipData, error) {
	return nil, fmt.Errorf("not implemented")
}

// marketRepositoryAdapter adapts persistence.MarketRepositoryGORM to trading.MarketRepository
type marketRepositoryAdapter struct {
	repo *persistence.MarketRepositoryGORM
}

func newMarketRepositoryAdapter(repo *persistence.MarketRepositoryGORM) *marketRepositoryAdapter {
	return &marketRepositoryAdapter{repo: repo}
}

func (a *marketRepositoryAdapter) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*trading.Market, error) {
	// Not needed for this handler, but must implement interface
	return nil, fmt.Errorf("not implemented")
}

func (a *marketRepositoryAdapter) FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*trading.CheapestMarketResult, error) {
	return a.repo.FindCheapestMarketSelling(ctx, goodSymbol, systemSymbol, playerID)
}

// Register steps

func InitializeEvaluateContractProfitabilityScenario(ctx *godog.ScenarioContext) {
	evalCtx := &evaluateContractProfitabilityContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		evalCtx.reset()
		return ctx, nil
	})

	// Background steps
	ctx.Step(`^a system "([^"]*)"$`, evalCtx.aSystem)

	// Given steps
	ctx.Step(`^a contract paying (\d+) credits on acceptance and (\d+) on fulfillment$`, evalCtx.aContractPayingCreditsOnAcceptanceAndOnFulfillment)
	ctx.Step(`^the contract requires delivery of (\d+) "([^"]*)" to "([^"]*)"$`, evalCtx.theContractRequiresDeliveryOfTo)
	ctx.Step(`^the contract requires delivery of (\d+) "([^"]*)" to "([^"]*)" with (\d+) units already fulfilled$`, evalCtx.theContractRequiresDeliveryOfToWithUnitsAlreadyFulfilled)
	ctx.Step(`^the cheapest market sells "([^"]*)" at (\d+) credits per unit in system "([^"]*)"$`, evalCtx.theCheapestMarketSellsAtCreditsPerUnitInSystem)
	ctx.Step(`^no market sells "([^"]*)" in system "([^"]*)"$`, evalCtx.noMarketSellsInSystem)
	ctx.Step(`^a ship with (\d+) cargo capacity$`, evalCtx.aShipWithCargoCapacity)
	ctx.Step(`^fuel cost is (\d+) credits per trip$`, evalCtx.fuelCostIsCreditsPerTrip)

	// When steps
	ctx.Step(`^I evaluate the contract profitability$`, evalCtx.iEvaluateTheContractProfitability)
	ctx.Step(`^I try to evaluate the contract profitability$`, evalCtx.iTryToEvaluateTheContractProfitability)
	ctx.Step(`^I try to evaluate the contract profitability with an invalid ship$`, evalCtx.iTryToEvaluateTheContractProfitabilityWithAnInvalidShip)

	// Then steps
	ctx.Step(`^the net profit should be (-?\d+) credits$`, evalCtx.theNetProfitShouldBeCredits)
	ctx.Step(`^the contract should be profitable$`, evalCtx.theContractShouldBeProfitable)
	ctx.Step(`^the contract should not be profitable$`, evalCtx.theContractShouldNotBeProfitable)
	ctx.Step(`^trips required should be (\d+)$`, evalCtx.tripsRequiredShouldBe)
	ctx.Step(`^the purchase cost should be (\d+) credits$`, evalCtx.thePurchaseCostShouldBeCredits)
	ctx.Step(`^the reason should contain "([^"]*)"$`, evalCtx.theReasonShouldContain)
	ctx.Step(`^I should get an error containing "([^"]*)"$`, evalCtx.iShouldGetAnErrorContaining)
}
