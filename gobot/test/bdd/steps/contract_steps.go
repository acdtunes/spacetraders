package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/cucumber/godog"
)

type contractContext struct {
	contractID    string
	playerID      int
	factionSymbol string
	contractType  string
	payment       contract.Payment
	deliveries    []contract.Delivery
	contract      *contract.Contract
	err           error
	boolResult    bool
}

func (cc *contractContext) reset() {
	cc.contractID = ""
	cc.playerID = 0
	cc.factionSymbol = ""
	cc.contractType = ""
	cc.payment = contract.Payment{}
	cc.deliveries = []contract.Delivery{}
	cc.contract = nil
	cc.err = nil
	cc.boolResult = false
}

// Given steps

func (cc *contractContext) aContractWithIDForPlayer(contractID string, playerID int) error {
	cc.contractID = contractID
	cc.playerID = playerID
	return nil
}

func (cc *contractContext) theContractHasFaction(factionSymbol string) error {
	cc.factionSymbol = factionSymbol
	return nil
}

func (cc *contractContext) theContractTypeIs(contractType string) error {
	cc.contractType = contractType
	return nil
}

func (cc *contractContext) paymentIsCreditsOnAcceptanceAndOnFulfillment(onAccepted, onFulfilled int) error {
	cc.payment = contract.Payment{
		OnAccepted:  onAccepted,
		OnFulfilled: onFulfilled,
	}
	return nil
}

func (cc *contractContext) aDeliveryOfUnitsOfToIsRequired(units int, tradeSymbol, destination string) error {
	cc.deliveries = append(cc.deliveries, contract.Delivery{
		TradeSymbol:       tradeSymbol,
		DestinationSymbol: destination,
		UnitsRequired:     units,
		UnitsFulfilled:    0,
	})
	return nil
}

func (cc *contractContext) aValidUnacceptedContract() error {
	cc.contractID = "contract-1"
	cc.playerID = 1
	cc.factionSymbol = "COSMIC"
	cc.contractType = "PROCUREMENT"
	cc.payment = contract.Payment{OnAccepted: 1000, OnFulfilled: 5000}
	cc.deliveries = []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    0,
		},
	}

	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		return err
	}
	cc.contract = c
	return nil
}

func (cc *contractContext) aValidAcceptedContract() error {
	if err := cc.aValidUnacceptedContract(); err != nil {
		return err
	}
	return cc.contract.Accept()
}

func (cc *contractContext) aValidAcceptedContractWithDeliveryOfTo(units int, tradeSymbol, destination string) error {
	cc.contractID = "contract-1"
	cc.playerID = 1
	cc.factionSymbol = "COSMIC"
	cc.contractType = "PROCUREMENT"
	cc.payment = contract.Payment{OnAccepted: 1000, OnFulfilled: 5000}
	cc.deliveries = []contract.Delivery{
		{
			TradeSymbol:       tradeSymbol,
			DestinationSymbol: destination,
			UnitsRequired:     units,
			UnitsFulfilled:    0,
		},
	}

	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		return err
	}
	cc.contract = c
	if err := cc.contract.Accept(); err != nil {
		return err
	}
	return nil
}

func (cc *contractContext) aContractWithOfUnitsAlreadyDelivered(fulfilled, required int, tradeSymbol string) error {
	cc.contractID = "contract-1"
	cc.playerID = 1
	cc.factionSymbol = "COSMIC"
	cc.contractType = "PROCUREMENT"
	cc.payment = contract.Payment{OnAccepted: 1000, OnFulfilled: 5000}
	cc.deliveries = []contract.Delivery{
		{
			TradeSymbol:       tradeSymbol,
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     required,
			UnitsFulfilled:    fulfilled,
		},
	}

	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		return err
	}
	cc.contract = c
	if err := cc.contract.Accept(); err != nil {
		return err
	}
	return nil
}

func (cc *contractContext) aContractWithAllDeliveriesFulfilled() error {
	cc.contractID = "contract-1"
	cc.playerID = 1
	cc.factionSymbol = "COSMIC"
	cc.contractType = "PROCUREMENT"
	cc.payment = contract.Payment{OnAccepted: 1000, OnFulfilled: 5000}
	cc.deliveries = []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    100,
		},
	}

	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		return err
	}
	cc.contract = c
	if err := cc.contract.Accept(); err != nil {
		return err
	}
	return nil
}

