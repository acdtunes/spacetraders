package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/cucumber/godog"
)

type containerContext struct {
	container  *container.Container
	err        error
	boolResult bool
	metadata   map[string]interface{}
	startTime  time.Time
	stopTime   time.Time
}

func (cc *containerContext) reset() {
	cc.container = nil
	cc.err = nil
	cc.boolResult = false
	cc.metadata = make(map[string]interface{})
	cc.startTime = time.Time{}
	cc.stopTime = time.Time{}
}

func (cc *containerContext) iCreateAContainerWith(table *godog.Table) error {
	var id string
	var containerType container.ContainerType
	var playerID, maxIterations int
	metadata := make(map[string]interface{})

	for i, row := range table.Rows {
		if i == 0 {
			continue
		}
		key := row.Cells[0].Value
		val := row.Cells[1].Value

		switch key {
		case "id":
			id = val
		case "type":
			containerType = container.ContainerType(val)
		case "player_id":
			fmt.Sscanf(val, "%d", &playerID)
		case "max_iterations":
			fmt.Sscanf(val, "%d", &maxIterations)
		}
	}

	cc.container = container.NewContainer(id, containerType, playerID, maxIterations, metadata)
	return nil
}

func (cc *containerContext) theContainerShouldHaveID(id string) error {
	if cc.container.ID() != id {
		return fmt.Errorf("expected id '%s' but got '%s'", id, cc.container.ID())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveType(containerType string) error {
	if string(cc.container.Type()) != containerType {
		return fmt.Errorf("expected type '%s' but got '%s'", containerType, cc.container.Type())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHavePlayerID(playerID int) error {
	if cc.container.PlayerID() != playerID {
		return fmt.Errorf("expected player_id %d but got %d", playerID, cc.container.PlayerID())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveMaxIterations(maxIterations int) error {
	if cc.container.MaxIterations() != maxIterations {
		return fmt.Errorf("expected max_iterations %d but got %d", maxIterations, cc.container.MaxIterations())
	}
	return nil
}

func (cc *containerContext) theContainerShouldHaveStatus(status string) error {
	if string(cc.container.Status()) != status {
		return fmt.Errorf("expected status '%s' but got '%s'", status, cc.container.Status())
	}
	return nil
}

func (cc *containerContext) theContainerCurrentIterationShouldBe(iteration int) error {
	if cc.container.CurrentIteration() != iteration {
		return fmt.Errorf("expected current_iteration %d but got %d", iteration, cc.container.CurrentIteration())
	}
	return nil
}

func (cc *containerContext) theContainerRestartCountShouldBe(count int) error {
	if cc.container.RestartCount() != count {
		return fmt.Errorf("expected restart_count %d but got %d", count, cc.container.RestartCount())
	}
	return nil
}

func (cc *containerContext) aContainerInStatus(status string) error {
	cc.container = container.NewContainer(
		"container-1",
		container.ContainerTypeNavigate,
		1,
		10,
		make(map[string]interface{}),
	)

	// Set status based on input
	switch status {
	case "RUNNING":
		cc.container.Start()
	case "COMPLETED":
		cc.container.Start()
		cc.container.Complete()
	case "FAILED":
		cc.container.Start()
		cc.container.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		cc.container.Start()
		cc.container.Stop()
		cc.container.MarkStopped()
	case "STOPPING":
		cc.container.Start()
		cc.container.Stop()
	}

	return nil
}

func (cc *containerContext) iStartTheContainer() error {
	cc.err = cc.container.Start()
	return nil
}

func (cc *containerContext) iAttemptToStartTheContainer() error {
	cc.err = cc.container.Start()
	return nil
}

func (cc *containerContext) theContainerStartedAtShouldBeSet() error {
	if cc.container.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set but it is nil")
	}
	return nil
}

func (cc *containerContext) theOperationShouldFailWithError(expectedError string) error {
	if cc.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(cc.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, cc.err.Error())
	}
	return nil
}

// InitializeContainerScenario registers all container-related step definitions
func InitializeContainerScenario(ctx *godog.ScenarioContext) {
	cc := &containerContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		cc.reset()
		return ctx, nil
	})

	ctx.Step(`^I create a container with:$`, cc.iCreateAContainerWith)
	ctx.Step(`^the container should have id "([^"]*)"$`, cc.theContainerShouldHaveID)
	ctx.Step(`^the container should have type "([^"]*)"$`, cc.theContainerShouldHaveType)
	ctx.Step(`^the container should have player_id (\d+)$`, cc.theContainerShouldHavePlayerID)
	ctx.Step(`^the container should have max_iterations (-?\d+)$`, cc.theContainerShouldHaveMaxIterations)
	ctx.Step(`^the container should have status "([^"]*)"$`, cc.theContainerShouldHaveStatus)
	ctx.Step(`^the container current_iteration should be (\d+)$`, cc.theContainerCurrentIterationShouldBe)
	ctx.Step(`^the container restart_count should be (\d+)$`, cc.theContainerRestartCountShouldBe)
	ctx.Step(`^a container in "([^"]*)" status$`, cc.aContainerInStatus)
	ctx.Step(`^I start the container$`, cc.iStartTheContainer)
	ctx.Step(`^I attempt to start the container$`, cc.iAttemptToStartTheContainer)
	ctx.Step(`^the container started_at should be set$`, cc.theContainerStartedAtShouldBeSet)

	// Note: Complete implementation would include all remaining steps from the container feature file
}
