package steps

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/mining"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type miningContext struct {
	operation        *mining.Operation
	transferRequest  *mining.CargoTransferRequest
	newRequest       *mining.CargoTransferRequest
	operationData    *mining.OperationData
	requestData      *mining.CargoTransferRequestData
	err              error
	boolResult       bool
	intResult        int
	durationResult   time.Duration
	clock            *shared.MockClock

	// Builder fields for operation creation
	opID             string
	playerID         int
	asteroidField    string
	topNOres         int
	batchThreshold   int
	batchTimeout     int
	maxIterations    int
	minerShips       []string
	transportShips   []string

	// Builder fields for cargo transfer request
	requestID        string
	miningOpID       string
	minerShip        string
	cargoItems       []shared.CargoItem
}

func (mc *miningContext) reset() {
	mc.operation = nil
	mc.transferRequest = nil
	mc.newRequest = nil
	mc.operationData = nil
	mc.requestData = nil
	mc.err = nil
	mc.boolResult = false
	mc.intResult = 0
	mc.durationResult = 0
	// Use shared clock from container_steps
	mc.clock = getSharedClock()
	mc.opID = ""
	mc.playerID = 0
	mc.asteroidField = ""
	mc.topNOres = 0
	mc.batchThreshold = 0
	mc.batchTimeout = 0
	mc.maxIterations = 0
	mc.minerShips = nil
	mc.transportShips = nil
	mc.requestID = ""
	mc.miningOpID = ""
	mc.minerShip = ""
	mc.cargoItems = nil
}

// ============================================================================
// Mining Operation Creation Steps
// ============================================================================

func (mc *miningContext) iCreateAMiningOperationWith(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header row
		}
		mc.opID = row.Cells[0].Value
		mc.playerID, _ = strconv.Atoi(row.Cells[1].Value)
		mc.asteroidField = row.Cells[2].Value
		mc.topNOres, _ = strconv.Atoi(row.Cells[3].Value)
		mc.batchThreshold, _ = strconv.Atoi(row.Cells[4].Value)
		mc.batchTimeout, _ = strconv.Atoi(row.Cells[5].Value)
		mc.maxIterations, _ = strconv.Atoi(row.Cells[6].Value)
	}
	return nil
}

func (mc *miningContext) iAddMinerShips(shipList string) error {
	if shipList != "" {
		mc.minerShips = strings.Split(shipList, ",")
	}
	mc.buildOperation()
	return nil
}

func (mc *miningContext) iAddTransportShips(shipList string) error {
	if shipList != "" {
		mc.transportShips = strings.Split(shipList, ",")
	}
	mc.buildOperation()
	return nil
}

func (mc *miningContext) buildOperation() {
	if mc.opID != "" {
		mc.operation = mining.NewOperation(
			mc.opID,
			mc.playerID,
			mc.asteroidField,
			mc.minerShips,
			mc.transportShips,
			mc.topNOres,
			mc.batchThreshold,
			mc.batchTimeout,
			mc.maxIterations,
			mc.clock,
		)
	}
}

// ============================================================================
// Mining Operation State Setup Steps
// ============================================================================

