package steps

import (
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// timeNow returns the current time (used for reconstructing tasks with specific state)
func timeNow() time.Time {
	return time.Now()
}

type manufacturingTaskContext struct {
	task          *manufacturing.ManufacturingTask
	lastError     error
	dependencies  []string
}

func InitializeManufacturingTaskScenario(ctx *godog.ScenarioContext) {
	mtc := &manufacturingTaskContext{}

	// Background
	ctx.Step(`^a manufacturing task context$`, mtc.aManufacturingTaskContext)

	// Task Creation
	ctx.Step(`^I create an ACQUIRE task for "([^"]*)" from market "([^"]*)"$`, mtc.iCreateAnAcquireTask)
	ctx.Step(`^I create a DELIVER task for "([^"]*)" to market "([^"]*)" with dependencies$`, mtc.iCreateADeliverTaskWithDependencies)
	ctx.Step(`^I create a COLLECT task for "([^"]*)" from factory "([^"]*)"$`, mtc.iCreateACollectTask)
	ctx.Step(`^I create a SELL task for "([^"]*)" to market "([^"]*)"$`, mtc.iCreateASellTask)

	// Given states
	ctx.Step(`^a PENDING task with no dependencies$`, mtc.aPendingTaskWithNoDependencies)
	ctx.Step(`^a READY task$`, mtc.aReadyTask)
	ctx.Step(`^an ASSIGNED task with ship "([^"]*)"$`, mtc.anAssignedTaskWithShip)
	ctx.Step(`^an EXECUTING task$`, mtc.anExecutingTask)
	ctx.Step(`^a FAILED task with retry count (\d+) and max retries (\d+)$`, mtc.aFailedTaskWithRetryCount)
	ctx.Step(`^a COMPLETED task$`, mtc.aCompletedTask)
	ctx.Step(`^a COMPLETED task with cost (\d+) and revenue (\d+)$`, mtc.aCompletedTaskWithCostAndRevenue)
	ctx.Step(`^an ACQUIRE task for "([^"]*)" from market "([^"]*)"$`, mtc.anAcquireTask)
	ctx.Step(`^a DELIVER task for "([^"]*)" to market "([^"]*)"$`, mtc.aDeliverTask)
	ctx.Step(`^a COLLECT task for "([^"]*)" from factory "([^"]*)"$`, mtc.aCollectTask)
	ctx.Step(`^a SELL task for "([^"]*)" to market "([^"]*)"$`, mtc.aSellTask)

	// State transitions
	ctx.Step(`^I mark the task as ready$`, mtc.iMarkTheTaskAsReady)
	ctx.Step(`^I try to mark the task as ready$`, mtc.iTryToMarkTheTaskAsReady)
	ctx.Step(`^I assign ship "([^"]*)" to the task$`, mtc.iAssignShipToTheTask)
	ctx.Step(`^I try to assign ship "([^"]*)" to the task$`, mtc.iTryToAssignShipToTheTask)
	ctx.Step(`^I start the task execution$`, mtc.iStartTheTaskExecution)
	ctx.Step(`^I complete the task$`, mtc.iCompleteTheTask)
	ctx.Step(`^I fail the task with error "([^"]*)"$`, mtc.iFailTheTaskWithError)
	ctx.Step(`^I reset the task for retry$`, mtc.iResetTheTaskForRetry)
	ctx.Step(`^I try to reset the task for retry$`, mtc.iTryToResetTheTaskForRetry)
	ctx.Step(`^I rollback the task assignment$`, mtc.iRollbackTheTaskAssignment)

	// Financial tracking
	ctx.Step(`^I set the task total cost to (\d+)$`, mtc.iSetTheTaskTotalCost)
	ctx.Step(`^I set the task total revenue to (\d+)$`, mtc.iSetTheTaskTotalRevenue)

	// Assertions
	ctx.Step(`^the task should have type "([^"]*)"$`, mtc.theTaskShouldHaveType)
	ctx.Step(`^the task should have good "([^"]*)"$`, mtc.theTaskShouldHaveGood)
	ctx.Step(`^the task should have source market "([^"]*)"$`, mtc.theTaskShouldHaveSourceMarket)
	ctx.Step(`^the task should have target market "([^"]*)"$`, mtc.theTaskShouldHaveTargetMarket)
	ctx.Step(`^the task should have factory symbol "([^"]*)"$`, mtc.theTaskShouldHaveFactorySymbol)
	ctx.Step(`^the task should have status "([^"]*)"$`, mtc.theTaskShouldHaveStatus)
	ctx.Step(`^the task should have dependencies$`, mtc.theTaskShouldHaveDependencies)
	ctx.Step(`^the task should have assigned ship "([^"]*)"$`, mtc.theTaskShouldHaveAssignedShip)
	ctx.Step(`^the task ready_at should be set$`, mtc.theTaskReadyAtShouldBeSet)
	ctx.Step(`^the task started_at should be set$`, mtc.theTaskStartedAtShouldBeSet)
	ctx.Step(`^the task completed_at should be set$`, mtc.theTaskCompletedAtShouldBeSet)
	ctx.Step(`^the assigned ship should be released$`, mtc.theAssignedShipShouldBeReleased)
	ctx.Step(`^the task error message should be "([^"]*)"$`, mtc.theTaskErrorMessageShouldBe)
	ctx.Step(`^the retry count should be (\d+)$`, mtc.theRetryCountShouldBe)
	ctx.Step(`^the retry count should still be (\d+)$`, mtc.theRetryCountShouldStillBe)
	ctx.Step(`^the error message should be cleared$`, mtc.theErrorMessageShouldBeCleared)
	ctx.Step(`^the task can retry should be (true|false)$`, mtc.theTaskCanRetryShouldBe)
	ctx.Step(`^the task is terminal should be (true|false)$`, mtc.theTaskIsTerminalShouldBe)
	ctx.Step(`^the task total cost should be (\d+)$`, mtc.theTaskTotalCostShouldBe)
	ctx.Step(`^the task total revenue should be (\d+)$`, mtc.theTaskTotalRevenueShouldBe)
	ctx.Step(`^the task net profit should be (\d+)$`, mtc.theTaskNetProfitShouldBe)
	ctx.Step(`^the task destination should be "([^"]*)"$`, mtc.theTaskDestinationShouldBe)
	ctx.Step(`^the operation should fail with "([^"]*)"$`, mtc.theOperationShouldFailWith)
}

// Background
func (mtc *manufacturingTaskContext) aManufacturingTaskContext() error {
	mtc.task = nil
	mtc.lastError = nil
	mtc.dependencies = []string{"dep-1", "dep-2"}
	return nil
}

// Task Creation - Using new atomic task types
func (mtc *manufacturingTaskContext) iCreateAnAcquireTask(good, market string) error {
	// ACQUIRE is now ACQUIRE_DELIVER (atomic: buy from source AND deliver to target)
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, good, market, "X1-AU21-FACTORY", nil)
	return nil
}

