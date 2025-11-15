package steps

import (
	"context"
	"fmt"
	
	"strings"

	"github.com/cucumber/godog"
	"github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"

	"github.com/andrescamacho/spacetraders-go/test/helpers")

// WaypointCacheContext holds state for waypoint caching tests
type WaypointCacheContext struct {
	db              *gorm.DB
	repo            *persistence.GormWaypointRepository
	waypointSymbol  string
	systemSymbol    string
	waypointType    string
	x               float64
	y               float64
	traits          []string
	hasFuel         bool
	orbitals        []string
	waypointsList   []*shared.Waypoint
	retrievedWaypoint *shared.Waypoint
	saveError       error
	retrieveError   error
}

func InitializeWaypointCacheScenario(ctx *godog.ScenarioContext) {
	c := &WaypointCacheContext{}

	// Reset context before each scenario
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		return ctx, c.reset()
	})

	// Background steps
	ctx.Step(`^the database is initialized$`, c.theDatabaseIsInitialized)

	// Given steps
	ctx.Step(`^waypoint "([^"]*)" exists in database for system "([^"]*)" with:$`, c.waypointExistsInDatabaseForSystemWith)
	ctx.Step(`^waypoints exist in database for system "([^"]*)":$`, c.waypointsExistInDatabaseForSystem)

	// When steps
	ctx.Step(`^I save waypoint "([^"]*)" for system "([^"]*)" with:$`, c.iSaveWaypointForSystemWith)
	ctx.Step(`^I save waypoints for system "([^"]*)" with:$`, c.iSaveWaypointsForSystemWith)
	ctx.Step(`^I query waypoint "([^"]*)" from system "([^"]*)"$`, c.iQueryWaypointFromSystem)
	ctx.Step(`^I list waypoints for system "([^"]*)"$`, c.iListWaypointsForSystem)
	ctx.Step(`^I filter waypoints for system "([^"]*)" by trait "([^"]*)"$`, c.iFilterWaypointsForSystemByTrait)
	ctx.Step(`^I filter waypoints for system "([^"]*)" by type "([^"]*)"$`, c.iFilterWaypointsForSystemByType)
	ctx.Step(`^I filter waypoints for system "([^"]*)" with fuel available$`, c.iFilterWaypointsForSystemWithFuelAvailable)
	ctx.Step(`^I save waypoint "([^"]*)" for system "([^"]*)" with orbitals:$`, c.iSaveWaypointForSystemWithOrbitals)

	// Then steps
	ctx.Step(`^the waypoint should be saved in the database$`, c.theWaypointShouldBeSavedInTheDatabase)
	ctx.Step(`^waypoint "([^"]*)" should exist in the database$`, c.waypointShouldExistInTheDatabase)
	ctx.Step(`^all waypoints should be saved in the database$`, c.allWaypointsShouldBeSavedInTheDatabase)
	ctx.Step(`^the database should have (\d+) waypoints for system "([^"]*)"$`, c.theDatabaseShouldHaveNWaypointsForSystem)
	ctx.Step(`^I should receive the waypoint$`, c.iShouldReceiveTheWaypoint)
	ctx.Step(`^the waypoint should have type "([^"]*)"$`, c.theWaypointShouldHaveType)
	ctx.Step(`^the waypoint should have coordinates \(([^,]+), ([^)]+)\)$`, c.theWaypointShouldHaveCoordinates)
	ctx.Step(`^the waypoint should have traits "([^"]*)"$`, c.theWaypointShouldHaveTraits)
	ctx.Step(`^the waypoint should have fuel available$`, c.theWaypointShouldHaveFuelAvailable)
	ctx.Step(`^I should receive (\d+) waypoints?$`, c.iShouldReceiveNWaypoints)
	ctx.Step(`^the waypoint list should contain "([^"]*)"$`, c.theWaypointListShouldContain)
	ctx.Step(`^waypoint "([^"]*)" should have traits "([^"]*)"$`, c.waypointShouldHaveTraits)
	ctx.Step(`^waypoint "([^"]*)" should have fuel available$`, c.waypointShouldHaveFuelAvailable)
	ctx.Step(`^waypoint "([^"]*)" should have orbitals "([^"]*)"$`, c.waypointShouldHaveOrbitals)
	ctx.Step(`^waypoint "([^"]*)" should have coordinates \(([^,]+), ([^)]+)\)$`, c.waypointShouldHaveCoordinates)
}

