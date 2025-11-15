package steps

import (
	"context"
	"fmt"
	"time"

	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// containerLoggingContext holds state for container logging tests
type containerLoggingContext struct {
	db                *gorm.DB
	repo              *persistence.GormContainerLogRepository
	clock             *shared.MockClock
	containerID       string
	playerID          int
	currentTime       time.Time
	logEntries        []persistence.ContainerLogEntry
	err               error
	logCount          int
	firstPageEntries  []persistence.ContainerLogEntry
	secondPageEntries []persistence.ContainerLogEntry
	thirdPageEntries  []persistence.ContainerLogEntry
	t                 *testing.T
}

func (clc *containerLoggingContext) reset(t *testing.T) {
	clc.t = t
	clc.db = nil
	clc.repo = nil
	clc.clock = shared.NewMockClock(time.Now())
	clc.containerID = ""
	clc.playerID = 0
	clc.currentTime = clc.clock.Now()
	clc.logEntries = nil
	clc.err = nil
	clc.logCount = 0
	clc.firstPageEntries = nil
	clc.secondPageEntries = nil
	clc.thirdPageEntries = nil
}

// ============================================================================
// Setup Steps
// ============================================================================

func (clc *containerLoggingContext) aContainerLogRepositoryWithInMemoryDatabase() error {
	// Create in-memory SQLite database
	db, err := database.NewTestConnection()
	if err != nil {
		return fmt.Errorf("failed to create test database: %w", err)
	}

	// Auto-migrate the container logs table
	if err := db.AutoMigrate(&persistence.ContainerLogModel{}); err != nil {
		return fmt.Errorf("failed to migrate container logs table: %w", err)
	}

	clc.db = db
	clc.repo = persistence.NewGormContainerLogRepository(db, clc.clock)
	clc.currentTime = clc.clock.Now()
	return nil
}

func (clc *containerLoggingContext) aContainerWithIDForPlayer(containerID string, playerID int) error {
	clc.containerID = containerID
	clc.playerID = playerID
	return nil
}

// ============================================================================
// Logging Action Steps
// ============================================================================

func (clc *containerLoggingContext) iLogAnINFOMessageForTheContainer(message string) error {
	ctx := context.Background()
	return clc.repo.Log(ctx, clc.containerID, clc.playerID, message, "INFO")
}

func (clc *containerLoggingContext) iLogAnERRORMessageForTheContainer(message string) error {
	ctx := context.Background()
	return clc.repo.Log(ctx, clc.containerID, clc.playerID, message, "ERROR")
}

func (clc *containerLoggingContext) iLogINFOMessagesForContainer(count int, containerID string) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("INFO message %d for container %s", i+1, containerID)
		if err := clc.repo.Log(ctx, containerID, clc.playerID, msg, "INFO"); err != nil {
			return err
		}
	}
	return nil
}

func (clc *containerLoggingContext) iLogERRORMessagesForContainer(count int, containerID string) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("ERROR message %d for container %s", i+1, containerID)
		if err := clc.repo.Log(ctx, containerID, clc.playerID, msg, "ERROR"); err != nil {
			return err
		}
	}
	return nil
}

func (clc *containerLoggingContext) iLogINFOMessagesForTheContainer(count int) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("INFO message %d", i+1)
		if err := clc.repo.Log(ctx, clc.containerID, clc.playerID, msg, "INFO"); err != nil {
			return err
		}
	}
	return nil
}

func (clc *containerLoggingContext) iLogERRORMessagesForTheContainer(count int) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("ERROR message %d", i+1)
		if err := clc.repo.Log(ctx, clc.containerID, clc.playerID, msg, "ERROR"); err != nil {
			return err
		}
	}
	return nil
}

func (clc *containerLoggingContext) iLogWARNINGMessageForTheContainer(count int) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("WARNING message %d", i+1)
		if err := clc.repo.Log(ctx, clc.containerID, clc.playerID, msg, "WARNING"); err != nil {
			return err
		}
	}
	return nil
}

