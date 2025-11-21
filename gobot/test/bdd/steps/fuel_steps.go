package steps

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type fuelContext struct {
	fuel       *shared.Fuel
	percentage float64
	isFull     bool
	canTravel  bool
	stringRep  string
	err        error
}

func (fc *fuelContext) reset() {
	fc.fuel = nil
	fc.percentage = 0
	fc.isFull = false
	fc.canTravel = false
	fc.stringRep = ""
	fc.err = nil
}

// Given steps

func (fc *fuelContext) aFuelValueObjectWithCurrentAndCapacity(current, capacity int) error {
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		return err
	}
	fc.fuel = fuel
	return nil
}

func (fc *fuelContext) iCreateFuelWithCurrentAndCapacity(current, capacity int) error {
	fuel, err := shared.NewFuel(current, capacity)
	fc.fuel = fuel
	fc.err = err
	return nil
}

// When steps

func (fc *fuelContext) iCalculateTheFuelPercentage() error {
	if fc.fuel == nil {
		return fmt.Errorf("no fuel object available")
	}
	fc.percentage = fc.fuel.Percentage()
	return nil
}

func (fc *fuelContext) iCheckIfFuelIsFull() error {
	if fc.fuel == nil {
		return fmt.Errorf("no fuel object available")
	}
	fc.isFull = fc.fuel.IsFull()
	return nil
}

func (fc *fuelContext) iGetTheFuelStringRepresentation() error {
	if fc.fuel == nil {
		return fmt.Errorf("no fuel object available")
	}
	fc.stringRep = fc.fuel.String()
	return nil
}

func (fc *fuelContext) iCheckIfFuelCanTravelUnitsWithSafetyMargin(units int, margin float64) error {
	if fc.fuel == nil {
		return fmt.Errorf("no fuel object available")
	}
	fc.canTravel = fc.fuel.CanTravel(units, margin)
	return nil
}

// Then steps

func (fc *fuelContext) theFuelPercentageShouldBe(expected float64) error {
	tolerance := 0.01
	if fc.percentage < expected-tolerance || fc.percentage > expected+tolerance {
		return fmt.Errorf("expected percentage %f, got %f", expected, fc.percentage)
	}
	return nil
}

func (fc *fuelContext) fuelShouldBeFull() error {
	if !fc.isFull {
		return fmt.Errorf("expected fuel to be full, but it was not")
	}
	return nil
}

func (fc *fuelContext) fuelShouldNotBeFull() error {
	if fc.isFull {
		return fmt.Errorf("expected fuel not to be full, but it was")
	}
	return nil
}

func (fc *fuelContext) theStringShouldBe(expected string) error {
	if fc.stringRep != expected {
		return fmt.Errorf("expected string '%s', got '%s'", expected, fc.stringRep)
	}
	return nil
}

func (fc *fuelContext) travelShouldBePossible() error {
	if !fc.canTravel {
		return fmt.Errorf("expected travel to be possible, but it was not")
	}
	return nil
}

func (fc *fuelContext) travelShouldNotBePossible() error {
	if fc.canTravel {
		return fmt.Errorf("expected travel not to be possible, but it was")
	}
	return nil
}

func (fc *fuelContext) fuelCreationShouldSucceed() error {
	if fc.err != nil {
		return fmt.Errorf("expected fuel creation to succeed, but got error: %v", fc.err)
	}
	if fc.fuel == nil {
		return fmt.Errorf("expected fuel to be created, but it was nil")
	}
	return nil
}

func (fc *fuelContext) fuelCreationShouldFailWith(expectedError string) error {
	if fc.err == nil {
		return fmt.Errorf("expected fuel creation to fail with '%s', but it succeeded", expectedError)
	}
	if fc.err.Error() != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, fc.err.Error())
	}
	return nil
}

func InitializeFuelScenario(ctx *godog.ScenarioContext) {
	fc := &fuelContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		fc.reset()
		return ctx, nil
	})

	// Given steps
	ctx.Step(`^a fuel value object with (\d+) current and (\d+) capacity$`, fc.aFuelValueObjectWithCurrentAndCapacity)
	ctx.Step(`^I create fuel with (-?\d+) current and (-?\d+) capacity$`, fc.iCreateFuelWithCurrentAndCapacity)

	// When steps
	ctx.Step(`^I get the fuel percentage$`, fc.iCalculateTheFuelPercentage)
	ctx.Step(`^I check if the fuel is full$`, fc.iCheckIfFuelIsFull)
	ctx.Step(`^I get the fuel string representation$`, fc.iGetTheFuelStringRepresentation)
	ctx.Step(`^I check if fuel can travel (\d+) units with safety margin ([0-9.]+)$`, fc.iCheckIfFuelCanTravelUnitsWithSafetyMargin)

	// Then steps
	ctx.Step(`^the fuel percentage should be ([0-9.]+)$`, fc.theFuelPercentageShouldBe)
	ctx.Step(`^fuel should be full$`, fc.fuelShouldBeFull)
	ctx.Step(`^fuel should not be full$`, fc.fuelShouldNotBeFull)
	ctx.Step(`^the string should be "([^"]*)"$`, fc.theStringShouldBe)
	ctx.Step(`^travel should be possible$`, fc.travelShouldBePossible)
	ctx.Step(`^travel should not be possible$`, fc.travelShouldNotBePossible)
	ctx.Step(`^fuel creation should succeed$`, fc.fuelCreationShouldSucceed)
	ctx.Step(`^fuel creation should fail with "([^"]*)"$`, fc.fuelCreationShouldFailWith)
}