func (c *WaypointCacheContext) reset() error {
	// Use shared test database and truncate all tables for test isolation
	if err := helpers.TruncateAllTables(); err != nil {
		return fmt.Errorf("failed to truncate tables: %w", err)
	}

	c.db = helpers.SharedTestDB
	c.repo = persistence.NewGormWaypointRepository(helpers.SharedTestDB)
	c.waypointsList = nil
	c.retrievedWaypoint = nil
	c.saveError = nil
	c.retrieveError = nil
	c.traits = nil
	c.orbitals = nil
	c.hasFuel = false

	return nil
}

// ============================================================================
// Background Steps
// ============================================================================

func (c *WaypointCacheContext) theDatabaseIsInitialized() error {
	// Already initialized in reset()
	return nil
}

// ============================================================================
// Given Steps
// ============================================================================

func (c *WaypointCacheContext) waypointExistsInDatabaseForSystemWith(waypointSymbol, systemSymbol string, table *godog.Table) error {
	// Parse table
	if len(table.Rows) < 2 {
		return fmt.Errorf("table must have header and data rows")
	}

	row := table.Rows[1]
	waypointType := getCellValue(table, row, "type")
	x := parseFloat(getCellValue(table, row, "x"))
	y := parseFloat(getCellValue(table, row, "y"))
	traitsStr := getCellValue(table, row, "traits")
	hasFuel := getCellValue(table, row, "has_fuel") == "true"

	// Create waypoint
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return fmt.Errorf("failed to create waypoint: %w", err)
	}
	waypoint.SystemSymbol = systemSymbol
	waypoint.Type = waypointType
	waypoint.HasFuel = hasFuel

	if traitsStr != "" {
		waypoint.Traits = strings.Split(traitsStr, ",")
	}

	// Save to database
	ctx := context.Background()
	if err := c.repo.Save(ctx, waypoint); err != nil {
		return fmt.Errorf("failed to save waypoint: %w", err)
	}

	return nil
}

func (c *WaypointCacheContext) waypointsExistInDatabaseForSystem(systemSymbol string, table *godog.Table) error {
	ctx := context.Background()

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		symbol := getCellValue(table, row, "symbol")
		waypointType := getCellValue(table, row, "type")
		x := parseFloat(getCellValue(table, row, "x"))
		y := parseFloat(getCellValue(table, row, "y"))
		traitsStr := getCellValue(table, row, "traits")
		hasFuel := getCellValue(table, row, "has_fuel") == "true"

		waypoint, err := shared.NewWaypoint(symbol, x, y)
		if err != nil {
			return fmt.Errorf("failed to create waypoint %s: %w", symbol, err)
		}
		waypoint.SystemSymbol = systemSymbol
		waypoint.Type = waypointType
		waypoint.HasFuel = hasFuel

		if traitsStr != "" {
			waypoint.Traits = strings.Split(traitsStr, ",")
		}

		if err := c.repo.Save(ctx, waypoint); err != nil {
			return fmt.Errorf("failed to save waypoint %s: %w", symbol, err)
		}
	}

	return nil
}

// ============================================================================
// When Steps
// ============================================================================

