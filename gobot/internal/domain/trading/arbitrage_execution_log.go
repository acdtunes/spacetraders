package trading

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ArbitrageExecutionLog captures complete execution data for ML training.
// This is a pure data structure (no business logic) optimized for persistence and analysis.
//
// The log captures three time-synchronized snapshots:
//  1. Opportunity features at decision time
//  2. Ship state at decision time
//  3. Actual execution results
//
// Immutability: All fields are private with read-only getters.
type ArbitrageExecutionLog struct {
	// Execution metadata
	id          int
	containerID string
	shipSymbol  string
	playerID    shared.PlayerID
	executedAt  time.Time
	success     bool
	errorMsg    string

	// Opportunity features (snapshot at decision time)
	good            string
	buyMarket       string
	sellMarket      string
	buyPrice        int
	sellPrice       int
	profitMargin    float64
	distance        float64
	estimatedProfit int
	buySupply       string
	sellActivity    string
	currentScore    float64 // Score that led to this selection

	// Ship state (snapshot at decision time)
	cargoCapacity   int
	cargoUsed       int
	fuelCurrent     int
	fuelCapacity    int
	currentLocation string

	// Execution results (actual outcomes)
	actualNetProfit  int
	actualDuration   int // seconds
	fuelConsumed     int
	unitsPurchased   int
	unitsSold        int
	purchaseCost     int
	saleRevenue      int

	// Price drift tracking (three-stage price capture)
	buyPriceAtValidation  int // Buy market price when validated (SAFETY CHECK 3A)
	sellPriceAtValidation int // Sell market price when validated (SAFETY CHECK 3B)
	buyPriceActual        int // Actual price paid per unit during purchase
	sellPriceActual       int // Actual price received per unit during sale

	// Derived metrics
	profitPerSecond float64 // Target variable for ML
	profitPerUnit   float64
	marginAccuracy  float64 // How accurate was our estimate?
}

// ArbitrageExecutionResult holds the result data from an arbitrage execution
type ArbitrageExecutionResult struct {
	NetProfit             int
	DurationSeconds       int
	FuelCost              int
	UnitsPurchased        int
	UnitsSold             int
	PurchaseCost          int
	SaleRevenue           int
	BuyPriceAtValidation  int // Price at validation time
	SellPriceAtValidation int // Price at validation time
	BuyPriceActual        int // Price actually paid
	SellPriceActual       int // Price actually received
}

