package steps

import (
	"fmt"
	"strconv"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type contractContext struct {
	contractID         string
	playerID           int
	faction            string
	contractType       string
	deliveries         []contract.Delivery
	payment            contract.Payment
	deadlineToAccept   string
	deadline           string
	contract           *contract.Contract
	err                error
	boolResult         bool
	profitabilityEval  *contract.ProfitabilityEvaluation
	profitabilityCtx   contract.ProfitabilityContext
	marketPricesMap    map[string]int
}

func (cc *contractContext) reset() {
	cc.contractID = ""
	cc.playerID = 0
	cc.faction = ""
	cc.contractType = ""
	cc.deliveries = nil
	cc.payment = contract.Payment{}
	cc.deadlineToAccept = ""
	cc.deadline = ""
	cc.contract = nil
	cc.err = nil
	cc.boolResult = false
	cc.profitabilityEval = nil
	cc.profitabilityCtx = contract.ProfitabilityContext{}
	cc.marketPricesMap = make(map[string]int)
}

// Contract setup steps

func (cc *contractContext) aContractWith(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		cc.contractID = row.Cells[0].Value
		fmt.Sscanf(row.Cells[1].Value, "%d", &cc.playerID)
		cc.faction = row.Cells[2].Value
		cc.contractType = row.Cells[3].Value
	}
	return nil
}

func (cc *contractContext) contractDeliveries(table *godog.Table) error {
	cc.deliveries = make([]contract.Delivery, 0)
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		var unitsRequired, unitsFulfilled int
		fmt.Sscanf(row.Cells[2].Value, "%d", &unitsRequired)
		fmt.Sscanf(row.Cells[3].Value, "%d", &unitsFulfilled)

		cc.deliveries = append(cc.deliveries, contract.Delivery{
			TradeSymbol:       row.Cells[0].Value,
			DestinationSymbol: row.Cells[1].Value,
			UnitsRequired:     unitsRequired,
			UnitsFulfilled:    unitsFulfilled,
		})
	}
	return nil
}

func (cc *contractContext) contractPayment(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		var onAccepted, onFulfilled int
		fmt.Sscanf(row.Cells[0].Value, "%d", &onAccepted)
		fmt.Sscanf(row.Cells[1].Value, "%d", &onFulfilled)
		cc.payment = contract.Payment{
			OnAccepted:  onAccepted,
			OnFulfilled: onFulfilled,
		}
	}

	// If contract already exists, recreate it with the new payment
	if cc.contract != nil {
		wasAccepted := cc.contract.Accepted()
		cc.contract = nil
		if err := cc.iCreateTheContract(); err != nil {
			return err
		}
		if wasAccepted {
			return cc.contract.Accept()
		}
	}

	return nil
}

func (cc *contractContext) contractDeadlines(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		cc.deadlineToAccept = row.Cells[0].Value
		cc.deadline = row.Cells[1].Value
	}
	return nil
}

func (cc *contractContext) theContractHasDeliveries(table *godog.Table) error {
	if err := cc.contractDeliveries(table); err != nil {
		return err
	}

	// If contract already exists, recreate it with the new deliveries
	if cc.contract != nil {
		wasAccepted := cc.contract.Accepted()
		cc.contract = nil
		if err := cc.iCreateTheContract(); err != nil {
			return err
		}
		if wasAccepted {
			return cc.contract.Accept()
		}
	}

	return nil
}

// Contract creation steps

func (cc *contractContext) iCreateTheContract() error {
	terms := contract.Terms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: cc.deadlineToAccept,
		Deadline:         cc.deadline,
	}

	// For testing invalid player IDs (0 or negative), we need to pass the zero value PlayerID
	// to let the Contract's domain validation handle it with the proper error message
	var playerIDValue shared.PlayerID
	if cc.playerID > 0 {
		var err error
		playerIDValue, err = shared.NewPlayerID(cc.playerID)
		if err != nil {
			cc.err = err
			return nil
		}
	}
	// If cc.playerID <= 0, playerIDValue remains zero value and Contract validation will catch it

	cc.contract, cc.err = contract.NewContract(
		cc.contractID,
		playerIDValue,
		cc.faction,
		cc.contractType,
		terms,
		nil, // Use default RealClock
	)
	return nil
}