func (c *WaypointCacheContext) iSaveWaypointForSystemWith(waypointSymbol, systemSymbol string, table *godog.Table) error {
	if len(table.Rows) < 2 {
		return fmt.Errorf("table must have header and data rows")
	}

	row := table.Rows[1]
	waypointType := getCellValue(table, row, "type")
	x := parseFloat(getCellValue(table, row, "x"))
	y := parseFloat(getCellValue(table, row, "y"))
	traitsStr := getCellValue(table, row, "traits")
	hasFuel := getCellValue(table, row, "has_fuel") == "true"

	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		c.saveError = err
		return nil
	}
	waypoint.SystemSymbol = systemSymbol
	waypoint.Type = waypointType
	waypoint.HasFuel = hasFuel

	if traitsStr != "" {
		waypoint.Traits = strings.Split(traitsStr, ",")
	}

	ctx := context.Background()
	c.saveError = c.repo.Save(ctx, waypoint)
	c.waypointSymbol = waypointSymbol
	c.systemSymbol = systemSymbol

	return nil
}

func (c *WaypointCacheContext) iSaveWaypointsForSystemWith(systemSymbol string, table *godog.Table) error {
	ctx := context.Background()

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		symbol := getCellValue(table, row, "symbol")
		waypointType := getCellValue(table, row, "type")
		x := parseFloat(getCellValue(table, row, "x"))
		y := parseFloat(getCellValue(table, row, "y"))
		traitsStr := getCellValue(table, row, "traits")
		hasFuel := getCellValue(table, row, "has_fuel") == "true"
		orbitalsStr := getCellValue(table, row, "orbitals")

		waypoint, err := shared.NewWaypoint(symbol, x, y)
		if err != nil {
			c.saveError = err
			return nil
		}
		waypoint.SystemSymbol = systemSymbol
		waypoint.Type = waypointType
		waypoint.HasFuel = hasFuel

		if traitsStr != "" {
			waypoint.Traits = strings.Split(traitsStr, ",")
		}

		if orbitalsStr != "" {
			waypoint.Orbitals = strings.Split(orbitalsStr, ",")
		}

		if err := c.repo.Save(ctx, waypoint); err != nil {
			c.saveError = err
			return nil
		}
	}

	c.systemSymbol = systemSymbol
	return nil
}

func (c *WaypointCacheContext) iQueryWaypointFromSystem(waypointSymbol, systemSymbol string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, systemSymbol)
	c.retrievedWaypoint = waypoint
	c.retrieveError = err
	return nil
}

func (c *WaypointCacheContext) iListWaypointsForSystem(systemSymbol string) error {
	ctx := context.Background()
	waypoints, err := c.repo.ListBySystem(ctx, systemSymbol)
	c.waypointsList = waypoints
	c.retrieveError = err
	return nil
}

func (c *WaypointCacheContext) iFilterWaypointsForSystemByTrait(systemSymbol, trait string) error {
	ctx := context.Background()
	waypoints, err := c.repo.ListBySystemWithTrait(ctx, systemSymbol, trait)
	c.waypointsList = waypoints
	c.retrieveError = err
	return nil
}

func (c *WaypointCacheContext) iFilterWaypointsForSystemByType(systemSymbol, waypointType string) error {
	ctx := context.Background()
	waypoints, err := c.repo.ListBySystemWithType(ctx, systemSymbol, waypointType)
	c.waypointsList = waypoints
	c.retrieveError = err
	return nil
}

func (c *WaypointCacheContext) iFilterWaypointsForSystemWithFuelAvailable(systemSymbol string) error {
	ctx := context.Background()
	waypoints, err := c.repo.ListBySystemWithFuel(ctx, systemSymbol)
	c.waypointsList = waypoints
	c.retrieveError = err
	return nil
}

func (c *WaypointCacheContext) iSaveWaypointForSystemWithOrbitals(waypointSymbol, systemSymbol string, table *godog.Table) error {
	if len(table.Rows) < 2 {
		return fmt.Errorf("table must have header and data rows")
	}

	row := table.Rows[1]
	waypointType := getCellValue(table, row, "type")
	x := parseFloat(getCellValue(table, row, "x"))
	y := parseFloat(getCellValue(table, row, "y"))
	traitsStr := getCellValue(table, row, "traits")
	hasFuel := getCellValue(table, row, "has_fuel") == "true"
	orbitalsStr := getCellValue(table, row, "orbitals")

	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		c.saveError = err
		return nil
	}
	waypoint.SystemSymbol = systemSymbol
	waypoint.Type = waypointType
	waypoint.HasFuel = hasFuel

	if traitsStr != "" {
		waypoint.Traits = strings.Split(traitsStr, ",")
	}

	if orbitalsStr != "" {
		waypoint.Orbitals = strings.Split(orbitalsStr, ",")
	}

	ctx := context.Background()
	c.saveError = c.repo.Save(ctx, waypoint)
	c.waypointSymbol = waypointSymbol
	c.systemSymbol = systemSymbol

	return nil
}