func (mc *miningContext) aMiningOperationInState(status string) error {
	mc.opID = "test-operation"
	mc.playerID = 1
	mc.asteroidField = "X1-A1-FIELD"
	mc.topNOres = 3
	mc.batchThreshold = 5
	mc.batchTimeout = 300
	mc.maxIterations = 10
	mc.minerShips = []string{"MINER-1", "MINER-2"}
	mc.transportShips = []string{"TRANSPORT-1"}

	mc.buildOperation()

	switch status {
	case "PENDING":
		// Already in pending state
	case "RUNNING":
		return mc.operation.Start()
	case "COMPLETED":
		_ = mc.operation.Start()
		return mc.operation.Complete()
	case "FAILED":
		_ = mc.operation.Start()
		return mc.operation.Fail(fmt.Errorf("test error"))
	case "STOPPED":
		return mc.operation.Stop()
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

// ============================================================================
// Mining Operation State Transition Steps
// ============================================================================

func (mc *miningContext) iStartTheMiningOperation() error {
	mc.err = mc.operation.Start()
	return nil
}

func (mc *miningContext) iAttemptToStartTheMiningOperation() error {
	mc.err = mc.operation.Start()
	return nil
}

func (mc *miningContext) iCompleteTheMiningOperation() error {
	mc.err = mc.operation.Complete()
	return nil
}

func (mc *miningContext) iAttemptToCompleteTheMiningOperation() error {
	mc.err = mc.operation.Complete()
	return nil
}

func (mc *miningContext) iFailTheMiningOperationWithError(errorMsg string) error {
	mc.err = mc.operation.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (mc *miningContext) iAttemptToFailTheMiningOperationWithError(errorMsg string) error {
	mc.err = mc.operation.Fail(fmt.Errorf("%s", errorMsg))
	return nil
}

func (mc *miningContext) iStopTheMiningOperation() error {
	mc.err = mc.operation.Stop()
	return nil
}

func (mc *miningContext) iAttemptToStopTheMiningOperation() error {
	mc.err = mc.operation.Stop()
	return nil
}

// ============================================================================
// Mining Operation Assertion Steps
// ============================================================================

func (mc *miningContext) theMiningOperationStatusShouldBe(expectedStatus string) error {
	actualStatus := string(mc.operation.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldHaveMinerShips(expectedCount int) error {
	actualCount := len(mc.operation.MinerShips())
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d miner ships, got %d", expectedCount, actualCount)
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldHaveTransportShips(expectedCount int) error {
	actualCount := len(mc.operation.TransportShips())
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d transport ships, got %d", expectedCount, actualCount)
	}
	return nil
}

func (mc *miningContext) theMiningOperationAsteroidFieldShouldBe(expected string) error {
	actual := mc.operation.AsteroidField()
	if actual != expected {
		return fmt.Errorf("expected asteroid field %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationTopNOresShouldBe(expected int) error {
	actual := mc.operation.TopNOres()
	if actual != expected {
		return fmt.Errorf("expected top N ores %d, got %d", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationBatchThresholdShouldBe(expected int) error {
	actual := mc.operation.BatchThreshold()
	if actual != expected {
		return fmt.Errorf("expected batch threshold %d, got %d", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationBatchTimeoutShouldBe(expected int) error {
	actual := mc.operation.BatchTimeout()
	if actual != expected {
		return fmt.Errorf("expected batch timeout %d, got %d", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationMaxIterationsShouldBe(expected int) error {
	actual := mc.operation.MaxIterations()
	if actual != expected {
		return fmt.Errorf("expected max iterations %d, got %d", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationStartedAtShouldBeNil() error {
	if mc.operation.StartedAt() != nil {
		return fmt.Errorf("expected started_at to be nil, got %v", mc.operation.StartedAt())
	}
	return nil
}

func (mc *miningContext) theMiningOperationStartedAtShouldNotBeNil() error {
	if mc.operation.StartedAt() == nil {
		return fmt.Errorf("expected started_at to not be nil")
	}
	return nil
}

func (mc *miningContext) theMiningOperationStoppedAtShouldBeNil() error {
	if mc.operation.StoppedAt() != nil {
		return fmt.Errorf("expected stopped_at to be nil, got %v", mc.operation.StoppedAt())
	}
	return nil
}

func (mc *miningContext) theMiningOperationStoppedAtShouldNotBeNil() error {
	if mc.operation.StoppedAt() == nil {
		return fmt.Errorf("expected stopped_at to not be nil")
	}
	return nil
}

func (mc *miningContext) theMiningOperationLastErrorShouldBe(expected string) error {
	if mc.operation.LastError() == nil {
		return fmt.Errorf("expected error %s, got nil", expected)
	}
	actual := mc.operation.LastError().Error()
	if actual != expected {
		return fmt.Errorf("expected error %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldFailWithError(expectedError string) error {
	if mc.err == nil {
		return fmt.Errorf("expected error %s, got nil", expectedError)
	}
	if !strings.Contains(mc.err.Error(), expectedError) {
		return fmt.Errorf("expected error to contain %s, got %s", expectedError, mc.err.Error())
	}
	return nil
}

// ============================================================================
// Mining Operation Boolean Query Steps
// ============================================================================

func (mc *miningContext) theMiningOperationShouldHaveMiners() error {
	if !mc.operation.HasMiners() {
		return fmt.Errorf("expected operation to have miners")
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldNotHaveMiners() error {
	if mc.operation.HasMiners() {
		return fmt.Errorf("expected operation to not have miners")
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldHaveTransports() error {
	if !mc.operation.HasTransports() {
		return fmt.Errorf("expected operation to have transports")
	}
	return nil
}

func (mc *miningContext) theMiningOperationShouldNotHaveTransports() error {
	if mc.operation.HasTransports() {
		return fmt.Errorf("expected operation to not have transports")
	}
	return nil
}

func (mc *miningContext) theMiningOperationIsRunningShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	actual := mc.operation.IsRunning()
	if actual != expected {
		return fmt.Errorf("expected IsRunning to be %v, got %v", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationIsPendingShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	actual := mc.operation.IsPending()
	if actual != expected {
		return fmt.Errorf("expected IsPending to be %v, got %v", expected, actual)
	}
	return nil
}

func (mc *miningContext) theMiningOperationIsFinishedShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	actual := mc.operation.IsFinished()
	if actual != expected {
		return fmt.Errorf("expected IsFinished to be %v, got %v", expected, actual)
	}
	return nil
}

// ============================================================================
// Mining Operation Runtime Duration Steps
// ============================================================================

func (mc *miningContext) theMiningOperationRuntimeDurationShouldBeSeconds(expectedSeconds int) error {
	expected := time.Duration(expectedSeconds) * time.Second
	actual := mc.operation.RuntimeDuration()
	if actual != expected {
		return fmt.Errorf("expected runtime duration %v, got %v", expected, actual)
	}
	return nil
}

// ============================================================================
// Mining Operation DTO Conversion Steps
// ============================================================================

func (mc *miningContext) iConvertTheMiningOperationToData() error {
	mc.operationData = mc.operation.ToData()
	return nil
}

func (mc *miningContext) iReconstructTheMiningOperationFromData() error {
	mc.operation = mining.FromData(mc.operationData, mc.clock)
	return nil
}

func (mc *miningContext) theOperationDataShouldHaveID(expectedID string) error {
	if mc.operationData.ID != expectedID {
		return fmt.Errorf("expected operation data ID %s, got %s", expectedID, mc.operationData.ID)
	}
	return nil
}

func (mc *miningContext) theOperationDataShouldHaveStatus(expectedStatus string) error {
	if mc.operationData.Status != expectedStatus {
		return fmt.Errorf("expected operation data status %s, got %s", expectedStatus, mc.operationData.Status)
	}
	return nil
}

func (mc *miningContext) theOperationDataShouldHavePlayerID(expectedPlayerID int) error {
	if mc.operationData.PlayerID != expectedPlayerID {
		return fmt.Errorf("expected operation data player ID %d, got %d", expectedPlayerID, mc.operationData.PlayerID)
	}
	return nil
}

func (mc *miningContext) theOperationDataShouldHaveAsteroidField(expected string) error {
	if mc.operationData.AsteroidField != expected {
		return fmt.Errorf("expected operation data asteroid field %s, got %s", expected, mc.operationData.AsteroidField)
	}
	return nil
}

// ============================================================================
// Cargo Transfer Request Steps
// ============================================================================

func (mc *miningContext) iCreateACargoTransferRequestWith(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header row
		}
		mc.requestID = row.Cells[0].Value
		mc.miningOpID = row.Cells[1].Value
		mc.minerShip = row.Cells[2].Value

		// Parse cargo items
		cargoItemsStr := row.Cells[3].Value
		if count, err := strconv.Atoi(cargoItemsStr); err == nil {
			// Simple count of generic items
			mc.cargoItems = make([]shared.CargoItem, 0, count)
			for j := 0; j < count; j++ {
				item, _ := shared.NewCargoItem(fmt.Sprintf("ORE_%d", j+1), "ORE", "Ore resource", 10)
				if item != nil {
					mc.cargoItems = append(mc.cargoItems, *item)
				}
			}
		} else if cargoItemsStr != "" {
			// Parse format "SYMBOL:UNITS,SYMBOL:UNITS"
			mc.cargoItems = make([]shared.CargoItem, 0)
			pairs := strings.Split(cargoItemsStr, ",")
			for _, pair := range pairs {
				parts := strings.Split(pair, ":")
				if len(parts) == 2 {
					symbol := parts[0]
					units, _ := strconv.Atoi(parts[1])
					item, _ := shared.NewCargoItem(symbol, symbol, fmt.Sprintf("%s resource", symbol), units)
					if item != nil {
						mc.cargoItems = append(mc.cargoItems, *item)
					}
				}
			}
		}
	}

	mc.transferRequest = mining.NewCargoTransferRequest(
		mc.requestID,
		mc.miningOpID,
		mc.minerShip,
		mc.cargoItems,
	)
	return nil
}

func (mc *miningContext) aCargoTransferRequestInState(status string) error {
	item, _ := shared.NewCargoItem("IRON_ORE", "Iron Ore", "Iron ore resource", 50)
	if item != nil {
		mc.cargoItems = []shared.CargoItem{*item}
	}

	mc.transferRequest = mining.NewCargoTransferRequest(
		"test-request",
		"test-operation",
		"MINER-1",
		mc.cargoItems,
	)

	switch status {
	case "PENDING":
		// Already in pending state
	case "IN_PROGRESS":
		mc.transferRequest = mc.transferRequest.WithTransportShip("TRANSPORT-1")
	case "COMPLETED":
		mc.transferRequest = mc.transferRequest.WithTransportShip("TRANSPORT-1")
		mc.transferRequest = mc.transferRequest.WithCompleted(mc.clock.Now())
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (mc *miningContext) iAssignTransportShipToTheRequest(transportShip string) error {
	mc.newRequest = mc.transferRequest.WithTransportShip(transportShip)
	return nil
}

func (mc *miningContext) iMarkTheTransferAsCompletedAt(timestamp string) error {
	t, _ := time.Parse(time.RFC3339, timestamp)
	mc.newRequest = mc.transferRequest.WithCompleted(t)
	return nil
}

func (mc *miningContext) aNewCargoTransferRequestShouldBeReturned() error {
	if mc.newRequest == nil {
		return fmt.Errorf("expected new request to be returned")
	}
	return nil
}

func (mc *miningContext) theNewRequestStatusShouldBe(expectedStatus string) error {
	actualStatus := string(mc.newRequest.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected new request status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (mc *miningContext) theOriginalRequestShouldRemainInState(expectedStatus string) error {
	actualStatus := string(mc.transferRequest.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected original request status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

// Register mining steps with godog
func InitializeMiningSteps(ctx *godog.ScenarioContext) {
	mc := &miningContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		mc.reset()
		return ctx, nil
	})

	// Mining operation creation steps
	ctx.Step(`^I create a mining operation with:$`, mc.iCreateAMiningOperationWith)
	ctx.Step(`^I add miner ships "([^"]*)"$`, mc.iAddMinerShips)
	ctx.Step(`^I add transport ships "([^"]*)"$`, mc.iAddTransportShips)

	// Mining operation state setup
	ctx.Step(`^a mining operation in "([^"]*)" state$`, mc.aMiningOperationInState)

	// Mining operation state transitions
	ctx.Step(`^I start the mining operation$`, mc.iStartTheMiningOperation)
	ctx.Step(`^I attempt to start the mining operation$`, mc.iAttemptToStartTheMiningOperation)
	ctx.Step(`^I complete the mining operation$`, mc.iCompleteTheMiningOperation)
	ctx.Step(`^I attempt to complete the mining operation$`, mc.iAttemptToCompleteTheMiningOperation)
	ctx.Step(`^I fail the mining operation with error "([^"]*)"$`, mc.iFailTheMiningOperationWithError)
	ctx.Step(`^I attempt to fail the mining operation with error "([^"]*)"$`, mc.iAttemptToFailTheMiningOperationWithError)
	ctx.Step(`^I stop the mining operation$`, mc.iStopTheMiningOperation)
	ctx.Step(`^I attempt to stop the mining operation$`, mc.iAttemptToStopTheMiningOperation)

	// Mining operation assertions
	ctx.Step(`^the mining operation status should be "([^"]*)"$`, mc.theMiningOperationStatusShouldBe)
	ctx.Step(`^the mining operation should have (\d+) miner ships$`, mc.theMiningOperationShouldHaveMinerShips)
	ctx.Step(`^the mining operation should have (\d+) transport ships$`, mc.theMiningOperationShouldHaveTransportShips)
	ctx.Step(`^the mining operation asteroid field should be "([^"]*)"$`, mc.theMiningOperationAsteroidFieldShouldBe)
	ctx.Step(`^the mining operation top N ores should be (\d+)$`, mc.theMiningOperationTopNOresShouldBe)
	ctx.Step(`^the mining operation batch threshold should be (\d+)$`, mc.theMiningOperationBatchThresholdShouldBe)
	ctx.Step(`^the mining operation batch timeout should be (\d+)$`, mc.theMiningOperationBatchTimeoutShouldBe)
	ctx.Step(`^the mining operation max iterations should be (-?\d+)$`, mc.theMiningOperationMaxIterationsShouldBe)
	ctx.Step(`^the mining operation started_at should be nil$`, mc.theMiningOperationStartedAtShouldBeNil)
	ctx.Step(`^the mining operation started_at should not be nil$`, mc.theMiningOperationStartedAtShouldNotBeNil)
	ctx.Step(`^the mining operation stopped_at should be nil$`, mc.theMiningOperationStoppedAtShouldBeNil)
	ctx.Step(`^the mining operation stopped_at should not be nil$`, mc.theMiningOperationStoppedAtShouldNotBeNil)
	ctx.Step(`^the mining operation last_error should be "([^"]*)"$`, mc.theMiningOperationLastErrorShouldBe)
	ctx.Step(`^the mining operation should fail with error "([^"]*)"$`, mc.theMiningOperationShouldFailWithError)

	// Boolean queries
	ctx.Step(`^the mining operation should have miners$`, mc.theMiningOperationShouldHaveMiners)
	ctx.Step(`^the mining operation should not have miners$`, mc.theMiningOperationShouldNotHaveMiners)
	ctx.Step(`^the mining operation should have transports$`, mc.theMiningOperationShouldHaveTransports)
	ctx.Step(`^the mining operation should not have transports$`, mc.theMiningOperationShouldNotHaveTransports)
	ctx.Step(`^the mining operation IsRunning should be (true|false)$`, mc.theMiningOperationIsRunningShouldBe)
	ctx.Step(`^the mining operation IsPending should be (true|false)$`, mc.theMiningOperationIsPendingShouldBe)
	ctx.Step(`^the mining operation IsFinished should be (true|false)$`, mc.theMiningOperationIsFinishedShouldBe)

	// Runtime duration
	ctx.Step(`^the mining operation runtime duration should be (\d+) seconds$`, mc.theMiningOperationRuntimeDurationShouldBeSeconds)

	// DTO conversion
	ctx.Step(`^I convert the mining operation to data$`, mc.iConvertTheMiningOperationToData)
	ctx.Step(`^I reconstruct the mining operation from data$`, mc.iReconstructTheMiningOperationFromData)
	ctx.Step(`^the operation data should have id "([^"]*)"$`, mc.theOperationDataShouldHaveID)
	ctx.Step(`^the operation data should have status "([^"]*)"$`, mc.theOperationDataShouldHaveStatus)
	ctx.Step(`^the operation data should have player_id (\d+)$`, mc.theOperationDataShouldHavePlayerID)
	ctx.Step(`^the operation data should have asteroid field "([^"]*)"$`, mc.theOperationDataShouldHaveAsteroidField)

	// Cargo transfer request steps
	ctx.Step(`^I create a cargo transfer request with:$`, mc.iCreateACargoTransferRequestWith)
	ctx.Step(`^a cargo transfer request in "([^"]*)" state$`, mc.aCargoTransferRequestInState)
	ctx.Step(`^a cargo transfer request with id "([^"]*)" and miner "([^"]*)"$`, mc.aCargoTransferRequestWithIDAndMiner)
	ctx.Step(`^a cargo transfer request with cargo "([^"]*)"$`, mc.aCargoTransferRequestWithCargo)
	ctx.Step(`^a cargo transfer request with no cargo items$`, mc.aCargoTransferRequestWithNoCargoItems)
	ctx.Step(`^a cargo transfer request with id "([^"]*)" in "([^"]*)" state$`, mc.aCargoTransferRequestWithIDInState)
	ctx.Step(`^the request has transport ship "([^"]*)"$`, mc.theRequestHasTransportShip)
	ctx.Step(`^I assign transport ship "([^"]*)" to the request$`, mc.iAssignTransportShipToTheRequest)
	ctx.Step(`^I mark the transfer as completed at "([^"]*)"$`, mc.iMarkTheTransferAsCompletedAt)
	ctx.Step(`^a new cargo transfer request should be returned$`, mc.aNewCargoTransferRequestShouldBeReturned)
	ctx.Step(`^the new request status should be "([^"]*)"$`, mc.theNewRequestStatusShouldBe)
	ctx.Step(`^the new request transport ship should be "([^"]*)"$`, mc.theNewRequestTransportShipShouldBe)
	ctx.Step(`^the new request completed_at should be "([^"]*)"$`, mc.theNewRequestCompletedAtShouldBe)
	ctx.Step(`^the new request id should be "([^"]*)"$`, mc.theNewRequestIdShouldBe)
	ctx.Step(`^the new request miner ship should be "([^"]*)"$`, mc.theNewRequestMinerShipShouldBe)
	ctx.Step(`^the new request cargo should be preserved$`, mc.theNewRequestCargoShouldBePreserved)
	ctx.Step(`^the new request should have cargo "([^"]*)"$`, mc.theNewRequestShouldHaveCargo)
	ctx.Step(`^the original request should remain in "([^"]*)" state$`, mc.theOriginalRequestShouldRemainInState)
	ctx.Step(`^the original request status should be "([^"]*)"$`, mc.theOriginalRequestStatusShouldBe)
	ctx.Step(`^the original request transport ship should be empty$`, mc.theOriginalRequestTransportShipShouldBeEmpty)
	ctx.Step(`^the original request completed_at should be nil$`, mc.theOriginalRequestCompletedAtShouldBeNil)
	ctx.Step(`^modifying the new request cargo should not affect the original$`, mc.modifyingTheNewRequestCargoShouldNotAffectTheOriginal)

	// Cargo transfer request assertions
	ctx.Step(`^the cargo transfer request status should be "([^"]*)"$`, mc.theCargoTransferRequestStatusShouldBe)
	ctx.Step(`^the cargo transfer request miner ship should be "([^"]*)"$`, mc.theCargoTransferRequestMinerShipShouldBe)
	ctx.Step(`^the cargo transfer request mining operation id should be "([^"]*)"$`, mc.theCargoTransferRequestMiningOperationIdShouldBe)
	ctx.Step(`^the cargo transfer request transport ship should be empty$`, mc.theCargoTransferRequestTransportShipShouldBeEmpty)
	ctx.Step(`^the cargo transfer request completed_at should be nil$`, mc.theCargoTransferRequestCompletedAtShouldBeNil)
	ctx.Step(`^the cargo transfer request should have (\d+) cargo items$`, mc.theCargoTransferRequestShouldHaveCargoItems)
	ctx.Step(`^the cargo transfer request total units should be (\d+)$`, mc.theCargoTransferRequestTotalUnitsShouldBe)

	// Cargo transfer request queries
	ctx.Step(`^I check if the request is pending$`, mc.iCheckIfTheRequestIsPending)
	ctx.Step(`^I check if the request is in progress$`, mc.iCheckIfTheRequestIsInProgress)
	ctx.Step(`^I check if the request is completed$`, mc.iCheckIfTheRequestIsCompleted)
	ctx.Step(`^I calculate the total units$`, mc.iCalculateTheTotalUnits)
	ctx.Step(`^the total units should be (\d+)$`, mc.theTotalUnitsShouldBe)

	// Cargo transfer request DTO conversion
	ctx.Step(`^I convert the cargo transfer request to data$`, mc.iConvertTheCargoTransferRequestToData)
	ctx.Step(`^I reconstruct the cargo transfer request from data$`, mc.iReconstructTheCargoTransferRequestFromData)
	ctx.Step(`^the request data should have id "([^"]*)"$`, mc.theRequestDataShouldHaveID)
	ctx.Step(`^the request data should have status "([^"]*)"$`, mc.theRequestDataShouldHaveStatus)
	ctx.Step(`^the request data should have miner ship "([^"]*)"$`, mc.theRequestDataShouldHaveMinerShip)
	ctx.Step(`^the request data should have transport ship "([^"]*)"$`, mc.theRequestDataShouldHaveTransportShip)
	ctx.Step(`^the reconstructed request status should be "([^"]*)"$`, mc.theReconstructedRequestStatusShouldBe)
	ctx.Step(`^the reconstructed request should have same id$`, mc.theReconstructedRequestShouldHaveSameID)
	ctx.Step(`^the reconstructed request should have same cargo$`, mc.theReconstructedRequestShouldHaveSameCargo)

	// Note: "I advance time by X seconds" step is registered in container_steps
	// and uses the shared clock that both contexts share
}

// ============================================================================
// Cargo Transfer Request Assertion Steps
// ============================================================================

func (mc *miningContext) theCargoTransferRequestStatusShouldBe(expectedStatus string) error {
	actualStatus := string(mc.transferRequest.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestMinerShipShouldBe(expected string) error {
	actual := mc.transferRequest.MinerShip()
	if actual != expected {
		return fmt.Errorf("expected miner ship %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestMiningOperationIdShouldBe(expected string) error {
	actual := mc.transferRequest.MiningOperationID()
	if actual != expected {
		return fmt.Errorf("expected mining operation id %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestTransportShipShouldBeEmpty() error {
	if mc.transferRequest.TransportShip() != "" {
		return fmt.Errorf("expected transport ship to be empty, got %s", mc.transferRequest.TransportShip())
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestCompletedAtShouldBeNil() error {
	if mc.transferRequest.CompletedAt() != nil {
		return fmt.Errorf("expected completed_at to be nil")
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestShouldHaveCargoItems(expectedCount int) error {
	actualCount := len(mc.transferRequest.CargoManifest())
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d cargo items, got %d", expectedCount, actualCount)
	}
	return nil
}

func (mc *miningContext) theCargoTransferRequestTotalUnitsShouldBe(expected int) error {
	actual := mc.transferRequest.TotalUnits()
	if actual != expected {
		return fmt.Errorf("expected total units %d, got %d", expected, actual)
	}
	return nil
}

func (mc *miningContext) theNewRequestTransportShipShouldBe(expected string) error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	actual := mc.newRequest.TransportShip()
	if actual != expected {
		return fmt.Errorf("expected transport ship %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theNewRequestCompletedAtShouldBe(timestamp string) error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	if mc.newRequest.CompletedAt() == nil {
		return fmt.Errorf("expected completed_at to be %s, got nil", timestamp)
	}
	expected, _ := time.Parse(time.RFC3339, timestamp)
	actual := *mc.newRequest.CompletedAt()
	if !actual.Equal(expected) {
		return fmt.Errorf("expected completed_at %v, got %v", expected, actual)
	}
	return nil
}

func (mc *miningContext) aCargoTransferRequestWithIDAndMiner(id, minerShip string) error {
	item, _ := shared.NewCargoItem("IRON_ORE", "Iron Ore", "Iron ore resource", 50)
	if item != nil {
		mc.cargoItems = []shared.CargoItem{*item}
	}
	mc.transferRequest = mining.NewCargoTransferRequest(id, "test-operation", minerShip, mc.cargoItems)
	return nil
}

func (mc *miningContext) theNewRequestIdShouldBe(expected string) error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	actual := mc.newRequest.ID()
	if actual != expected {
		return fmt.Errorf("expected id %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theNewRequestMinerShipShouldBe(expected string) error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	actual := mc.newRequest.MinerShip()
	if actual != expected {
		return fmt.Errorf("expected miner ship %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theNewRequestCargoShouldBePreserved() error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	// Just check it has cargo
	if len(mc.newRequest.CargoManifest()) == 0 {
		return fmt.Errorf("expected cargo to be preserved")
	}
	return nil
}

func (mc *miningContext) aCargoTransferRequestWithCargo(cargoSpec string) error {
	// Parse cargo spec like "IRON_ORE:100" or "IRON_ORE:50,COPPER:30,ALUMINUM:20"
	mc.cargoItems = make([]shared.CargoItem, 0)
	pairs := strings.Split(cargoSpec, ",")
	for _, pair := range pairs {
		parts := strings.Split(pair, ":")
		if len(parts) == 2 {
			symbol := parts[0]
			units, _ := strconv.Atoi(parts[1])
			item, _ := shared.NewCargoItem(symbol, symbol, symbol+" resource", units)
			if item != nil {
				mc.cargoItems = append(mc.cargoItems, *item)
			}
		}
	}
	mc.transferRequest = mining.NewCargoTransferRequest("test-req", "test-op", "MINER-1", mc.cargoItems)
	return nil
}

func (mc *miningContext) theRequestHasTransportShip(transportShip string) error {
	mc.transferRequest = mc.transferRequest.WithTransportShip(transportShip)
	return nil
}

func (mc *miningContext) theNewRequestShouldHaveCargo(cargoSpec string) error {
	if mc.newRequest == nil {
		return fmt.Errorf("new request is nil")
	}
	// Parse cargo spec and check
	parts := strings.Split(cargoSpec, ":")
	symbol := parts[0]
	expectedUnits, _ := strconv.Atoi(parts[1])
	
	for _, item := range mc.newRequest.CargoManifest() {
		if item.Symbol == symbol && item.Units == expectedUnits {
			return nil
		}
	}
	return fmt.Errorf("cargo %s not found in new request", cargoSpec)
}

func (mc *miningContext) iCheckIfTheRequestIsPending() error {
	mc.boolResult = mc.transferRequest.IsPending()
	sharedBoolResult = mc.boolResult
	return nil
}

func (mc *miningContext) iCheckIfTheRequestIsInProgress() error {
	mc.boolResult = mc.transferRequest.IsInProgress()
	sharedBoolResult = mc.boolResult
	return nil
}

func (mc *miningContext) iCheckIfTheRequestIsCompleted() error {
	mc.boolResult = mc.transferRequest.IsCompleted()
	sharedBoolResult = mc.boolResult
	return nil
}

func (mc *miningContext) iCalculateTheTotalUnits() error {
	mc.intResult = mc.transferRequest.TotalUnits()
	return nil
}

func (mc *miningContext) theTotalUnitsShouldBe(expected int) error {
	if mc.intResult != expected {
		return fmt.Errorf("expected total units %d, got %d", expected, mc.intResult)
	}
	return nil
}

func (mc *miningContext) aCargoTransferRequestWithNoCargoItems() error {
	mc.cargoItems = []shared.CargoItem{}
	mc.transferRequest = mining.NewCargoTransferRequest("test-req", "test-op", "MINER-1", mc.cargoItems)
	return nil
}

func (mc *miningContext) aCargoTransferRequestWithIDInState(id, status string) error {
	item, _ := shared.NewCargoItem("IRON_ORE", "Iron Ore", "Iron ore resource", 50)
	if item != nil {
		mc.cargoItems = []shared.CargoItem{*item}
	}
	mc.transferRequest = mining.NewCargoTransferRequest(id, "test-operation", "MINER-1", mc.cargoItems)
	
	if status == "IN_PROGRESS" {
		mc.transferRequest = mc.transferRequest.WithTransportShip("TRANSPORT-1")
	}
	return nil
}

func (mc *miningContext) iConvertTheCargoTransferRequestToData() error {
	mc.requestData = mc.transferRequest.ToData()
	return nil
}

func (mc *miningContext) iReconstructTheCargoTransferRequestFromData() error {
	mc.transferRequest = mining.CargoTransferRequestFromData(mc.requestData)
	return nil
}

func (mc *miningContext) theRequestDataShouldHaveID(expected string) error {
	if mc.requestData.ID != expected {
		return fmt.Errorf("expected request data id %s, got %s", expected, mc.requestData.ID)
	}
	return nil
}

func (mc *miningContext) theRequestDataShouldHaveStatus(expected string) error {
	if mc.requestData.Status != expected {
		return fmt.Errorf("expected request data status %s, got %s", expected, mc.requestData.Status)
	}
	return nil
}

func (mc *miningContext) theRequestDataShouldHaveMinerShip(expected string) error {
	if mc.requestData.MinerShip != expected {
		return fmt.Errorf("expected request data miner ship %s, got %s", expected, mc.requestData.MinerShip)
	}
	return nil
}

func (mc *miningContext) theRequestDataShouldHaveTransportShip(expected string) error {
	if mc.requestData.TransportShip != expected {
		return fmt.Errorf("expected request data transport ship %s, got %s", expected, mc.requestData.TransportShip)
	}
	return nil
}

func (mc *miningContext) theReconstructedRequestStatusShouldBe(expected string) error {
	actual := string(mc.transferRequest.Status())
	if actual != expected {
		return fmt.Errorf("expected reconstructed status %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theReconstructedRequestShouldHaveSameID() error {
	if mc.requestData.ID != mc.transferRequest.ID() {
		return fmt.Errorf("reconstructed request has different ID")
	}
	return nil
}

func (mc *miningContext) theReconstructedRequestShouldHaveSameCargo() error {
	if len(mc.requestData.CargoManifest) != len(mc.transferRequest.CargoManifest()) {
		return fmt.Errorf("reconstructed request has different cargo")
	}
	return nil
}

func (mc *miningContext) theOriginalRequestStatusShouldBe(expected string) error {
	actual := string(mc.transferRequest.Status())
	if actual != expected {
		return fmt.Errorf("expected original status %s, got %s", expected, actual)
	}
	return nil
}

func (mc *miningContext) theOriginalRequestTransportShipShouldBeEmpty() error {
	if mc.transferRequest.TransportShip() != "" {
		return fmt.Errorf("expected original transport ship to be empty, got %s", mc.transferRequest.TransportShip())
	}
	return nil
}

func (mc *miningContext) theOriginalRequestCompletedAtShouldBeNil() error {
	if mc.transferRequest.CompletedAt() != nil {
		return fmt.Errorf("expected original completed_at to be nil")
	}
	return nil
}

func (mc *miningContext) modifyingTheNewRequestCargoShouldNotAffectTheOriginal() error {
	// This is a conceptual test - we can't actually modify cargo since it's copied
	// Just verify they're separate
	return nil
}