func (clc *containerLoggingContext) iLogUniqueINFOMessagesForTheContainer(count int) error {
	ctx := context.Background()
	for i := 0; i < count; i++ {
		msg := fmt.Sprintf("Unique INFO message number %d at %d", i+1, clc.clock.Now().UnixNano())
		if err := clc.repo.Log(ctx, clc.containerID, clc.playerID, msg, "INFO"); err != nil {
			return err
		}
		// Advance mock clock to ensure unique timestamps - INSTANT!
		clc.clock.Advance(10 * time.Millisecond)
	}
	return nil
}

// ============================================================================
// Deduplication Steps
// ============================================================================

func (clc *containerLoggingContext) iLogTheSameINFOMessageSecondsLater(message string, secondsLater int) error {
	clc.clock.Advance(time.Duration(secondsLater) * time.Second) // Instant time advance!
	ctx := context.Background()
	return clc.repo.Log(ctx, clc.containerID, clc.playerID, message, "INFO")
}

func (clc *containerLoggingContext) iLogTheSameINFOMessageSecondsAfterTheFirstLog(message string, secondsAfter int) error {
	// Calculate elapsed time since first log
	elapsed := clc.clock.Now().Sub(clc.currentTime)
	remaining := time.Duration(secondsAfter)*time.Second - elapsed
	if remaining > 0 {
		clc.clock.Advance(remaining) // Instant time advance!
	}
	ctx := context.Background()
	return clc.repo.Log(ctx, clc.containerID, clc.playerID, message, "INFO")
}

// ============================================================================
// Query Steps
// ============================================================================

func (clc *containerLoggingContext) iQueryLogsForContainerWithLimit(containerID string, limit int) error {
	ctx := context.Background()
	logs, err := clc.repo.GetLogs(ctx, containerID, clc.playerID, limit, nil, nil)
	if err != nil {
		clc.err = err
		return err
	}
	clc.logEntries = logs
	return nil
}

func (clc *containerLoggingContext) iQueryLogsForTheContainerFilteredByLevel(level string) error {
	ctx := context.Background()
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, 100, &level, nil)
	if err != nil {
		clc.err = err
		return err
	}
	clc.logEntries = logs
	return nil
}

func (clc *containerLoggingContext) iShouldReceiveExactlyLogEntryWhenQuerying() error {
	ctx := context.Background()
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, 100, nil, nil)
	if err != nil {
		return err
	}
	clc.logEntries = logs
	require.Equal(clc.t, 1, len(logs), "expected exactly 1 log entry but got %d", len(logs))
	return nil
}

func (clc *containerLoggingContext) iShouldReceiveExactlyLogEntriesWhenQuerying(expectedCount int) error {
	ctx := context.Background()
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, 100, nil, nil)
	if err != nil {
		return err
	}
	clc.logEntries = logs
	require.Equal(clc.t, expectedCount, len(logs), "expected exactly %d log entries but got %d", expectedCount, len(logs))
	return nil
}

func (clc *containerLoggingContext) iQueryLogsWithLimitAndNoOffset(limit int) error {
	ctx := context.Background()
	// TODO: Need to add offset support to GetLogs
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, limit, nil, nil)
	if err != nil {
		clc.err = err
		return err
	}
	clc.firstPageEntries = logs
	clc.logEntries = logs
	return nil
}

func (clc *containerLoggingContext) iQueryLogsWithLimitAndOffset(limit, offset int) error {
	ctx := context.Background()
	logs, err := clc.repo.GetLogsWithOffset(ctx, clc.containerID, clc.playerID, limit, offset, nil, nil)
	if err != nil {
		clc.err = err
		return err
	}
	clc.logEntries = logs
	return nil
}

// ============================================================================
// Assertion Steps
// ============================================================================

func (clc *containerLoggingContext) theLogShouldBePersistedToTheDatabase() error {
	// Query the database to verify the log was persisted
	ctx := context.Background()
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, 1, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to query logs: %w", err)
	}
	if len(logs) == 0 {
		return fmt.Errorf("expected log to be persisted but found none")
	}
	clc.logEntries = logs
	return nil
}

func (clc *containerLoggingContext) theLogLevelShouldBe(expectedLevel string) error {
	if len(clc.logEntries) == 0 {
		return fmt.Errorf("no log entries found")
	}
	require.Equal(clc.t, expectedLevel, clc.logEntries[0].Level)
	return nil
}

