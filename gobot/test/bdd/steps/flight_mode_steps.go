package steps

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type flightModeContext struct {
	flightMode   shared.FlightMode
	stringRep    string
	isValidName  bool
	parseErr     error
	parsedMode   shared.FlightMode
}

func (fmc *flightModeContext) reset() {
	fmc.flightMode = shared.FlightModeCruise
	fmc.stringRep = ""
	fmc.isValidName = false
	fmc.parseErr = nil
	fmc.parsedMode = shared.FlightModeCruise
}

// When steps

func (fmc *flightModeContext) iGetTheStringRepresentationOfFlightMode(modeName string) error {
	switch modeName {
	case "CRUISE":
		fmc.flightMode = shared.FlightModeCruise
	case "DRIFT":
		fmc.flightMode = shared.FlightModeDrift
	case "BURN":
		fmc.flightMode = shared.FlightModeBurn
	case "STEALTH":
		fmc.flightMode = shared.FlightModeStealth
	default:
		return fmt.Errorf("unknown flight mode: %s", modeName)
	}
	fmc.stringRep = fmc.flightMode.String()
	return nil
}

func (fmc *flightModeContext) iCheckIfIsAValidFlightModeName(name string) error {
	fmc.isValidName = shared.IsValidFlightModeName(name)
	return nil
}

func (fmc *flightModeContext) iParseFlightModeFromString(modeStr string) error {
	mode, err := shared.ParseFlightMode(modeStr)
	fmc.parsedMode = mode
	fmc.parseErr = err
	return nil
}

// Then steps

func (fmc *flightModeContext) theFlightModeStringShouldBe(expected string) error {
	if fmc.stringRep != expected {
		return fmt.Errorf("expected string '%s', got '%s'", expected, fmc.stringRep)
	}
	return nil
}

func (fmc *flightModeContext) theNameShouldBeValid() error {
	if !fmc.isValidName {
		return fmt.Errorf("expected name to be valid, but it was not")
	}
	return nil
}

func (fmc *flightModeContext) theNameShouldNotBeValid() error {
	if fmc.isValidName {
		return fmt.Errorf("expected name not to be valid, but it was")
	}
	return nil
}

func (fmc *flightModeContext) parsingShouldSucceed() error {
	if fmc.parseErr != nil {
		return fmt.Errorf("expected parsing to succeed, but got error: %v", fmc.parseErr)
	}
	return nil
}

func (fmc *flightModeContext) theParsedFlightModeShouldBe(expected string) error {
	var expectedMode shared.FlightMode
	switch expected {
	case "CRUISE":
		expectedMode = shared.FlightModeCruise
	case "DRIFT":
		expectedMode = shared.FlightModeDrift
	case "BURN":
		expectedMode = shared.FlightModeBurn
	case "STEALTH":
		expectedMode = shared.FlightModeStealth
	default:
		return fmt.Errorf("unknown expected flight mode: %s", expected)
	}

	if fmc.parsedMode != expectedMode {
		return fmt.Errorf("expected parsed mode %s, got %s", expected, fmc.parsedMode.String())
	}
	return nil
}

func (fmc *flightModeContext) parsingShouldFailWithError(expectedError string) error {
	if fmc.parseErr == nil {
		return fmt.Errorf("expected parsing to fail with '%s', but it succeeded", expectedError)
	}
	if fmc.parseErr.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, fmc.parseErr.Error())
	}
	return nil
}

func InitializeFlightModeScenario(ctx *godog.ScenarioContext) {
	fmc := &flightModeContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		fmc.reset()
		return ctx, nil
	})

	// When steps
	ctx.Step(`^I get the string representation of ([A-Z]+) flight mode$`, fmc.iGetTheStringRepresentationOfFlightMode)
	ctx.Step(`^I check if "([^"]*)" is a valid flight mode name$`, fmc.iCheckIfIsAValidFlightModeName)
	ctx.Step(`^I parse flight mode from string "([^"]*)"$`, fmc.iParseFlightModeFromString)

	// Then steps
	ctx.Step(`^the flight mode string should be "([^"]*)"$`, fmc.theFlightModeStringShouldBe)
	ctx.Step(`^the name should be valid$`, fmc.theNameShouldBeValid)
	ctx.Step(`^the name should not be valid$`, fmc.theNameShouldNotBeValid)
	ctx.Step(`^parsing should succeed$`, fmc.parsingShouldSucceed)
	ctx.Step(`^the parsed flight mode should be ([A-Z]+)$`, fmc.theParsedFlightModeShouldBe)
	ctx.Step(`^parsing should fail with error "([^"]*)"$`, fmc.parsingShouldFailWithError)
}