// ============================================================================
// Then Steps
// ============================================================================

func (c *WaypointCacheContext) theWaypointShouldBeSavedInTheDatabase() error {
	require.NoError(nil, c.saveError, "Expected no error when saving waypoint")
	return nil
}

func (c *WaypointCacheContext) waypointShouldExistInTheDatabase(waypointSymbol string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, c.systemSymbol)
	require.NoError(nil, err, "Expected to find waypoint %s", waypointSymbol)
	require.NotNil(nil, waypoint, "Expected waypoint to exist")
	return nil
}

func (c *WaypointCacheContext) allWaypointsShouldBeSavedInTheDatabase() error {
	require.NoError(nil, c.saveError, "Expected no error when saving waypoints")
	return nil
}

func (c *WaypointCacheContext) theDatabaseShouldHaveNWaypointsForSystem(count int, systemSymbol string) error {
	ctx := context.Background()
	waypoints, err := c.repo.ListBySystem(ctx, systemSymbol)
	require.NoError(nil, err, "Expected no error when listing waypoints")
	assert.Equal(nil, count, len(waypoints), "Expected %d waypoints, got %d", count, len(waypoints))
	return nil
}

func (c *WaypointCacheContext) iShouldReceiveTheWaypoint() error {
	require.NoError(nil, c.retrieveError, "Expected no error when retrieving waypoint")
	require.NotNil(nil, c.retrievedWaypoint, "Expected to receive a waypoint")
	return nil
}

func (c *WaypointCacheContext) theWaypointShouldHaveType(waypointType string) error {
	assert.Equal(nil, waypointType, c.retrievedWaypoint.Type, "Waypoint type mismatch")
	return nil
}

func (c *WaypointCacheContext) theWaypointShouldHaveCoordinates(xStr, yStr string) error {
	x := parseFloat(strings.TrimSpace(xStr))
	y := parseFloat(strings.TrimSpace(yStr))
	assert.InDelta(nil, x, c.retrievedWaypoint.X, 0.001, "X coordinate mismatch")
	assert.InDelta(nil, y, c.retrievedWaypoint.Y, 0.001, "Y coordinate mismatch")
	return nil
}

func (c *WaypointCacheContext) theWaypointShouldHaveTraits(traitsStr string) error {
	expectedTraits := strings.Split(traitsStr, ",")
	assert.Equal(nil, len(expectedTraits), len(c.retrievedWaypoint.Traits), "Traits count mismatch")
	for _, trait := range expectedTraits {
		found := false
		for _, actualTrait := range c.retrievedWaypoint.Traits {
			if actualTrait == trait {
				found = true
				break
			}
		}
		assert.True(nil, found, "Expected trait %s not found", trait)
	}
	return nil
}

func (c *WaypointCacheContext) theWaypointShouldHaveFuelAvailable() error {
	assert.True(nil, c.retrievedWaypoint.HasFuel, "Expected waypoint to have fuel available")
	return nil
}

func (c *WaypointCacheContext) iShouldReceiveNWaypoints(count int) error {
	if c.retrieveError != nil {
		return fmt.Errorf("expected no error when retrieving waypoints but got: %v", c.retrieveError)
	}
	if len(c.waypointsList) != count {
		return fmt.Errorf("expected %d waypoints, got %d", count, len(c.waypointsList))
	}
	return nil
}