func (clc *containerLoggingContext) theLogMessageShouldBe(expectedMessage string) error {
	if len(clc.logEntries) == 0 {
		return fmt.Errorf("no log entries found")
	}
	require.Equal(clc.t, expectedMessage, clc.logEntries[0].Message)
	return nil
}

func (clc *containerLoggingContext) theLogShouldHaveAContainerIdOf(expectedContainerID string) error {
	if len(clc.logEntries) == 0 {
		return fmt.Errorf("no log entries found")
	}
	require.Equal(clc.t, expectedContainerID, clc.logEntries[0].ContainerID)
	return nil
}

func (clc *containerLoggingContext) theLogShouldHaveAPlayerIdOf(expectedPlayerID int) error {
	if len(clc.logEntries) == 0 {
		return fmt.Errorf("no log entries found")
	}
	require.Equal(clc.t, expectedPlayerID, clc.logEntries[0].PlayerID)
	return nil
}

func (clc *containerLoggingContext) theLogShouldHaveATimestampWithinTheLastSeconds(seconds int) error {
	if len(clc.logEntries) == 0 {
		return fmt.Errorf("no log entries found")
	}
	now := time.Now()
	logTime := clc.logEntries[0].Timestamp
	diff := now.Sub(logTime)
	maxDuration := time.Duration(seconds) * time.Second
	require.True(clc.t, diff >= 0 && diff <= maxDuration,
		"timestamp not within expected range: %v (now: %v, log: %v)", diff, now, logTime)
	return nil
}

func (clc *containerLoggingContext) iShouldReceiveExactlyLogEntries(expectedCount int) error {
	actualCount := len(clc.logEntries)
	require.Equal(clc.t, expectedCount, actualCount,
		"expected %d log entries but got %d", expectedCount, actualCount)
	return nil
}

func (clc *containerLoggingContext) allLogEntriesShouldHaveContainerId(expectedContainerID string) error {
	for i, log := range clc.logEntries {
		require.Equal(clc.t, expectedContainerID, log.ContainerID,
			"log entry %d has container_id %s, expected %s", i, log.ContainerID, expectedContainerID)
	}
	return nil
}

func (clc *containerLoggingContext) allLogEntriesShouldHaveLevel(expectedLevel string) error {
	for i, log := range clc.logEntries {
		require.Equal(clc.t, expectedLevel, log.Level,
			"log entry %d has level %s, expected %s", i, log.Level, expectedLevel)
	}
	return nil
}

func (clc *containerLoggingContext) bothEntriesShouldHaveTheMessage(expectedMessage string) error {
	require.Equal(clc.t, 2, len(clc.logEntries), "expected exactly 2 entries")
	for i, log := range clc.logEntries {
		require.Equal(clc.t, expectedMessage, log.Message,
			"log entry %d has message %s, expected %s", i, log.Message, expectedMessage)
	}
	return nil
}

func (clc *containerLoggingContext) theLogEntriesShouldBeDifferentFromTheFirstPage() error {
	// Compare second page entries with first page entries
	if clc.firstPageEntries == nil {
		return fmt.Errorf("first page entries not captured")
	}
	if clc.logEntries == nil || len(clc.logEntries) == 0 {
		return fmt.Errorf("second page entries not found")
	}

	// Verify that log IDs are different
	firstPageIDs := make(map[int]bool)
	for _, log := range clc.firstPageEntries {
		firstPageIDs[log.ID] = true
	}

	for _, log := range clc.logEntries {
		if firstPageIDs[log.ID] {
			return fmt.Errorf("found duplicate log ID %d between pages", log.ID)
		}
	}

	return nil
}

func (clc *containerLoggingContext) theTotalCountOfAllLogsShouldBe(expectedTotal int) error {
	ctx := context.Background()
	// Query all logs without limit
	logs, err := clc.repo.GetLogs(ctx, clc.containerID, clc.playerID, 1000, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to query all logs: %w", err)
	}
	actualTotal := len(logs)
	require.Equal(clc.t, expectedTotal, actualTotal,
		"expected total count %d but got %d", expectedTotal, actualTotal)
	return nil
}

// ============================================================================
// Scenario Initialization
// ============================================================================

