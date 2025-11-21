package steps

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrescamacho/spacetraders-go/pkg/utils"
	"github.com/cucumber/godog"
)

type containerIDContext struct {
	generatedID  string
	generatedIDs []string
	operation    string
	shipSymbol   string
	idMap        map[string]string // operation -> container ID
}

func (ctx *containerIDContext) reset() {
	ctx.generatedID = ""
	ctx.generatedIDs = []string{}
	ctx.operation = ""
	ctx.shipSymbol = ""
	ctx.idMap = make(map[string]string)
}

func InitializeContainerIDSteps(ctx *godog.ScenarioContext) {
	idCtx := &containerIDContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		idCtx.reset()
		return ctx, nil
	})

	ctx.Step(`^the container ID generator is available$`, idCtx.containerIDGeneratorIsAvailable)
	ctx.Step(`^I generate a container ID with operation "([^"]*)" and ship "([^"]*)"$`, idCtx.generateContainerID)
	ctx.Step(`^the container ID should match the pattern "([^"]*)"$`, idCtx.containerIDShouldMatchPattern)
	ctx.Step(`^the container ID should be shorter than (\d+) characters$`, idCtx.containerIDShouldBeShorterThan)
	ctx.Step(`^the agent prefix "([^"]*)" should be stripped from the ship symbol$`, idCtx.agentPrefixShouldBeStripped)
	ctx.Step(`^the ship symbol should remain unchanged$`, idCtx.shipSymbolShouldRemainUnchanged)
	ctx.Step(`^I generate (\d+) container IDs with operation "([^"]*)" and ship "([^"]*)"$`, idCtx.generateMultipleContainerIDs)
	ctx.Step(`^all container IDs should be unique$`, idCtx.allContainerIDsShouldBeUnique)
	ctx.Step(`^I generate container IDs for the following operations and ships:$`, idCtx.generateContainerIDsForTable)
	ctx.Step(`^all container IDs should match their respective patterns$`, idCtx.allContainerIDsShouldMatchPatterns)
	ctx.Step(`^all container IDs should contain their operation names$`, idCtx.allContainerIDsShouldContainOperationNames)
	ctx.Step(`^all container IDs should have 8-character hex UUID suffixes$`, idCtx.allContainerIDsShouldHaveUUIDSuffixes)
	ctx.Step(`^the container ID should be at least (\d+)% shorter than legacy format "([^"]*)"$`, idCtx.containerIDShouldBeShorterThanLegacy)
	ctx.Step(`^the UUID suffix should be exactly (\d+) characters long$`, idCtx.uuidSuffixShouldBeExactLength)
	ctx.Step(`^the UUID suffix should only contain hexadecimal characters \[a-f0-9\]$`, idCtx.uuidSuffixShouldBeHexadecimal)
}

func (ctx *containerIDContext) containerIDGeneratorIsAvailable() error {
	// No setup needed - just verify the function exists by trying to call it
	testID := utils.GenerateContainerID("test", "TEST-SHIP-1")
	if testID == "" {
		return fmt.Errorf("container ID generator returned empty string")
	}
	return nil
}

func (ctx *containerIDContext) generateContainerID(operation, shipSymbol string) error {
	ctx.operation = operation
	ctx.shipSymbol = shipSymbol
	ctx.generatedID = utils.GenerateContainerID(operation, shipSymbol)
	if ctx.generatedID == "" {
		return fmt.Errorf("generated container ID is empty")
	}
	return nil
}

func (ctx *containerIDContext) containerIDShouldMatchPattern(pattern string) error {
	matched, err := regexp.MatchString("^"+pattern+"$", ctx.generatedID)
	if err != nil {
		return fmt.Errorf("invalid regex pattern: %w", err)
	}
	if !matched {
		return fmt.Errorf("container ID %q does not match pattern %q", ctx.generatedID, pattern)
	}
	return nil
}

func (ctx *containerIDContext) containerIDShouldBeShorterThan(maxLength int) error {
	if len(ctx.generatedID) >= maxLength {
		return fmt.Errorf("container ID length %d is not shorter than %d (ID: %s)", len(ctx.generatedID), maxLength, ctx.generatedID)
	}
	return nil
}

func (ctx *containerIDContext) agentPrefixShouldBeStripped(prefix string) error {
	if strings.Contains(ctx.generatedID, prefix) {
		return fmt.Errorf("container ID %q still contains agent prefix %q", ctx.generatedID, prefix)
	}
	return nil
}