func (cc *contractContext) iAttemptToCreateTheContract() error {
	return cc.iCreateTheContract()
}

func (cc *contractContext) iAttemptToCreateTheContractWithNoDeliveries() error {
	cc.deliveries = []contract.Delivery{}
	return cc.iCreateTheContract()
}

// State setup steps

func (cc *contractContext) aValidUnacceptedContract() error {
	cc.contractID = "CONTRACT-1"
	cc.playerID = 1
	cc.faction = "COMMERCE"
	cc.contractType = "PROCUREMENT"

	// Only set default deliveries if none were provided via table
	if len(cc.deliveries) == 0 {
		cc.deliveries = []contract.Delivery{
			{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-MARKET",
				UnitsRequired:     100,
				UnitsFulfilled:    0,
			},
		}
	}

	// Only set default payment if not already set
	if cc.payment.OnAccepted == 0 && cc.payment.OnFulfilled == 0 {
		cc.payment = contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		}
	}

	// Only set default deadlines if not already set
	if cc.deadline == "" {
		cc.deadline = "2099-12-31T23:59:59Z"
		cc.deadlineToAccept = "2099-11-30T23:59:59Z"
	}

	return cc.iCreateTheContract()
}

func (cc *contractContext) aValidAcceptedContract() error {
	if err := cc.aValidUnacceptedContract(); err != nil {
		return err
	}
	return cc.contract.Accept()
}

func (cc *contractContext) aValidFulfilledContract() error {
	if err := cc.aValidAcceptedContract(); err != nil {
		return err
	}
	// Fulfill all deliveries
	for i := range cc.deliveries {
		cc.contract.Terms().Deliveries[i].UnitsFulfilled = cc.contract.Terms().Deliveries[i].UnitsRequired
	}
	return cc.contract.Fulfill()
}

func (cc *contractContext) aValidUnacceptedContractWithDelivery(table *godog.Table) error {
	if err := cc.contractDeliveries(table); err != nil {
		return err
	}
	return cc.aValidUnacceptedContract()
}

func (cc *contractContext) aValidAcceptedContractWithDelivery(table *godog.Table) error {
	if err := cc.contractDeliveries(table); err != nil {
		return err
	}
	if err := cc.aValidUnacceptedContract(); err != nil {
		return err
	}
	return cc.contract.Accept()
}

func (cc *contractContext) aContractWithDeadline(deadline string) error {
	cc.deadline = deadline
	return cc.aValidUnacceptedContract()
}

// Contract action steps

func (cc *contractContext) iAcceptTheContract() error {
	cc.err = cc.contract.Accept()
	return nil
}

func (cc *contractContext) iAttemptToAcceptTheContract() error {
	return cc.iAcceptTheContract()
}

func (cc *contractContext) iDeliverUnitsOf(units int, tradeSymbol string) error {
	cc.err = cc.contract.DeliverCargo(tradeSymbol, units)
	return nil
}

func (cc *contractContext) iAttemptToDeliverUnitsOf(units int, tradeSymbol string) error {
	return cc.iDeliverUnitsOf(units, tradeSymbol)
}

func (cc *contractContext) iCheckIfContractCanBeFulfilled() error {
	cc.boolResult = cc.contract.CanFulfill()
	return nil
}

func (cc *contractContext) iFulfillTheContract() error {
	cc.err = cc.contract.Fulfill()
	return nil
}

func (cc *contractContext) iAttemptToFulfillTheContract() error {
	return cc.iFulfillTheContract()
}

func (cc *contractContext) iCheckIfContractIsExpired() error {
	cc.boolResult = cc.contract.IsExpired()
	return nil
}

// Profitability steps