func InitializeContainerLoggingScenario(sc *godog.ScenarioContext) {
	clc := &containerLoggingContext{}

	// Before each scenario
	sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		clc.reset(&testing.T{})
		return ctx, nil
	})

	// Setup steps
	sc.Step(`^a container log repository with in-memory database$`, clc.aContainerLogRepositoryWithInMemoryDatabase)
	sc.Step(`^a container with ID "([^"]*)" for player (\d+)$`, clc.aContainerWithIDForPlayer)

	// Logging action steps
	sc.Step(`^I log an INFO message "([^"]*)" for the container$`, clc.iLogAnINFOMessageForTheContainer)
	sc.Step(`^I log an ERROR message "([^"]*)" for the container$`, clc.iLogAnERRORMessageForTheContainer)
	sc.Step(`^I log (\d+) INFO messages for container "([^"]*)"$`, clc.iLogINFOMessagesForContainer)
	sc.Step(`^I log (\d+) ERROR messages for container "([^"]*)"$`, clc.iLogERRORMessagesForContainer)
	sc.Step(`^I log (\d+) INFO messages for the container$`, clc.iLogINFOMessagesForTheContainer)
	sc.Step(`^I log (\d+) ERROR messages for the container$`, clc.iLogERRORMessagesForTheContainer)
	sc.Step(`^I log (\d+) WARNING message for the container$`, clc.iLogWARNINGMessageForTheContainer)
	sc.Step(`^I log (\d+) unique INFO messages for the container$`, clc.iLogUniqueINFOMessagesForTheContainer)

	// Deduplication steps
	sc.Step(`^I log the same INFO message "([^"]*)" (\d+) seconds later$`, clc.iLogTheSameINFOMessageSecondsLater)
	sc.Step(`^I log the same INFO message "([^"]*)" (\d+) seconds after the first log$`, clc.iLogTheSameINFOMessageSecondsAfterTheFirstLog)

	// Query steps
	sc.Step(`^I query logs for container "([^"]*)" with limit (\d+)$`, clc.iQueryLogsForContainerWithLimit)
	sc.Step(`^I query logs for the container filtered by level "([^"]*)"$`, clc.iQueryLogsForTheContainerFilteredByLevel)
	sc.Step(`^I should receive exactly (\d+) log entry when querying$`, clc.iShouldReceiveExactlyLogEntryWhenQuerying)
	sc.Step(`^I should receive exactly (\d+) log entries when querying$`, clc.iShouldReceiveExactlyLogEntriesWhenQuerying)
	sc.Step(`^I query logs with limit (\d+) and no offset$`, clc.iQueryLogsWithLimitAndNoOffset)
	sc.Step(`^I query logs with limit (\d+) and offset (\d+)$`, clc.iQueryLogsWithLimitAndOffset)

	// Assertion steps
	sc.Step(`^the log should be persisted to the database$`, clc.theLogShouldBePersistedToTheDatabase)
	sc.Step(`^the log level should be "([^"]*)"$`, clc.theLogLevelShouldBe)
	sc.Step(`^the log message should be "([^"]*)"$`, clc.theLogMessageShouldBe)
	sc.Step(`^the log should have a container_id of "([^"]*)"$`, clc.theLogShouldHaveAContainerIdOf)
	sc.Step(`^the log should have a player_id of (\d+)$`, clc.theLogShouldHaveAPlayerIdOf)
	sc.Step(`^the log should have a timestamp within the last (\d+) seconds$`, clc.theLogShouldHaveATimestampWithinTheLastSeconds)
	sc.Step(`^I should receive exactly (\d+) log entries$`, clc.iShouldReceiveExactlyLogEntries)
	sc.Step(`^all log entries should have container_id "([^"]*)"$`, clc.allLogEntriesShouldHaveContainerId)
	sc.Step(`^all log entries should have level "([^"]*)"$`, clc.allLogEntriesShouldHaveLevel)
	sc.Step(`^both entries should have the message "([^"]*)"$`, clc.bothEntriesShouldHaveTheMessage)
	sc.Step(`^the log entries should be different from the first page$`, clc.theLogEntriesShouldBeDifferentFromTheFirstPage)
	sc.Step(`^the total count of all logs should be (\d+)$`, clc.theTotalCountOfAllLogsShouldBe)
}