// NewArbitrageExecutionLog creates a log entry from opportunity, ship state, and result.
//
// This factory captures all three time-synchronized snapshots:
//  1. Opportunity features (from ArbitrageOpportunity)
//  2. Ship state (from Ship entity)
//  3. Execution results (from ArbitrageExecutionResult, may be nil if execution failed)
//
// Parameters:
//   - opportunity: The arbitrage opportunity that was selected
//   - ship: Ship state at decision time
//   - result: Execution outcome (nil if execution failed before completion)
//   - containerID: Container ID for correlation
//   - playerID: Player identifier
//   - success: Whether execution completed successfully
//   - errorMsg: Error message if execution failed (empty if success)
//
// Returns:
//   - ArbitrageExecutionLog with all fields populated
func NewArbitrageExecutionLog(
	opportunity *ArbitrageOpportunity,
	ship *navigation.Ship,
	result *ArbitrageExecutionResult,
	containerID string,
	playerID shared.PlayerID,
	success bool,
	errorMsg string,
) *ArbitrageExecutionLog {
	log := &ArbitrageExecutionLog{
		// Metadata
		containerID: containerID,
		shipSymbol:  ship.ShipSymbol(),
		playerID:    playerID,
		executedAt:  time.Now(),
		success:     success,
		errorMsg:    errorMsg,

		// Opportunity features
		good:            opportunity.Good(),
		buyMarket:       opportunity.BuyMarket().Symbol,
		sellMarket:      opportunity.SellMarket().Symbol,
		buyPrice:        opportunity.BuyPrice(),
		sellPrice:       opportunity.SellPrice(),
		profitMargin:    opportunity.ProfitMargin(),
		distance:        opportunity.Distance(),
		estimatedProfit: opportunity.EstimatedProfit(),
		buySupply:       opportunity.BuySupply(),
		sellActivity:    opportunity.SellActivity(),
		currentScore:    opportunity.Score(),

		// Ship state
		cargoCapacity:   ship.Cargo().Capacity,
		cargoUsed:       ship.Cargo().Units,
		fuelCurrent:     ship.Fuel().Current,
		fuelCapacity:    ship.Fuel().Capacity,
		currentLocation: ship.CurrentLocation().Symbol,
	}

	// Add results if available
	if result != nil {
		log.actualNetProfit = result.NetProfit
		log.actualDuration = result.DurationSeconds
		log.fuelConsumed = result.FuelCost
		log.unitsPurchased = result.UnitsPurchased
		log.unitsSold = result.UnitsSold
		log.purchaseCost = result.PurchaseCost
		log.saleRevenue = result.SaleRevenue
		log.buyPriceAtValidation = result.BuyPriceAtValidation
		log.sellPriceAtValidation = result.SellPriceAtValidation
		log.buyPriceActual = result.BuyPriceActual
		log.sellPriceActual = result.SellPriceActual

		// Compute derived metrics
		if log.actualDuration > 0 {
			log.profitPerSecond = float64(log.actualNetProfit) / float64(log.actualDuration)
		}
		if log.unitsSold > 0 {
			log.profitPerUnit = float64(log.actualNetProfit) / float64(log.unitsSold)
		}

		// Margin accuracy: actual vs. estimated
		if log.unitsSold > 0 && log.purchaseCost > 0 {
			actualMargin := float64(log.saleRevenue-log.purchaseCost) / float64(log.purchaseCost) * 100
			log.marginAccuracy = actualMargin - log.profitMargin
		}
	}

	return log
}

// ReconstructArbitrageExecutionLog reconstructs a log from database fields.
// This is used by the repository layer when loading from persistence.
func ReconstructArbitrageExecutionLog(
	id int,
	containerID string,
	shipSymbol string,
	playerID shared.PlayerID,
	executedAt time.Time,
	success bool,
	errorMsg string,
	good string,
	buyMarket string,
	sellMarket string,
	buyPrice int,
	sellPrice int,
	profitMargin float64,
	distance float64,
	estimatedProfit int,
	buySupply string,
	sellActivity string,
	currentScore float64,
	cargoCapacity int,
	cargoUsed int,
	fuelCurrent int,
	fuelCapacity int,
	currentLocation string,
	actualNetProfit int,
	actualDuration int,
	fuelConsumed int,
	unitsPurchased int,
	unitsSold int,
	purchaseCost int,
	saleRevenue int,
	buyPriceAtValidation int,
	sellPriceAtValidation int,
	buyPriceActual int,
	sellPriceActual int,
	profitPerSecond float64,
	profitPerUnit float64,
	marginAccuracy float64,
) *ArbitrageExecutionLog {
	return &ArbitrageExecutionLog{
		id:                    id,
		containerID:           containerID,
		shipSymbol:            shipSymbol,
		playerID:              playerID,
		executedAt:            executedAt,
		success:               success,
		errorMsg:              errorMsg,
		good:                  good,
		buyMarket:             buyMarket,
		sellMarket:            sellMarket,
		buyPrice:              buyPrice,
		sellPrice:             sellPrice,
		profitMargin:          profitMargin,
		distance:              distance,
		estimatedProfit:       estimatedProfit,
		buySupply:             buySupply,
		sellActivity:          sellActivity,
		currentScore:          currentScore,
		cargoCapacity:         cargoCapacity,
		cargoUsed:             cargoUsed,
		fuelCurrent:           fuelCurrent,
		fuelCapacity:          fuelCapacity,
		currentLocation:       currentLocation,
		actualNetProfit:       actualNetProfit,
		actualDuration:        actualDuration,
		fuelConsumed:          fuelConsumed,
		unitsPurchased:        unitsPurchased,
		unitsSold:             unitsSold,
		purchaseCost:          purchaseCost,
		saleRevenue:           saleRevenue,
		buyPriceAtValidation:  buyPriceAtValidation,
		sellPriceAtValidation: sellPriceAtValidation,
		buyPriceActual:        buyPriceActual,
		sellPriceActual:       sellPriceActual,
		profitPerSecond:       profitPerSecond,
		profitPerUnit:         profitPerUnit,
		marginAccuracy:        marginAccuracy,
	}
}