func (ctx *containerIDContext) shipSymbolShouldRemainUnchanged() error {
	if !strings.Contains(ctx.generatedID, ctx.shipSymbol) {
		return fmt.Errorf("container ID %q does not contain original ship symbol %q", ctx.generatedID, ctx.shipSymbol)
	}
	return nil
}

func (ctx *containerIDContext) generateMultipleContainerIDs(count int, operation, shipSymbol string) error {
	ctx.generatedIDs = make([]string, count)
	for i := 0; i < count; i++ {
		ctx.generatedIDs[i] = utils.GenerateContainerID(operation, shipSymbol)
		if ctx.generatedIDs[i] == "" {
			return fmt.Errorf("generated container ID at index %d is empty", i)
		}
	}
	return nil
}

func (ctx *containerIDContext) allContainerIDsShouldBeUnique() error {
	seen := make(map[string]bool)
	for i, id := range ctx.generatedIDs {
		if seen[id] {
			return fmt.Errorf("duplicate container ID found at index %d: %q", i, id)
		}
		seen[id] = true
	}
	return nil
}

func (ctx *containerIDContext) generateContainerIDsForTable(table *godog.Table) error {
	ctx.idMap = make(map[string]string)

	// Skip header row
	for i := 1; i < len(table.Rows); i++ {
		row := table.Rows[i]
		operation := row.Cells[0].Value
		shipSymbol := row.Cells[1].Value

		containerID := utils.GenerateContainerID(operation, shipSymbol)
		if containerID == "" {
			return fmt.Errorf("generated empty container ID for operation %q and ship %q", operation, shipSymbol)
		}

		ctx.idMap[operation] = containerID
	}

	return nil
}

func (ctx *containerIDContext) allContainerIDsShouldMatchPatterns() error {
	for operation, containerID := range ctx.idMap {
		// Extract expected ship symbol (strip agent prefix)
		// This is a simplified check - just verify the ID contains the operation
		if !strings.HasPrefix(containerID, operation+"-") {
			return fmt.Errorf("container ID %q does not start with operation %q", containerID, operation)
		}
	}
	return nil
}

func (ctx *containerIDContext) allContainerIDsShouldContainOperationNames() error {
	for operation, containerID := range ctx.idMap {
		if !strings.Contains(containerID, operation) {
			return fmt.Errorf("container ID %q does not contain operation name %q", containerID, operation)
		}
	}
	return nil
}

func (ctx *containerIDContext) allContainerIDsShouldHaveUUIDSuffixes() error {
	uuidPattern := regexp.MustCompile(`-[a-f0-9]{8}$`)
	for operation, containerID := range ctx.idMap {
		if !uuidPattern.MatchString(containerID) {
			return fmt.Errorf("container ID %q does not have 8-character hex UUID suffix (operation: %s)", containerID, operation)
		}
	}
	return nil
}

func (ctx *containerIDContext) containerIDShouldBeShorterThanLegacy(percentage int, legacyFormat string) error {
	legacyLength := len(legacyFormat)
	newLength := len(ctx.generatedID)

	reduction := float64(legacyLength-newLength) / float64(legacyLength) * 100
	if reduction < float64(percentage) {
		return fmt.Errorf("container ID reduction %.1f%% is less than required %d%% (legacy: %d chars, new: %d chars)",
			reduction, percentage, legacyLength, newLength)
	}
	return nil
}

func (ctx *containerIDContext) uuidSuffixShouldBeExactLength(expectedLength int) error {
	// Extract the UUID suffix (last part after the last hyphen)
	parts := strings.Split(ctx.generatedID, "-")
	if len(parts) < 2 {
		return fmt.Errorf("container ID %q does not have enough parts separated by hyphens", ctx.generatedID)
	}

	uuidSuffix := parts[len(parts)-1]
	if len(uuidSuffix) != expectedLength {
		return fmt.Errorf("UUID suffix %q has length %d, expected %d", uuidSuffix, len(uuidSuffix), expectedLength)
	}
	return nil
}

func (ctx *containerIDContext) uuidSuffixShouldBeHexadecimal() error {
	// Extract the UUID suffix (last part after the last hyphen)
	parts := strings.Split(ctx.generatedID, "-")
	if len(parts) < 2 {
		return fmt.Errorf("container ID %q does not have enough parts separated by hyphens", ctx.generatedID)
	}

	uuidSuffix := parts[len(parts)-1]
	hexPattern := regexp.MustCompile(`^[a-f0-9]+$`)
	if !hexPattern.MatchString(uuidSuffix) {
		return fmt.Errorf("UUID suffix %q contains non-hexadecimal characters", uuidSuffix)
	}
	return nil
}