func (cc *contractContext) marketPrices(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		tradeSymbol := row.Cells[0].Value
		var sellPrice int
		fmt.Sscanf(row.Cells[1].Value, "%d", &sellPrice)
		cc.marketPricesMap[tradeSymbol] = sellPrice
	}
	cc.profitabilityCtx.MarketPrices = cc.marketPricesMap
	return nil
}

func (cc *contractContext) profitabilityContext(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		var cargoCapacity, fuelCost int
		fmt.Sscanf(row.Cells[0].Value, "%d", &cargoCapacity)
		fmt.Sscanf(row.Cells[1].Value, "%d", &fuelCost)
		cc.profitabilityCtx.CargoCapacity = cargoCapacity
		cc.profitabilityCtx.FuelCostPerTrip = fuelCost
		cc.profitabilityCtx.CheapestMarketWaypoint = row.Cells[2].Value
	}
	return nil
}

func (cc *contractContext) iEvaluateProfitability() error {
	cc.profitabilityEval, cc.err = cc.contract.EvaluateProfitability(cc.profitabilityCtx)
	return nil
}

func (cc *contractContext) iAttemptToEvaluateProfitability() error {
	return cc.iEvaluateProfitability()
}

// Assertion steps

func (cc *contractContext) theContractShouldBeValid() error {
	if cc.err != nil {
		return fmt.Errorf("expected contract to be valid, got error: %s", cc.err)
	}
	if cc.contract == nil {
		return fmt.Errorf("expected contract to be created, got nil")
	}
	return nil
}

func (cc *contractContext) contractCreationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error '%s', but contract creation succeeded", expectedError)
	}
	if cc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, cc.err.Error())
	}
	return nil
}

func (cc *contractContext) theContractOperationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error '%s', but operation succeeded", expectedError)
	}
	if cc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, cc.err.Error())
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeAccepted() error {
	if cc.contract.Accepted() {
		return fmt.Errorf("expected contract to not be accepted")
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeFulfilled() error {
	if cc.contract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled")
	}
	return nil
}

func (cc *contractContext) theContractShouldHaveDeliveries(count int) error {
	actual := len(cc.contract.Terms().Deliveries)
	if actual != count {
		return fmt.Errorf("expected %d deliveries, got %d", count, actual)
	}
	return nil
}

func (cc *contractContext) theContractShouldBeAccepted() error {
	if !cc.contract.Accepted() {
		return fmt.Errorf("expected contract to be accepted")
	}
	return nil
}

func (cc *contractContext) theDeliveryShouldShowUnitsFulfilled(units int) error {
	if len(cc.contract.Terms().Deliveries) == 0 {
		return fmt.Errorf("contract has no deliveries")
	}
	actual := cc.contract.Terms().Deliveries[0].UnitsFulfilled
	if actual != units {
		return fmt.Errorf("expected %d units fulfilled, got %d", units, actual)
	}
	return nil
}

func (cc *contractContext) theDeliveryForShouldShowUnitsFulfilled(tradeSymbol string, units int) error {
	for _, delivery := range cc.contract.Terms().Deliveries {
		if delivery.TradeSymbol == tradeSymbol {
			if delivery.UnitsFulfilled != units {
				return fmt.Errorf("expected %d units fulfilled for %s, got %d", units, tradeSymbol, delivery.UnitsFulfilled)
			}
			return nil
		}
	}
	return fmt.Errorf("trade symbol %s not found in deliveries", tradeSymbol)
}

func (cc *contractContext) theContractCannotBeFulfilled() error {
	if cc.boolResult {
		return fmt.Errorf("expected contract to not be fulfillable")
	}
	return nil
}

func (cc *contractContext) theContractCanBeFulfilled() error {
	if !cc.boolResult {
		return fmt.Errorf("expected contract to be fulfillable")
	}
	return nil
}

func (cc *contractContext) theContractShouldBeFulfilled() error {
	if !cc.contract.Fulfilled() {
		return fmt.Errorf("expected contract to be fulfilled")
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeExpired() error {
	if cc.boolResult {
		return fmt.Errorf("expected contract to not be expired")
	}
	return nil
}

func (cc *contractContext) theContractShouldBeExpired() error {
	if !cc.boolResult {
		return fmt.Errorf("expected contract to be expired")
	}
	return nil
}

func (cc *contractContext) theContractShouldBeProfitable() error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if !cc.profitabilityEval.IsProfitable {
		return fmt.Errorf("expected contract to be profitable")
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeProfitable() error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.IsProfitable {
		return fmt.Errorf("expected contract to not be profitable")
	}
	return nil
}

func (cc *contractContext) netProfitShouldBe(expected int) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.NetProfit != expected {
		return fmt.Errorf("expected net profit %d, got %d", expected, cc.profitabilityEval.NetProfit)
	}
	return nil
}

func (cc *contractContext) totalPaymentShouldBe(expected int) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.TotalPayment != expected {
		return fmt.Errorf("expected total payment %d, got %d", expected, cc.profitabilityEval.TotalPayment)
	}
	return nil
}

func (cc *contractContext) purchaseCostShouldBe(expected int) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.PurchaseCost != expected {
		return fmt.Errorf("expected purchase cost %d, got %d", expected, cc.profitabilityEval.PurchaseCost)
	}
	return nil
}