// Getters - provide read-only access to maintain immutability

func (l *ArbitrageExecutionLog) ID() int {
	return l.id
}

func (l *ArbitrageExecutionLog) ContainerID() string {
	return l.containerID
}

func (l *ArbitrageExecutionLog) ShipSymbol() string {
	return l.shipSymbol
}

func (l *ArbitrageExecutionLog) PlayerID() shared.PlayerID {
	return l.playerID
}

func (l *ArbitrageExecutionLog) ExecutedAt() time.Time {
	return l.executedAt
}

func (l *ArbitrageExecutionLog) Success() bool {
	return l.success
}

func (l *ArbitrageExecutionLog) ErrorMsg() string {
	return l.errorMsg
}

func (l *ArbitrageExecutionLog) Good() string {
	return l.good
}

func (l *ArbitrageExecutionLog) BuyMarket() string {
	return l.buyMarket
}

func (l *ArbitrageExecutionLog) SellMarket() string {
	return l.sellMarket
}

func (l *ArbitrageExecutionLog) BuyPrice() int {
	return l.buyPrice
}

func (l *ArbitrageExecutionLog) SellPrice() int {
	return l.sellPrice
}

func (l *ArbitrageExecutionLog) ProfitMargin() float64 {
	return l.profitMargin
}

func (l *ArbitrageExecutionLog) Distance() float64 {
	return l.distance
}

func (l *ArbitrageExecutionLog) EstimatedProfit() int {
	return l.estimatedProfit
}

func (l *ArbitrageExecutionLog) BuySupply() string {
	return l.buySupply
}

func (l *ArbitrageExecutionLog) SellActivity() string {
	return l.sellActivity
}

func (l *ArbitrageExecutionLog) CurrentScore() float64 {
	return l.currentScore
}

func (l *ArbitrageExecutionLog) CargoCapacity() int {
	return l.cargoCapacity
}

func (l *ArbitrageExecutionLog) CargoUsed() int {
	return l.cargoUsed
}

func (l *ArbitrageExecutionLog) FuelCurrent() int {
	return l.fuelCurrent
}

func (l *ArbitrageExecutionLog) FuelCapacity() int {
	return l.fuelCapacity
}

func (l *ArbitrageExecutionLog) CurrentLocation() string {
	return l.currentLocation
}

func (l *ArbitrageExecutionLog) ActualNetProfit() int {
	return l.actualNetProfit
}

func (l *ArbitrageExecutionLog) ActualDuration() int {
	return l.actualDuration
}

func (l *ArbitrageExecutionLog) FuelConsumed() int {
	return l.fuelConsumed
}

func (l *ArbitrageExecutionLog) UnitsPurchased() int {
	return l.unitsPurchased
}

func (l *ArbitrageExecutionLog) UnitsSold() int {
	return l.unitsSold
}

func (l *ArbitrageExecutionLog) PurchaseCost() int {
	return l.purchaseCost
}

func (l *ArbitrageExecutionLog) SaleRevenue() int {
	return l.saleRevenue
}

func (l *ArbitrageExecutionLog) BuyPriceAtValidation() int {
	return l.buyPriceAtValidation
}

func (l *ArbitrageExecutionLog) SellPriceAtValidation() int {
	return l.sellPriceAtValidation
}

func (l *ArbitrageExecutionLog) BuyPriceActual() int {
	return l.buyPriceActual
}

func (l *ArbitrageExecutionLog) SellPriceActual() int {
	return l.sellPriceActual
}

func (l *ArbitrageExecutionLog) ProfitPerSecond() float64 {
	return l.profitPerSecond
}

func (l *ArbitrageExecutionLog) ProfitPerUnit() float64 {
	return l.profitPerUnit
}

func (l *ArbitrageExecutionLog) MarginAccuracy() float64 {
	return l.marginAccuracy
}
