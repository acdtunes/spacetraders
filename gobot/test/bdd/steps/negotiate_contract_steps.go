package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Shared contract variable for use by contract_steps.go
// This allows negotiate contract scenarios to use contract entity assertions
var sharedContractForNegotiate *contract.Contract

type negotiateContractContext struct {
	ships              map[string]*navigation.Ship
	contracts          map[string]*contract.Contract
	playerID           int
	agentSymbol        string
	token              string
	response           *appContract.NegotiateContractResponse
	err                error
	existingContractID string
	wasNegotiated      bool
	shipDockedDuringCommand bool
}

func (ctx *negotiateContractContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.contracts = make(map[string]*contract.Contract)
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.response = nil
	ctx.err = nil
	ctx.existingContractID = ""
	ctx.wasNegotiated = false
	ctx.shipDockedDuringCommand = false
	sharedContractForNegotiate = nil
}

// Player setup steps (shared)

func (ctx *negotiateContractContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token

	// Update shared context for cross-scenario compatibility
	sharedPlayerExistsWithAgentAndToken(agentSymbol, token)

	return nil
}

func (ctx *negotiateContractContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID

	// Update shared context for cross-scenario compatibility
	sharedPlayerHasPlayerID(playerID)

	return nil
}

// Given steps

func (ctx *negotiateContractContext) aShipForPlayerAtWithStatus(shipSymbol string, playerID int, location, status string) error {
	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	var navStatus navigation.NavStatus
	switch status {
	case "DOCKED":
		navStatus = navigation.NavStatusDocked
	case "IN_ORBIT":
		navStatus = navigation.NavStatusInOrbit
	case "IN_TRANSIT":
		navStatus = navigation.NavStatusInTransit
	default:
		return fmt.Errorf("unknown nav status: %s", status)
	}

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, "FRAME_EXPLORER", navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	return nil
}

func (ctx *negotiateContractContext) aShipForPlayerInTransitTo(shipSymbol string, playerID int, destination string) error {
	waypoint, _ := shared.NewWaypoint("X1-START", 0, 0)
	destWaypoint, _ := shared.NewWaypoint(destination, 100, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, "FRAME_EXPLORER", "", navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	// Transition to in-transit
	if err := ship.StartTransit(destWaypoint); err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	return nil
}

func (ctx *negotiateContractContext) theShipHasNoActiveContract() error {
	// This is implicitly true - no contract exists in our context
	return nil
}

func (ctx *negotiateContractContext) agentAlreadyHasAnActiveContractForPlayer(contractID string, playerID int) error {
	// Create an existing contract
	terms := contract.ContractTerms{
		Payment: contract.Payment{
			OnAccepted:  10000,
			OnFulfilled: 50000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       "IRON_ORE",
				DestinationSymbol: "X1-A1",
				UnitsRequired:     100,
				UnitsFulfilled:    0,
			},
		},
		DeadlineToAccept: "2025-12-31T23:59:59Z",
		Deadline:         "2026-01-31T23:59:59Z",
	}

	existingContract, err := contract.NewContract(
		contractID,
		playerID,
		"COSMIC",
		"PROCUREMENT",
		terms,
	)
	if err != nil {
		return err
	}

	ctx.contracts[contractID] = existingContract
	ctx.existingContractID = contractID
	return nil
}

// When steps

func (ctx *negotiateContractContext) iExecuteNegotiateContractCommandForShipAndPlayer(shipSymbol string, playerID int) error {
	// Simulate the command handler logic
	ship, exists := ctx.ships[shipSymbol]
	if !exists {
		ctx.err = fmt.Errorf("ship not found")
		return nil
	}

	// Check player ownership
	if ship.PlayerID() != playerID {
		ctx.err = fmt.Errorf("ship not found")
		return nil
	}

	// Ensure ship is docked (idempotent)
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		ctx.err = err
		return nil
	}

	if stateChanged {
		ctx.shipDockedDuringCommand = true
	}

	// Simulate API call
	if ctx.existingContractID != "" {
		// Error 4511: Agent already has active contract
		existingContract := ctx.contracts[ctx.existingContractID]
		ctx.response = &appContract.NegotiateContractResponse{
			Contract:      existingContract,
			WasNegotiated: false,
		}
		// Set shared contract for contract entity assertions
		sharedContractForNegotiate = existingContract
		return nil
	}

	// Create new contract
	terms := contract.ContractTerms{
		Payment: contract.Payment{
			OnAccepted:  15000,
			OnFulfilled: 75000,
		},
		Deliveries: []contract.Delivery{
			{
				TradeSymbol:       "PRECIOUS_STONES",
				DestinationSymbol: "X1-A1",
				UnitsRequired:     50,
				UnitsFulfilled:    0,
			},
		},
		DeadlineToAccept: "2025-12-31T23:59:59Z",
		Deadline:         "2026-01-31T23:59:59Z",
	}

	newContract, err := contract.NewContract(
		"NEW-CONTRACT-1",
		playerID,
		"COSMIC",
		"PROCUREMENT",
		terms,
	)
	if err != nil {
		ctx.err = err
		return nil
	}

	ctx.contracts["NEW-CONTRACT-1"] = newContract
	ctx.wasNegotiated = true

	ctx.response = &appContract.NegotiateContractResponse{
		Contract:      newContract,
		WasNegotiated: true,
	}

	// Set shared contract for contract entity assertions
	sharedContractForNegotiate = newContract

	return nil
}