func (c *WaypointCacheContext) theWaypointListShouldContain(waypointSymbol string) error {
	found := false
	for _, wp := range c.waypointsList {
		if wp.Symbol == waypointSymbol {
			found = true
			break
		}
	}
	assert.True(nil, found, "Expected waypoint list to contain %s", waypointSymbol)
	return nil
}

func (c *WaypointCacheContext) waypointShouldHaveTraits(waypointSymbol, traitsStr string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, c.systemSymbol)
	require.NoError(nil, err, "Expected to find waypoint %s", waypointSymbol)
	require.NotNil(nil, waypoint, "Expected waypoint to exist")

	expectedTraits := strings.Split(traitsStr, ",")
	assert.Equal(nil, len(expectedTraits), len(waypoint.Traits), "Traits count mismatch for %s", waypointSymbol)
	for _, trait := range expectedTraits {
		found := false
		for _, actualTrait := range waypoint.Traits {
			if actualTrait == trait {
				found = true
				break
			}
		}
		assert.True(nil, found, "Expected trait %s not found in waypoint %s", trait, waypointSymbol)
	}
	return nil
}

func (c *WaypointCacheContext) waypointShouldHaveFuelAvailable(waypointSymbol string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, c.systemSymbol)
	require.NoError(nil, err, "Expected to find waypoint %s", waypointSymbol)
	require.NotNil(nil, waypoint, "Expected waypoint to exist")
	assert.True(nil, waypoint.HasFuel, "Expected waypoint %s to have fuel available", waypointSymbol)
	return nil
}

func (c *WaypointCacheContext) waypointShouldHaveOrbitals(waypointSymbol, orbitalsStr string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, c.systemSymbol)
	require.NoError(nil, err, "Expected to find waypoint %s", waypointSymbol)
	require.NotNil(nil, waypoint, "Expected waypoint to exist")

	expectedOrbitals := strings.Split(orbitalsStr, ",")
	assert.Equal(nil, len(expectedOrbitals), len(waypoint.Orbitals), "Orbitals count mismatch for %s", waypointSymbol)
	for _, orbital := range expectedOrbitals {
		found := false
		for _, actualOrbital := range waypoint.Orbitals {
			if actualOrbital == orbital {
				found = true
				break
			}
		}
		assert.True(nil, found, "Expected orbital %s not found in waypoint %s", orbital, waypointSymbol)
	}
	return nil
}

func (c *WaypointCacheContext) waypointShouldHaveCoordinates(waypointSymbol, xStr, yStr string) error {
	ctx := context.Background()
	waypoint, err := c.repo.FindBySymbol(ctx, waypointSymbol, c.systemSymbol)
	require.NoError(nil, err, "Expected to find waypoint %s", waypointSymbol)
	require.NotNil(nil, waypoint, "Expected waypoint to exist")

	x := parseFloat(strings.TrimSpace(xStr))
	y := parseFloat(strings.TrimSpace(yStr))
	assert.InDelta(nil, x, waypoint.X, 0.001, "X coordinate mismatch for %s", waypointSymbol)
	assert.InDelta(nil, y, waypoint.Y, 0.001, "Y coordinate mismatch for %s", waypointSymbol)
	return nil
}

// ============================================================================
// Helper Functions
// ============================================================================

// getCellValueFromTable gets a cell value from a table row by column name
// It uses the first row (table.Rows[0]) as the header to find the column index
func getCellValueFromTable(table *godog.Table, row *messages.PickleTableRow, columnName string) string {
	if len(table.Rows) == 0 {
		return ""
	}

	headerRow := table.Rows[0]

	// Find column index by matching header
	for i, headerCell := range headerRow.Cells {
		if headerCell.Value == columnName {
			if i < len(row.Cells) {
				return row.Cells[i].Value
			}
			return ""
		}
	}

	return ""
}

// getCellValue is a helper for backwards compatibility - it attempts to find the value by index
// This version requires the table to have consistent column ordering
func getCellValue(table *godog.Table, row *messages.PickleTableRow, columnName string) string {
	return getCellValueFromTable(table, row, columnName)
}