func (cc *contractContext) aContractWithIncompleteDeliveries() error {
	cc.contractID = "contract-1"
	cc.playerID = 1
	cc.factionSymbol = "COSMIC"
	cc.contractType = "PROCUREMENT"
	cc.payment = contract.Payment{OnAccepted: 1000, OnFulfilled: 5000}
	cc.deliveries = []contract.Delivery{
		{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-GZ7-A1",
			UnitsRequired:     100,
			UnitsFulfilled:    50,
		},
	}

	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		return err
	}
	cc.contract = c
	if err := cc.contract.Accept(); err != nil {
		return err
	}
	return nil
}

// When steps

func (cc *contractContext) iCreateTheContract() error {
	terms := contract.ContractTerms{
		Payment:          cc.payment,
		Deliveries:       cc.deliveries,
		DeadlineToAccept: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		Deadline:         time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}

	c, err := contract.NewContract(cc.contractID, cc.playerID, cc.factionSymbol, cc.contractType, terms)
	if err != nil {
		cc.err = err
		return nil
	}
	cc.contract = c
	return nil
}

func (cc *contractContext) iAcceptTheContract() error {
	cc.err = cc.contract.Accept()
	return nil
}

func (cc *contractContext) iTryToAcceptTheContract() error {
	cc.err = cc.contract.Accept()
	return nil
}

func (cc *contractContext) iDeliverUnitsOf(units int, tradeSymbol string) error {
	cc.err = cc.contract.DeliverCargo(tradeSymbol, units)
	return nil
}

func (cc *contractContext) iTryToDeliverUnitsOf(units int, tradeSymbol string) error {
	cc.err = cc.contract.DeliverCargo(tradeSymbol, units)
	return nil
}

func (cc *contractContext) iCheckIfContractCanBeFulfilled() error {
	cc.boolResult = cc.contract.CanFulfill()
	return nil
}

func (cc *contractContext) iFulfillTheContract() error {
	cc.err = cc.contract.Fulfill()
	return nil
}

func (cc *contractContext) iTryToFulfillTheContract() error {
	cc.err = cc.contract.Fulfill()
	return nil
}

// Then steps

func (cc *contractContext) theContractShouldBeCreatedSuccessfully() error {
	if cc.err != nil {
		return fmt.Errorf("expected no error, got: %v", cc.err)
	}
	if cc.contract == nil {
		return fmt.Errorf("expected contract to be created")
	}
	return nil
}