func (mtc *manufacturingTaskContext) iCreateADeliverTaskWithDependencies(good, market string) error {
	// DELIVER is now part of ACQUIRE_DELIVER - use that with dependencies
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, good, "X1-AU21-SOURCE", market, mtc.dependencies)
	return nil
}

func (mtc *manufacturingTaskContext) iCreateACollectTask(good, factory string) error {
	// COLLECT is now COLLECT_SELL (atomic: collect from factory AND sell to market)
	mtc.task = manufacturing.NewCollectSellTask("pipeline-1", 1, good, factory, "X1-AU21-MARKET", nil)
	return nil
}

func (mtc *manufacturingTaskContext) iCreateASellTask(good, market string) error {
	// SELL is now part of COLLECT_SELL
	mtc.task = manufacturing.NewCollectSellTask("pipeline-1", 1, good, "X1-AU21-FACTORY", market, nil)
	return nil
}

// Given states
func (mtc *manufacturingTaskContext) aPendingTaskWithNoDependencies() error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	return nil
}

func (mtc *manufacturingTaskContext) aReadyTask() error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	return mtc.task.MarkReady()
}

func (mtc *manufacturingTaskContext) anAssignedTaskWithShip(shipSymbol string) error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	if err := mtc.task.MarkReady(); err != nil {
		return err
	}
	return mtc.task.AssignShip(shipSymbol)
}

func (mtc *manufacturingTaskContext) anExecutingTask() error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	if err := mtc.task.MarkReady(); err != nil {
		return err
	}
	if err := mtc.task.AssignShip("AGENT-1"); err != nil {
		return err
	}
	return mtc.task.StartExecution()
}

func (mtc *manufacturingTaskContext) aFailedTaskWithRetryCount(retryCount, maxRetries int) error {
	// Use ReconstructTask to create a task with specific retry count
	mtc.task = manufacturing.ReconstituteTask(
		"task-1",
		"pipeline-1",
		1,
		manufacturing.TaskTypeAcquireDeliver,
		manufacturing.TaskStatusFailed,
		"IRON_ORE",
		0,
		0,
		"X1-AU21-A1",
		"",
		"",
		nil,
		"",
		0,
		retryCount,
		maxRetries,
		0,
		0,
		"some error",
		timeNow(),
		nil,
		nil,
		nil,
	)
	return nil
}