func (cc *contractContext) fuelCostShouldBe(expected int) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.FuelCost != expected {
		return fmt.Errorf("expected fuel cost %d, got %d", expected, cc.profitabilityEval.FuelCost)
	}
	return nil
}

func (cc *contractContext) tripsRequiredShouldBe(expected int) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.TripsRequired != expected {
		return fmt.Errorf("expected trips required %d, got %d", expected, cc.profitabilityEval.TripsRequired)
	}
	return nil
}

func (cc *contractContext) profitabilityReasonShouldBe(expected string) error {
	if cc.profitabilityEval == nil {
		return fmt.Errorf("profitability evaluation not performed")
	}
	if cc.profitabilityEval.Reason != expected {
		return fmt.Errorf("expected reason '%s', got '%s'", expected, cc.profitabilityEval.Reason)
	}
	return nil
}

func (cc *contractContext) theProfitabilityEvaluationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error '%s', but evaluation succeeded", expectedError)
	}
	if cc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, cc.err.Error())
	}
	return nil
}

// RegisterContractSteps registers all contract step definitions
func RegisterContractSteps(sc *godog.ScenarioContext) {
	ctx := &contractContext{
		marketPricesMap: make(map[string]int),
	}

	// Setup steps
	sc.Step(`^a contract with:$`, ctx.aContractWith)
	sc.Step(`^contract deliveries:$`, ctx.contractDeliveries)
	sc.Step(`^contract payment:$`, ctx.contractPayment)
	sc.Step(`^contract deadlines:$`, ctx.contractDeadlines)
	sc.Step(`^the contract has deliveries:$`, ctx.theContractHasDeliveries)
	sc.Step(`^a valid unaccepted contract$`, ctx.aValidUnacceptedContract)
	sc.Step(`^a valid accepted contract$`, ctx.aValidAcceptedContract)
	sc.Step(`^a valid fulfilled contract$`, ctx.aValidFulfilledContract)
	sc.Step(`^a valid unaccepted contract with delivery:$`, ctx.aValidUnacceptedContractWithDelivery)
	sc.Step(`^a valid accepted contract with delivery:$`, ctx.aValidAcceptedContractWithDelivery)
	sc.Step(`^a contract with deadline "([^"]*)"$`, ctx.aContractWithDeadline)

	// Creation steps
	sc.Step(`^I create the contract$`, ctx.iCreateTheContract)
	sc.Step(`^I attempt to create the contract$`, ctx.iAttemptToCreateTheContract)
	sc.Step(`^I attempt to create the contract with no deliveries$`, ctx.iAttemptToCreateTheContractWithNoDeliveries)

	// Action steps
	sc.Step(`^I accept the contract$`, ctx.iAcceptTheContract)
	sc.Step(`^I attempt to accept the contract$`, ctx.iAttemptToAcceptTheContract)
	sc.Step(`^I deliver (\d+) units of "([^"]*)"$`, ctx.iDeliverUnitsOf)
	sc.Step(`^I attempt to deliver (\d+) units of "([^"]*)"$`, ctx.iAttemptToDeliverUnitsOf)
	sc.Step(`^I check if contract can be fulfilled$`, ctx.iCheckIfContractCanBeFulfilled)
	sc.Step(`^I fulfill the contract$`, ctx.iFulfillTheContract)
	sc.Step(`^I attempt to fulfill the contract$`, ctx.iAttemptToFulfillTheContract)
	sc.Step(`^I check if contract is expired$`, ctx.iCheckIfContractIsExpired)

	// Profitability steps
	sc.Step(`^market prices:$`, ctx.marketPrices)
	sc.Step(`^profitability context:$`, ctx.profitabilityContext)
	sc.Step(`^I evaluate profitability$`, ctx.iEvaluateProfitability)
	sc.Step(`^I attempt to evaluate profitability$`, ctx.iAttemptToEvaluateProfitability)

	// Assertion steps
	sc.Step(`^the contract should be valid$`, ctx.theContractShouldBeValid)
	sc.Step(`^contract creation should fail with error "([^"]*)"$`, ctx.contractCreationShouldFailWithError)
	sc.Step(`^the contract operation should fail with error "([^"]*)"$`, ctx.theContractOperationShouldFailWithError)
	sc.Step(`^the contract should not be accepted$`, ctx.theContractShouldNotBeAccepted)
	sc.Step(`^the contract should not be fulfilled$`, ctx.theContractShouldNotBeFulfilled)
	sc.Step(`^the contract should have (\d+) deliveries$`, ctx.theContractShouldHaveDeliveries)
	sc.Step(`^the contract should be accepted$`, ctx.theContractShouldBeAccepted)
	sc.Step(`^the delivery should show (\d+) units fulfilled$`, ctx.theDeliveryShouldShowUnitsFulfilled)
	sc.Step(`^the delivery for "([^"]*)" should show (\d+) units fulfilled$`, ctx.theDeliveryForShouldShowUnitsFulfilled)
	sc.Step(`^the contract cannot be fulfilled$`, ctx.theContractCannotBeFulfilled)
	sc.Step(`^the contract can be fulfilled$`, ctx.theContractCanBeFulfilled)
	sc.Step(`^the contract should be fulfilled$`, ctx.theContractShouldBeFulfilled)
	sc.Step(`^the contract should not be expired$`, ctx.theContractShouldNotBeExpired)
	sc.Step(`^the contract should be expired$`, ctx.theContractShouldBeExpired)
	sc.Step(`^the contract should be profitable$`, ctx.theContractShouldBeProfitable)
	sc.Step(`^the contract should not be profitable$`, ctx.theContractShouldNotBeProfitable)
	sc.Step(`^net profit should be (-?\d+)$`, func(profitStr string) error {
		profit, _ := strconv.Atoi(profitStr)
		return ctx.netProfitShouldBe(profit)
	})
	sc.Step(`^total payment should be (\d+)$`, ctx.totalPaymentShouldBe)
	sc.Step(`^purchase cost should be (\d+)$`, ctx.purchaseCostShouldBe)
	sc.Step(`^fuel cost should be (\d+)$`, ctx.fuelCostShouldBe)
	sc.Step(`^trips required should be (\d+)$`, ctx.tripsRequiredShouldBe)
	sc.Step(`^profitability reason should be "([^"]*)"$`, ctx.profitabilityReasonShouldBe)
	sc.Step(`^the profitability evaluation should fail with error "([^"]*)"$`, ctx.theProfitabilityEvaluationShouldFailWithError)
}