func (cc *contractContext) theContractIDShouldBe(expectedID string) error {
	if cc.contract.ContractID() != expectedID {
		return fmt.Errorf("expected contract ID %s, got %s", expectedID, cc.contract.ContractID())
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeAccepted() error {
	// Check negotiate contract shared variable first
	contract := cc.contract
	if contract == nil && sharedContractForNegotiate != nil {
		contract = sharedContractForNegotiate
	}
	if contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if contract.Accepted() {
		return fmt.Errorf("expected contract to not be accepted")
	}
	return nil
}

func (cc *contractContext) theContractShouldNotBeFulfilled() error {
	// Check negotiate contract shared variable first
	contract := cc.contract
	if contract == nil && sharedContractForNegotiate != nil {
		contract = sharedContractForNegotiate
	}
	if contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if contract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled")
	}
	return nil
}

func (cc *contractContext) theContractShouldBeAccepted() error {
	if !cc.contract.Accepted() {
		return fmt.Errorf("expected contract to be accepted")
	}
	return nil
}

func (cc *contractContext) iShouldGetAnError(expectedError string) error {
	// Check shared error first (used by other contexts like tradeGoodContext)
	actualErr := sharedErr
	if actualErr == nil {
		actualErr = cc.err
	}
	if actualErr == nil {
		return fmt.Errorf("expected error '%s', got nil", expectedError)
	}
	if !strings.Contains(actualErr.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s', got '%s'", expectedError, actualErr.Error())
	}
	return nil
}

func (cc *contractContext) theDeliveryShouldShowUnitsFulfilled(expectedFulfilled int) error {
	terms := cc.contract.Terms()
	if len(terms.Deliveries) == 0 {
		return fmt.Errorf("no deliveries found")
	}
	if terms.Deliveries[0].UnitsFulfilled != expectedFulfilled {
		return fmt.Errorf("expected %d units fulfilled, got %d", expectedFulfilled, terms.Deliveries[0].UnitsFulfilled)
	}
	return nil
}

func (cc *contractContext) theContractCanBeFulfilled() error {
	if !cc.boolResult {
		return fmt.Errorf("expected contract to be fulfillable")
	}
	return nil
}

func (cc *contractContext) theContractCannotBeFulfilled() error {
	if cc.boolResult {
		return fmt.Errorf("expected contract to not be fulfillable")
	}
	return nil
}

func (cc *contractContext) theContractShouldBeFulfilled() error {
	if !cc.contract.Fulfilled() {
		return fmt.Errorf("expected contract to be fulfilled")
	}
	return nil
}

// InitializeContractScenario registers contract steps
func InitializeContractScenario(ctx *godog.ScenarioContext) {
	cc := &contractContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		cc.reset()
		return ctx, nil
	})

	// Given steps
	ctx.Step(`^a contract with ID "([^"]*)" for player (\d+)$`, cc.aContractWithIDForPlayer)
	ctx.Step(`^the contract has faction "([^"]*)"$`, cc.theContractHasFaction)
	ctx.Step(`^the contract type is "([^"]*)"$`, cc.theContractTypeIs)
	ctx.Step(`^payment is (\d+) credits on acceptance and (\d+) on fulfillment$`, cc.paymentIsCreditsOnAcceptanceAndOnFulfillment)
	ctx.Step(`^a delivery of (\d+) units of "([^"]*)" to "([^"]*)" is required$`, cc.aDeliveryOfUnitsOfToIsRequired)
	ctx.Step(`^a valid unaccepted contract$`, cc.aValidUnacceptedContract)
	ctx.Step(`^a valid accepted contract$`, cc.aValidAcceptedContract)
	ctx.Step(`^a valid accepted contract with delivery of (\d+) "([^"]*)" to "([^"]*)"$`, cc.aValidAcceptedContractWithDeliveryOfTo)
	ctx.Step(`^a contract with (\d+) of (\d+) "([^"]*)" units already delivered$`, cc.aContractWithOfUnitsAlreadyDelivered)
	ctx.Step(`^a contract with all deliveries fulfilled$`, cc.aContractWithAllDeliveriesFulfilled)
	ctx.Step(`^a contract with incomplete deliveries$`, cc.aContractWithIncompleteDeliveries)

	// When steps
	ctx.Step(`^I create the contract$`, cc.iCreateTheContract)
	ctx.Step(`^I accept the contract$`, cc.iAcceptTheContract)
	ctx.Step(`^I try to accept the contract$`, cc.iTryToAcceptTheContract)
	ctx.Step(`^I deliver (\d+) units of "([^"]*)"$`, cc.iDeliverUnitsOf)
	ctx.Step(`^I try to deliver (\d+) units of "([^"]*)"$`, cc.iTryToDeliverUnitsOf)
	ctx.Step(`^I check if contract can be fulfilled$`, cc.iCheckIfContractCanBeFulfilled)
	ctx.Step(`^I fulfill the contract$`, cc.iFulfillTheContract)
	ctx.Step(`^I try to fulfill the contract$`, cc.iTryToFulfillTheContract)

	// Then steps
	ctx.Step(`^the contract should be created successfully$`, cc.theContractShouldBeCreatedSuccessfully)
	ctx.Step(`^the contract ID should be "([^"]*)"$`, cc.theContractIDShouldBe)
	ctx.Step(`^the contract should not be accepted$`, cc.theContractShouldNotBeAccepted)
	ctx.Step(`^the contract should not be fulfilled$`, cc.theContractShouldNotBeFulfilled)
	ctx.Step(`^the contract should be accepted$`, cc.theContractShouldBeAccepted)
	ctx.Step(`^I should get a contract error "([^"]*)"$`, cc.iShouldGetAnError)
	ctx.Step(`^the delivery should show (\d+) units fulfilled$`, cc.theDeliveryShouldShowUnitsFulfilled)
	ctx.Step(`^the contract can be fulfilled$`, cc.theContractCanBeFulfilled)
	ctx.Step(`^the contract cannot be fulfilled$`, cc.theContractCannotBeFulfilled)
	ctx.Step(`^the contract should be fulfilled$`, cc.theContractShouldBeFulfilled)
}