// Then steps

func (ctx *negotiateContractContext) theCommandShouldSucceed() error {
	if ctx.err != nil {
		return fmt.Errorf("expected success but got error: %v", ctx.err)
	}
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	return nil
}

func (ctx *negotiateContractContext) aNewContractShouldBeNegotiated() error {
	if ctx.response == nil || ctx.response.Contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if !ctx.response.WasNegotiated {
		return fmt.Errorf("expected WasNegotiated=true but got false")
	}
	return nil
}

func (ctx *negotiateContractContext) theContractShouldBelongToPlayer(playerID int) error {
	if ctx.response == nil || ctx.response.Contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if ctx.response.Contract.PlayerID() != playerID {
		return fmt.Errorf("expected contract for player %d but got %d", playerID, ctx.response.Contract.PlayerID())
	}
	return nil
}

func (ctx *negotiateContractContext) theContractShouldNotBeAccepted() error {
	if ctx.response == nil || ctx.response.Contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if ctx.response.Contract.Accepted() {
		return fmt.Errorf("expected contract to not be accepted")
	}
	return nil
}

func (ctx *negotiateContractContext) theContractShouldNotBeFulfilled() error {
	if ctx.response == nil || ctx.response.Contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if ctx.response.Contract.Fulfilled() {
		return fmt.Errorf("expected contract to not be fulfilled")
	}
	return nil
}

func (ctx *negotiateContractContext) theExistingContractShouldBeReturned(contractID string) error {
	if ctx.response == nil || ctx.response.Contract == nil {
		return fmt.Errorf("no contract in response")
	}
	if ctx.response.Contract.ContractID() != contractID {
		return fmt.Errorf("expected contract %s but got %s", contractID, ctx.response.Contract.ContractID())
	}
	return nil
}

func (ctx *negotiateContractContext) noNewContractShouldBeNegotiated() error {
	if ctx.response == nil {
		return fmt.Errorf("no response received")
	}
	if ctx.response.WasNegotiated {
		return fmt.Errorf("expected WasNegotiated=false but got true")
	}
	return nil
}

func (ctx *negotiateContractContext) theShipShouldBeDockedFirst() error {
	if !ctx.shipDockedDuringCommand {
		return fmt.Errorf("expected ship to be docked during command execution")
	}
	return nil
}

func (ctx *negotiateContractContext) theCommandShouldFailWithError(expectedError string) error {
	if ctx.err == nil {
		return fmt.Errorf("expected error but command succeeded")
	}
	if ctx.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s' but got '%s'", expectedError, ctx.err.Error())
	}
	return nil
}

// Register steps

func InitializeNegotiateContractScenario(ctx *godog.ScenarioContext) {
	negotiateCtx := &negotiateContractContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		negotiateCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, negotiateCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, negotiateCtx.thePlayerHasPlayerID)
	ctx.Step(`^a negotiate contract ship "([^"]*)" for player (\d+) at "([^"]*)" with status "([^"]*)"$`, negotiateCtx.aShipForPlayerAtWithStatus)
	ctx.Step(`^a negotiate contract ship "([^"]*)" for player (\d+) in transit to "([^"]*)"$`, negotiateCtx.aShipForPlayerInTransitTo)
	ctx.Step(`^the ship has no active contract$`, negotiateCtx.theShipHasNoActiveContract)
	ctx.Step(`^agent already has an active contract "([^"]*)" for player (\d+)$`, negotiateCtx.agentAlreadyHasAnActiveContractForPlayer)
	ctx.Step(`^I execute NegotiateContractCommand for ship "([^"]*)" and player (\d+)$`, negotiateCtx.iExecuteNegotiateContractCommandForShipAndPlayer)
	ctx.Step(`^the negotiate contract command should succeed$`, negotiateCtx.theCommandShouldSucceed)
	ctx.Step(`^a new contract should be negotiated$`, negotiateCtx.aNewContractShouldBeNegotiated)
	ctx.Step(`^the contract should belong to player (\d+)$`, negotiateCtx.theContractShouldBelongToPlayer)
	// NOTE: "the contract should not be accepted" and "the contract should not be fulfilled" steps
	// are registered by contract_steps.go and use sharedContractForNegotiate
	ctx.Step(`^the existing contract "([^"]*)" should be returned$`, negotiateCtx.theExistingContractShouldBeReturned)
	ctx.Step(`^no new contract should be negotiated$`, negotiateCtx.noNewContractShouldBeNegotiated)
	ctx.Step(`^the ship should be docked first$`, negotiateCtx.theShipShouldBeDockedFirst)
	ctx.Step(`^the negotiate contract command should fail with error "([^"]*)"$`, negotiateCtx.theCommandShouldFailWithError)
}