func (mtc *manufacturingTaskContext) aCompletedTask() error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	if err := mtc.task.MarkReady(); err != nil {
		return err
	}
	if err := mtc.task.AssignShip("AGENT-1"); err != nil {
		return err
	}
	if err := mtc.task.StartExecution(); err != nil {
		return err
	}
	return mtc.task.Complete()
}

func (mtc *manufacturingTaskContext) aCompletedTaskWithCostAndRevenue(cost, revenue int) error {
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, "IRON_ORE", "X1-AU21-A1", "X1-AU21-FACTORY", nil)
	if err := mtc.task.MarkReady(); err != nil {
		return err
	}
	if err := mtc.task.AssignShip("AGENT-1"); err != nil {
		return err
	}
	if err := mtc.task.StartExecution(); err != nil {
		return err
	}
	mtc.task.SetTotalCost(cost)
	mtc.task.SetTotalRevenue(revenue)
	return mtc.task.Complete()
}

func (mtc *manufacturingTaskContext) anAcquireTask(good, market string) error {
	// ACQUIRE is now ACQUIRE_DELIVER
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, good, market, "X1-AU21-FACTORY", nil)
	return nil
}

func (mtc *manufacturingTaskContext) aDeliverTask(good, market string) error {
	// DELIVER is now part of ACQUIRE_DELIVER
	mtc.task = manufacturing.NewAcquireDeliverTask("pipeline-1", 1, good, "X1-AU21-SOURCE", market, nil)
	return nil
}

func (mtc *manufacturingTaskContext) aCollectTask(good, factory string) error {
	// COLLECT is now COLLECT_SELL
	mtc.task = manufacturing.NewCollectSellTask("pipeline-1", 1, good, factory, "X1-AU21-MARKET", nil)
	return nil
}

func (mtc *manufacturingTaskContext) aSellTask(good, market string) error {
	// SELL is now part of COLLECT_SELL
	mtc.task = manufacturing.NewCollectSellTask("pipeline-1", 1, good, "X1-AU21-FACTORY", market, nil)
	return nil
}

// State transitions
func (mtc *manufacturingTaskContext) iMarkTheTaskAsReady() error {
	mtc.lastError = mtc.task.MarkReady()
	return nil
}

func (mtc *manufacturingTaskContext) iTryToMarkTheTaskAsReady() error {
	mtc.lastError = mtc.task.MarkReady()
	return nil
}

func (mtc *manufacturingTaskContext) iAssignShipToTheTask(shipSymbol string) error {
	mtc.lastError = mtc.task.AssignShip(shipSymbol)
	return nil
}

func (mtc *manufacturingTaskContext) iTryToAssignShipToTheTask(shipSymbol string) error {
	mtc.lastError = mtc.task.AssignShip(shipSymbol)
	return nil
}

func (mtc *manufacturingTaskContext) iStartTheTaskExecution() error {
	mtc.lastError = mtc.task.StartExecution()
	return nil
}

func (mtc *manufacturingTaskContext) iCompleteTheTask() error {
	mtc.lastError = mtc.task.Complete()
	return nil
}

func (mtc *manufacturingTaskContext) iFailTheTaskWithError(errMsg string) error {
	mtc.lastError = mtc.task.Fail(errMsg)
	return nil
}

func (mtc *manufacturingTaskContext) iResetTheTaskForRetry() error {
	mtc.lastError = mtc.task.ResetForRetry()
	return nil
}

func (mtc *manufacturingTaskContext) iTryToResetTheTaskForRetry() error {
	mtc.lastError = mtc.task.ResetForRetry()
	return nil
}

func (mtc *manufacturingTaskContext) iRollbackTheTaskAssignment() error {
	mtc.lastError = mtc.task.RollbackAssignment()
	return nil
}

// Financial tracking
func (mtc *manufacturingTaskContext) iSetTheTaskTotalCost(cost int) error {
	mtc.task.SetTotalCost(cost)
	return nil
}

func (mtc *manufacturingTaskContext) iSetTheTaskTotalRevenue(revenue int) error {
	mtc.task.SetTotalRevenue(revenue)
	return nil
}

// Assertions
func (mtc *manufacturingTaskContext) theTaskShouldHaveType(expectedType string) error {
	if string(mtc.task.TaskType()) != expectedType {
		return fmt.Errorf("expected task type %s, got %s", expectedType, mtc.task.TaskType())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveGood(expectedGood string) error {
	if mtc.task.Good() != expectedGood {
		return fmt.Errorf("expected good %s, got %s", expectedGood, mtc.task.Good())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveSourceMarket(expectedMarket string) error {
	if mtc.task.SourceMarket() != expectedMarket {
		return fmt.Errorf("expected source market %s, got %s", expectedMarket, mtc.task.SourceMarket())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveTargetMarket(expectedMarket string) error {
	if mtc.task.TargetMarket() != expectedMarket {
		return fmt.Errorf("expected target market %s, got %s", expectedMarket, mtc.task.TargetMarket())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveFactorySymbol(expectedFactory string) error {
	if mtc.task.FactorySymbol() != expectedFactory {
		return fmt.Errorf("expected factory symbol %s, got %s", expectedFactory, mtc.task.FactorySymbol())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveStatus(expectedStatus string) error {
	if string(mtc.task.Status()) != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, mtc.task.Status())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveDependencies() error {
	if !mtc.task.HasDependencies() {
		return fmt.Errorf("expected task to have dependencies")
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskShouldHaveAssignedShip(expectedShip string) error {
	if mtc.task.AssignedShip() != expectedShip {
		return fmt.Errorf("expected assigned ship %s, got %s", expectedShip, mtc.task.AssignedShip())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskReadyAtShouldBeSet() error {
	if mtc.task.ReadyAt() == nil {
		return fmt.Errorf("expected ready_at to be set")
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskStartedAtShouldBeSet() error {
	if mtc.task.StartedAt() == nil {
		return fmt.Errorf("expected started_at to be set")
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskCompletedAtShouldBeSet() error {
	if mtc.task.CompletedAt() == nil {
		return fmt.Errorf("expected completed_at to be set")
	}
	return nil
}

func (mtc *manufacturingTaskContext) theAssignedShipShouldBeReleased() error {
	if mtc.task.AssignedShip() != "" {
		return fmt.Errorf("expected assigned ship to be released, got %s", mtc.task.AssignedShip())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskErrorMessageShouldBe(expectedMsg string) error {
	if mtc.task.ErrorMessage() != expectedMsg {
		return fmt.Errorf("expected error message %s, got %s", expectedMsg, mtc.task.ErrorMessage())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theRetryCountShouldBe(expectedCount int) error {
	if mtc.task.RetryCount() != expectedCount {
		return fmt.Errorf("expected retry count %d, got %d", expectedCount, mtc.task.RetryCount())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theRetryCountShouldStillBe(expectedCount int) error {
	return mtc.theRetryCountShouldBe(expectedCount)
}

func (mtc *manufacturingTaskContext) theErrorMessageShouldBeCleared() error {
	if mtc.task.ErrorMessage() != "" {
		return fmt.Errorf("expected error message to be cleared, got %s", mtc.task.ErrorMessage())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskCanRetryShouldBe(expected string) error {
	expectedBool := expected == "true"
	if mtc.task.CanRetry() != expectedBool {
		return fmt.Errorf("expected can retry to be %v, got %v", expectedBool, mtc.task.CanRetry())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskIsTerminalShouldBe(expected string) error {
	expectedBool := expected == "true"
	if mtc.task.IsTerminal() != expectedBool {
		return fmt.Errorf("expected is terminal to be %v, got %v", expectedBool, mtc.task.IsTerminal())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskTotalCostShouldBe(expectedCost int) error {
	if mtc.task.TotalCost() != expectedCost {
		return fmt.Errorf("expected total cost %d, got %d", expectedCost, mtc.task.TotalCost())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskTotalRevenueShouldBe(expectedRevenue int) error {
	if mtc.task.TotalRevenue() != expectedRevenue {
		return fmt.Errorf("expected total revenue %d, got %d", expectedRevenue, mtc.task.TotalRevenue())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskNetProfitShouldBe(expectedProfit int) error {
	if mtc.task.NetProfit() != expectedProfit {
		return fmt.Errorf("expected net profit %d, got %d", expectedProfit, mtc.task.NetProfit())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theTaskDestinationShouldBe(expectedDest string) error {
	if mtc.task.GetDestination() != expectedDest {
		return fmt.Errorf("expected destination %s, got %s", expectedDest, mtc.task.GetDestination())
	}
	return nil
}

func (mtc *manufacturingTaskContext) theOperationShouldFailWith(expectedError string) error {
	if mtc.lastError == nil {
		return fmt.Errorf("expected operation to fail with '%s', but it succeeded", expectedError)
	}
	if !strings.Contains(mtc.lastError.Error(), expectedError) {
		return fmt.Errorf("expected error to contain '%s', got '%s'", expectedError, mtc.lastError.Error())
	}
	return nil
}
